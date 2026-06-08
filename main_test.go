package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/admin"
	"github.com/gin-gonic/gin"
)

func TestLoggerMiddlewareRedactsSensitiveContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logs bytes.Buffer
	previousOutput := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})

	r := gin.New()
	r.Use(loggerMiddleware())
	r.GET("/probe", func(c *gin.Context) {
		c.Set("x-account-email", "alice@example.com")
		c.Set("x-account-proxy", "http://user:secret@proxy.example:8080")
		c.Set("x-model", "gpt-5.5")
		c.Set("x-reasoning-effort", "medium")
		c.Set("x-service-tier", "fast")
		c.Status(http.StatusAccepted)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}

	got := logs.String()
	for _, forbidden := range []string{"alice@example.com", "secret"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("log output leaked %q: %s", forbidden, got)
		}
	}
	for _, expected := range []string{"GET /probe 202", "gpt-5.5", "effort=medium", "fast"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("log output missing %q: %s", expected, got)
		}
	}
}

func TestNewHTTPServerSetsReadAndIdleTimeouts(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.NewServeMux())

	if server.ReadHeaderTimeout <= 0 {
		t.Fatalf("ReadHeaderTimeout = %v, want positive timeout", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout <= server.ReadHeaderTimeout {
		t.Fatalf("ReadTimeout = %v, want longer than ReadHeaderTimeout %v", server.ReadTimeout, server.ReadHeaderTimeout)
	}
	if server.IdleTimeout <= 0 {
		t.Fatalf("IdleTimeout = %v, want positive timeout", server.IdleTimeout)
	}
	if server.WriteTimeout != 0*time.Second {
		t.Fatalf("WriteTimeout = %v, want zero to preserve long streaming responses", server.WriteTimeout)
	}
}

func TestOAuthCallbackRouterShowsInfoForNonCallbackPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := newOAuthCallbackRouter(&admin.Handler{}, "http://127.0.0.1:18080/admin/")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	body := w.Body.String()
	for _, expected := range []string{"OAuth 回调端口", "http://127.0.0.1:18080/admin/"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %q: %s", expected, body)
		}
	}
}

func TestOAuthCallbackRouterKeepsCallbackEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := newOAuthCallbackRouter(&admin.Handler{}, "http://127.0.0.1:18080/admin/")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if !strings.Contains(w.Body.String(), "缺少 code 或 state 参数") {
		t.Fatalf("body missing callback validation message: %s", w.Body.String())
	}
}
