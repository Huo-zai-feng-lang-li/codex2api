package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

func TestNewUTLSNetDialTLSContextForcesHTTP11(t *testing.T) {
	offeredProtocols := make(chan []string, 1)
	server := httptest.NewUnstartedServer(nil)
	server.TLS = &tls.Config{
		NextProtos: []string{"h2", "http/1.1"},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			offeredProtocols <- append([]string(nil), hello.SupportedProtos...)
			return nil, nil
		},
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	t.Setenv("CODEX_TLS_SKIP_VERIFY", "true")

	conn, err := NewUTLSNetDialTLSContext("")(context.Background(), "tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial WSS TLS: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	tlsConn, ok := conn.(*utls.UConn)
	if !ok {
		t.Fatalf("connection type = %T, want *utls.UConn", conn)
	}
	if got := tlsConn.ConnectionState().NegotiatedProtocol; got != "http/1.1" {
		t.Fatalf("negotiated ALPN = %q, want http/1.1", got)
	}
	if got := <-offeredProtocols; !reflect.DeepEqual(got, []string{"http/1.1"}) {
		t.Fatalf("offered ALPN = %v, want [http/1.1]", got)
	}
}

func TestWebsocketClientHelloSpecOmitsHTTP2ALPS(t *testing.T) {
	spec, err := websocketClientHelloSpec()
	if err != nil {
		t.Fatalf("websocketClientHelloSpec: %v", err)
	}
	for _, extension := range spec.Extensions {
		switch extension.(type) {
		case *utls.ApplicationSettingsExtension, *utls.ApplicationSettingsExtensionNew:
			t.Fatalf("WSS ClientHello contains HTTP/2 ALPS extension %T", extension)
		}
	}
}

func TestNewUTLSTransportKeepsHTTP2(t *testing.T) {
	requestProtocol := make(chan int, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestProtocol <- r.ProtoMajor
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	t.Setenv("CODEX_TLS_SKIP_VERIFY", "true")

	response, err := NewUTLSHttpClient("").Get(server.URL)
	if err != nil {
		t.Fatalf("GET over uTLS HTTP transport: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })

	if response.ProtoMajor != 2 {
		t.Fatalf("response protocol = HTTP/%d, want HTTP/2", response.ProtoMajor)
	}
	if got := <-requestProtocol; got != 2 {
		t.Fatalf("request protocol = HTTP/%d, want HTTP/2", got)
	}
}

func TestUTLSTransportsRejectInvalidProxyInsteadOfDirectFallback(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	t.Setenv("CODEX_TLS_SKIP_VERIFY", "true")
	const invalidProxy = "ftp://127.0.0.1:1"

	response, err := NewUTLSHttpClient(invalidProxy).Get(server.URL)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("HTTP uTLS request unexpectedly bypassed invalid proxy")
	}

	conn, err := NewUTLSNetDialTLSContext(invalidProxy)(context.Background(), "tcp", server.Listener.Addr().String())
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("WebSocket uTLS dial unexpectedly bypassed invalid proxy")
	}
}

func TestNewUTLSNetDialTLSContextCancelsHTTPProxyConnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		time.Sleep(750 * time.Millisecond)
		_ = conn.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	conn, err := NewUTLSNetDialTLSContext("http://"+listener.Addr().String())(ctx, "tcp", "example.com:443")
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("proxy cancellation took %v, want under 500ms", elapsed)
	}
}
