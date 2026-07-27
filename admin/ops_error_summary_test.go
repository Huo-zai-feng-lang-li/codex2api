package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func TestGetOpsErrorSummaryExposesAttemptAndRequestCounters(t *testing.T) {
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "codex2api.db"))
	if err != nil {
		t.Fatalf("database.New(sqlite) returned error: %v", err)
	}
	defer db.Close()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/admin/ops/errors/summary", nil)
	(&Handler{db: db}).GetOpsErrorSummary(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, field := range []string{"total_errors", "terminal_errors", "retry_errors", "retry_attempts"} {
		value, ok := payload[field]
		if !ok {
			t.Errorf("response missing %q: %s", field, recorder.Body.String())
			continue
		}
		if string(value) != "0" {
			t.Errorf("%s = %s, want 0", field, value)
		}
	}
}
