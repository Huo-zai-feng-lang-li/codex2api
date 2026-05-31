package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
)

func TestShouldFallbackUsageProbeAfterWhamFailure(t *testing.T) {
	tests := []struct {
		name string
		resp *http.Response
		err  error
		want bool
	}{
		{
			name: "transport error has no response and should not fallback",
			resp: nil,
			err:  errors.New("wham request: proxyconnect tcp: connect refused"),
			want: false,
		},
		{
			name: "http status response can fallback",
			resp: &http.Response{StatusCode: http.StatusBadGateway},
			err:  errors.New("wham returned status 502"),
			want: true,
		},
		{
			name: "parse failure after response can fallback",
			resp: &http.Response{StatusCode: http.StatusOK},
			err:  errors.New("parse wham response"),
			want: true,
		},
		{
			name: "success does not fallback",
			resp: &http.Response{StatusCode: http.StatusOK},
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFallbackUsageProbeAfterWhamFailure(tt.resp, tt.err); got != tt.want {
				t.Fatalf("shouldFallbackUsageProbeAfterWhamFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProbeUsageSnapshotUsesOpenAIResponsesAPIRequest(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("Authorization = %q, want Bearer sk-test", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if len(body) == 0 {
			t.Fatal("request body should not be empty")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response","status":"completed","output":[]}`))
	}))
	defer server.Close()

	store := auth.NewStore(nil, nil, &database.SystemSettings{TestModel: "gpt-5.4", MaxConcurrency: 1})
	handler := &Handler{store: store}
	account := &auth.Account{
		DBID:         1,
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      server.URL,
		APIKey:       "sk-test",
		Models:       []string{"gpt-5.4"},
		Status:       auth.StatusReady,
		HealthTier:   auth.HealthTierHealthy,
	}

	if err := handler.ProbeUsageSnapshot(context.Background(), account); err != nil {
		t.Fatalf("ProbeUsageSnapshot returned error: %v", err)
	}
	if !called {
		t.Fatal("ProbeUsageSnapshot should call the OpenAI Responses upstream")
	}
}

func TestProbeUsageSnapshotPaymentRequiredRecordsCooldown(t *testing.T) {
	upstreamBody := `{"error":"Usage limit reached, will reset on Jun 2 at 11:22 PM (UTC+8)"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer server.Close()

	store := auth.NewStore(nil, nil, &database.SystemSettings{TestModel: "gpt-5.4-mini", MaxConcurrency: 1})
	handler := &Handler{store: store}
	account := &auth.Account{
		DBID:         2,
		UpstreamType: auth.UpstreamOpenAIResponses,
		BaseURL:      server.URL,
		APIKey:       "sk-test",
		Models:       []string{"gpt-5.4-mini"},
		Status:       auth.StatusReady,
		HealthTier:   auth.HealthTierHealthy,
	}

	err := handler.ProbeUsageSnapshot(context.Background(), account)

	if err == nil {
		t.Fatal("ProbeUsageSnapshot should return an error for payment_required")
	}
	if got := account.RuntimeStatus(); got != "payment_required" {
		t.Fatalf("RuntimeStatus() = %q, want payment_required", got)
	}
	account.Mu().RLock()
	errorMsg := account.ErrorMsg
	account.Mu().RUnlock()
	if !strings.Contains(errorMsg, "Usage limit reached") {
		t.Fatalf("ErrorMsg = %q, want usage limit message", errorMsg)
	}
}
