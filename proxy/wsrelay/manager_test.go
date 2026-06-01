package wsrelay

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/gorilla/websocket"
)

func TestAcquireConnectionReusesIdleConnectedConnection(t *testing.T) {
	manager := NewManager()
	t.Cleanup(manager.Stop)
	// 测试环境无真实 WebSocket 连接，注入探活函数跳过 Ping
	manager.probeFunc = func(wc *WsConnection) bool { return true }

	account := &auth.Account{DBID: 42}
	wsURL := "wss://example.test/responses"
	key := manager.poolKey(account.ID(), wsURL, "session-1", "")

	session := NewSession(account.ID(), manager)
	session.SetConnected(true)
	conn := &WsConnection{
		session:  session,
		URL:      wsURL,
		PoolKey:  key,
		httpResp: &http.Response{StatusCode: http.StatusSwitchingProtocols},
	}
	conn.SetState(StateConnected)
	conn.Touch()
	manager.connections.Store(key, conn)
	manager.sessions.Store(key, session)

	got, pr, err := manager.AcquireConnection(context.Background(), account, wsURL, "session-1", http.Header{}, "")
	if err != nil {
		t.Fatalf("AcquireConnection() error = %v", err)
	}
	if got != conn {
		t.Fatal("expected existing connection to be reused")
	}
	if pr == nil {
		t.Fatal("expected pending request reservation")
	}
	if session.PendingCount() != 1 {
		t.Fatalf("PendingCount = %d, want %d", session.PendingCount(), 1)
	}
	session.RemovePendingRequest(pr.RequestID)
}

func TestPoolKeyIncludesSessionKey(t *testing.T) {
	manager := NewManager()
	t.Cleanup(manager.Stop)

	keyA := manager.poolKey(42, "wss://example.test/responses", "session-a", "")
	keyB := manager.poolKey(42, "wss://example.test/responses", "session-b", "")
	if keyA == keyB {
		t.Fatal("expected different session keys to produce different pool keys")
	}
}

func TestPoolKeyKeepsSameSessionStable(t *testing.T) {
	manager := NewManager()
	t.Cleanup(manager.Stop)

	keyA := manager.poolKey(42, "wss://example.test/responses", "session-a", "http://proxy-a")
	keyB := manager.poolKey(42, "wss://example.test/responses", "session-a", "http://proxy-a")
	if keyA != keyB {
		t.Fatal("expected identical session keys to produce the same pool key")
	}
}

func TestPoolKeyIncludesProxyScope(t *testing.T) {
	manager := NewManager()
	t.Cleanup(manager.Stop)

	keyA := manager.poolKey(42, "wss://example.test/responses", "session-a", "http://proxy-a")
	keyB := manager.poolKey(42, "wss://example.test/responses", "session-a", "http://proxy-b")
	if keyA == keyB {
		t.Fatal("expected different proxies to produce different pool keys")
	}
}

func TestCanReuseConnection(t *testing.T) {
	manager := NewManager()
	t.Cleanup(manager.Stop)

	t.Run("idle connected session can be reused", func(t *testing.T) {
		session := NewSession(42, manager)
		session.SetConnected(true)
		conn := &WsConnection{session: session}
		conn.SetState(StateConnected)
		conn.Touch()

		if !canReuseConnection(conn) {
			t.Fatal("expected connection to be reusable")
		}
	})

	t.Run("pending request blocks reuse", func(t *testing.T) {
		session := NewSession(42, manager)
		session.SetConnected(true)
		pending := session.AddPendingRequest("session-a")
		t.Cleanup(func() { session.RemovePendingRequest(pending.RequestID) })

		conn := &WsConnection{session: session}
		conn.SetState(StateConnected)
		conn.Touch()

		if canReuseConnection(conn) {
			t.Fatal("expected connection with pending request to be non-reusable")
		}
	})

	t.Run("expired connection cannot be reused", func(t *testing.T) {
		session := NewSession(42, manager)
		session.SetConnected(true)
		conn := &WsConnection{session: session}
		conn.SetState(StateConnected)
		conn.lastUsed.Store(time.Now().Add(-IdleTimeout - time.Second).UnixNano())

		if canReuseConnection(conn) {
			t.Fatal("expected expired connection to be non-reusable")
		}
	})
}

func TestAcquireConnectionWaitsWhileSessionHasPendingRequest(t *testing.T) {
	manager := NewManager()
	t.Cleanup(manager.Stop)
	manager.probeFunc = func(wc *WsConnection) bool { return true }

	account := &auth.Account{DBID: 42}
	wsURL := "wss://example.test/responses"
	key := manager.poolKey(account.ID(), wsURL, "session-1", "")

	session := NewSession(account.ID(), manager)
	session.SetConnected(true)
	blocking := session.AddPendingRequest("session-1")
	t.Cleanup(func() { session.RemovePendingRequest(blocking.RequestID) })

	conn := &WsConnection{
		session:  session,
		URL:      wsURL,
		PoolKey:  key,
		httpResp: &http.Response{StatusCode: http.StatusSwitchingProtocols},
	}
	conn.SetState(StateConnected)
	conn.Touch()
	manager.connections.Store(key, conn)
	manager.sessions.Store(key, session)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, _, err := manager.AcquireConnection(ctx, account, wsURL, "session-1", http.Header{}, "")
	if err == nil {
		t.Fatal("expected acquire to stop when session stays busy until context timeout")
	}
}

func TestCreateConnectionIgnoresEnvironmentProxyWhenProxyURLBlank(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:51081")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:51081")
	t.Setenv("http_proxy", "http://127.0.0.1:51081")
	t.Setenv("https_proxy", "http://127.0.0.1:51081")

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	manager := NewManager()
	t.Cleanup(manager.Stop)
	account := &auth.Account{DBID: 7}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	conn, err := manager.createConnection(context.Background(), account, wsURL, "session-env", http.Header{}, "")
	if err != nil {
		t.Fatalf("createConnection() error = %v", err)
	}
	_ = conn.Close()
}

func TestCreateConnectionUsesExplicitHTTPProxy(t *testing.T) {
	var gotProxyConnect atomic.Bool
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	defer proxyListener.Close()
	go func() {
		conn, err := proxyListener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		gotProxyConnect.Store(true)
		reader := bufio.NewReader(conn)
		req, err := http.ReadRequest(reader)
		if err != nil {
			t.Errorf("read proxy request: %v", err)
			return
		}
		if req.Method == http.MethodConnect {
			_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
			req, err = http.ReadRequest(reader)
			if err != nil {
				t.Errorf("read tunneled websocket request: %v", err)
				return
			}
		}
		if req.Header.Get("Upgrade") != "websocket" {
			t.Errorf("Upgrade = %q, want websocket", req.Header.Get("Upgrade"))
			return
		}
		key := req.Header.Get("Sec-Websocket-Key")
		if key == "" {
			key = req.Header.Get("Sec-WebSocket-Key")
		}
		if key == "" {
			t.Errorf("missing Sec-WebSocket-Key")
			return
		}
		sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		accept := base64.StdEncoding.EncodeToString(sum[:])
		_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"))
	}()

	manager := NewManager()
	t.Cleanup(manager.Stop)
	account := &auth.Account{DBID: 7}
	wsURL := "ws://example.test/responses"

	conn, err := manager.createConnection(context.Background(), account, wsURL, "session-proxy", http.Header{}, "http://"+proxyListener.Addr().String())
	if err != nil {
		t.Fatalf("createConnection() error = %v", err)
	}
	_ = conn.Close()
	if !gotProxyConnect.Load() {
		t.Fatal("expected websocket handshake to go through explicit proxy")
	}
}
