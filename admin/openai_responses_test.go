package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
)

func TestFetchOpenAIResponsesModelIDsSupportsV1BaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("Authorization = %q, want Bearer sk-test", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1"},{"id":"gpt-4.1"},{"id":"gpt-4.1-mini"}]}`))
	}))
	defer server.Close()

	models, err := fetchOpenAIResponsesModelIDs(context.Background(), server.URL+"/v1", "sk-test", "")
	if err != nil {
		t.Fatalf("fetchOpenAIResponsesModelIDs returned error: %v", err)
	}
	want := []string{"gpt-4.1", "gpt-4.1-mini"}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestFetchOpenAIResponsesModelIDsIgnoresEnvironmentProxyWhenProxyURLBlank(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:51081")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:51081")
	t.Setenv("http_proxy", "http://127.0.0.1:51081")
	t.Setenv("https_proxy", "http://127.0.0.1:51081")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.4-mini"}]}`))
	}))
	defer server.Close()

	models, err := fetchOpenAIResponsesModelIDs(context.Background(), server.URL, "sk-test", "")
	if err != nil {
		t.Fatalf("fetchOpenAIResponsesModelIDs returned error: %v", err)
	}
	want := []string{"gpt-5.4-mini"}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestFetchOpenAIResponsesModelIDsUsesExplicitHTTPProxy(t *testing.T) {
	var gotProxyRequest bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("upstream path = %q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.4"}]}`))
	}))
	defer upstream.Close()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProxyRequest = true
		if r.URL.Scheme != "http" || r.URL.Host == "" {
			t.Fatalf("proxy saw URL = %q, want absolute-form HTTP proxy request", r.URL.String())
		}
		outReq := r.Clone(r.Context())
		outReq.RequestURI = ""
		resp, err := http.DefaultTransport.RoundTrip(outReq)
		if err != nil {
			t.Fatalf("proxy round trip: %v", err)
		}
		defer resp.Body.Close()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxyServer.Close()

	models, err := fetchOpenAIResponsesModelIDs(context.Background(), upstream.URL, "sk-test", proxyServer.URL)
	if err != nil {
		t.Fatalf("fetchOpenAIResponsesModelIDs returned error: %v", err)
	}
	if !gotProxyRequest {
		t.Fatal("expected /v1/models request to go through explicit proxy")
	}
	if !reflect.DeepEqual(models, []string{"gpt-5.4"}) {
		t.Fatalf("models = %#v, want %#v", models, []string{"gpt-5.4"})
	}
}

func TestResolveOpenAIResponsesModelsProxyFallsBackToStorePolicy(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{ProxyURL: " http://global-proxy:8080 "})
	handler := &Handler{store: store}

	got := handler.resolveOpenAIResponsesModelsProxy(&fetchOpenAIResponsesModelsReq{})
	if got != "http://global-proxy:8080" {
		t.Fatalf("resolveOpenAIResponsesModelsProxy() = %q, want global proxy", got)
	}
}

func TestResolveOpenAIResponsesModelsProxyPrefersRequestAndRuntimeAccount(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{ProxyURL: "http://global-proxy:8080"})
	store.AddAccount(&auth.Account{DBID: 42, ProxyURL: " http://account-proxy:8080 "})
	handler := &Handler{store: store}

	got := handler.resolveOpenAIResponsesModelsProxy(&fetchOpenAIResponsesModelsReq{
		AccountID: 42,
		ProxyURL:  " http://request-proxy:8080 ",
	})
	if got != "http://request-proxy:8080" {
		t.Fatalf("explicit request proxy = %q, want request proxy", got)
	}

	got = handler.resolveOpenAIResponsesModelsProxy(&fetchOpenAIResponsesModelsReq{AccountID: 42})
	if got != "http://account-proxy:8080" {
		t.Fatalf("runtime account proxy = %q, want account proxy", got)
	}
}

func TestResolveOpenAIResponsesModelsProxyReturnsDirectWhenNothingConfigured(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{})
	handler := &Handler{store: store}

	got := handler.resolveOpenAIResponsesModelsProxy(&fetchOpenAIResponsesModelsReq{})
	if got != "" {
		t.Fatalf("resolveOpenAIResponsesModelsProxy() = %q, want direct", got)
	}
}

func TestConnectionTestModelForOpenAIResponsesAccountUsesFirstSupportedFallback(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{TestModel: "gpt-5.4"})
	handler := &Handler{store: store}
	account := &auth.Account{
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      "https://api.openai.com",
		APIKey:       "sk-test",
		Models:       []string{"gpt-4.1-mini", "gpt-4.1"},
	}

	model, err := handler.connectionTestModelForAccount(context.Background(), account, "")
	if err != nil {
		t.Fatalf("connectionTestModelForAccount returned error: %v", err)
	}
	if model != "gpt-4.1-mini" {
		t.Fatalf("model = %q, want first account model", model)
	}

	model, err = handler.connectionTestModelForAccount(context.Background(), account, "gpt-4.1")
	if err != nil {
		t.Fatalf("requested model returned error: %v", err)
	}
	if model != "gpt-4.1" {
		t.Fatalf("requested model = %q, want gpt-4.1", model)
	}
}
