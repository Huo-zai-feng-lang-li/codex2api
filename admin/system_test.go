package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestShutdownSystemRequestsGracefulShutdown(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var reason string
	handler := &Handler{
		shutdownFunc: func(nextReason string) bool {
			reason = nextReason
			return true
		},
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/system/shutdown", nil)

	handler.ShutdownSystem(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.HasPrefix(reason, "admin_api:") {
		t.Fatalf("shutdown reason = %q, want admin_api prefix", reason)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["shutting"] != true {
		t.Fatalf("shutting = %v, want true", payload["shutting"])
	}
}

func TestShutdownSystemReportsDuplicateRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := &Handler{
		shutdownFunc: func(string) bool {
			return false
		},
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/system/shutdown", nil)

	handler.ShutdownSystem(ctx)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
}
