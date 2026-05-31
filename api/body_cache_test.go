package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type countingReadCloser struct {
	reader *strings.Reader
	reads  int
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	c.reads++
	return c.reader.Read(p)
}

func (c *countingReadCloser) Close() error { return nil }

func TestBodyCacheMiddlewareReusesExistingRawBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := &countingReadCloser{reader: strings.NewReader("ignored")}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("raw_body", []byte("cached"))
		c.Request.Body = body
		c.Next()
	})
	r.Use(BodyCacheMiddleware())
	r.POST("/test", func(c *gin.Context) {
		raw := GetRawBody(c)
		if string(raw) != "cached" {
			t.Fatalf("raw body = %q, want cached", string(raw))
		}
		readBack, err := io.ReadAll(c.Request.Body)
		if err != nil {
			t.Fatalf("read restored body: %v", err)
		}
		if string(readBack) != "cached" {
			t.Fatalf("restored body = %q, want cached", string(readBack))
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader("ignored"))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if body.reads != 0 {
		t.Fatalf("BodyCacheMiddleware read request body %d times, want 0", body.reads)
	}
}
