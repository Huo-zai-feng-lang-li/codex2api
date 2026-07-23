package proxy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestErrorLogDirUsesEnv(t *testing.T) {
	t.Setenv("LOG_DIR", "/tmp/codex2api-logs")
	if got, want := errorLogDir(), "/tmp/codex2api-logs"; got != want {
		t.Fatalf("errorLogDir() = %q, want %q", got, want)
	}
}

func TestFileLoggerRotationUsesDefaultRetention(t *testing.T) {
	const defaultMaxSize = int64(50 << 20)

	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)
	t.Setenv("ERROR_LOG_MAX_MB", "")
	t.Setenv("ERROR_LOG_MAX_BACKUPS", "")

	logPath := filepath.Join(dir, "rotation.log")
	seedLogFile(t, logPath, defaultMaxSize-1, "current-default")
	for i := 1; i <= 5; i++ {
		seedLogFile(t, fmt.Sprintf("%s.%d", logPath, i), 0, fmt.Sprintf("backup-%d", i))
	}

	logger := &fileLogger{path: filepath.Base(logPath)}
	logger.writeEntry("/v1/responses", 500, "gpt-test", 1, []byte("fresh-default"))
	logger.close()

	assertLogMarker(t, logPath, "fresh-default")
	assertLogMarker(t, logPath+".1", "current-default")
	for i := 2; i <= 5; i++ {
		assertLogMarker(t, fmt.Sprintf("%s.%d", logPath, i), fmt.Sprintf("backup-%d", i-1))
	}
	assertLogMarkerAbsent(t, logPath+".5", "backup-5")
}

func TestFileLoggerRotationUsesEnvironmentOverrides(t *testing.T) {
	const overriddenMaxSize = int64(1 << 20)

	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)
	t.Setenv("ERROR_LOG_MAX_MB", "1")
	t.Setenv("ERROR_LOG_MAX_BACKUPS", "2")

	logPath := filepath.Join(dir, "rotation.log")
	seedLogFile(t, logPath, overriddenMaxSize-1, "current-override")
	seedLogFile(t, logPath+".1", 0, "backup-1")
	seedLogFile(t, logPath+".2", 0, "backup-2")

	logger := &fileLogger{path: filepath.Base(logPath)}
	logger.writeEntry("/v1/responses", 500, "gpt-test", 2, []byte("fresh-override"))
	logger.close()

	assertLogMarker(t, logPath, "fresh-override")
	assertLogMarker(t, logPath+".1", "current-override")
	assertLogMarker(t, logPath+".2", "backup-1")
	assertLogMarkerAbsent(t, logPath+".2", "backup-2")
	if _, err := os.Stat(logPath + ".3"); !os.IsNotExist(err) {
		t.Fatalf("unexpected backup beyond configured retention: %v", err)
	}
}

func TestFileLoggerRotationPreservesSanitizationAndTruncation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)

	logger := &fileLogger{path: "sanitized.log"}
	body := "api_key=super-secret-value\n" + strings.Repeat("界", 6000)
	logger.writeEntry("/v1/responses?access_token=endpoint-secret", 500, "Bearer model-secret", 3, []byte(body))
	logger.close()

	content := readLogFile(t, filepath.Join(dir, "sanitized.log"))
	for _, secret := range []string{"super-secret-value", "endpoint-secret", "model-secret"} {
		if strings.Contains(content, secret) {
			t.Fatalf("log contains sensitive value %q", secret)
		}
	}
	if !strings.Contains(content, "****MASKED****") {
		t.Fatal("log does not contain the masking marker")
	}

	response := responseBody(t, content)
	if got, want := utf8.RuneCountInString(response), 5000; got != want {
		t.Fatalf("logged response length = %d runes, want %d", got, want)
	}
}

func TestFileLoggerRotationSerializesConcurrentWrites(t *testing.T) {
	const writers = 300

	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)
	t.Setenv("ERROR_LOG_MAX_MB", "1")
	t.Setenv("ERROR_LOG_MAX_BACKUPS", "2")

	logPath := filepath.Join(dir, "concurrent.log")
	seedLogFile(t, logPath, 1<<20-1, "seed")
	logger := &fileLogger{path: filepath.Base(logPath)}

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			marker := fmt.Sprintf("concurrent-%03d", id)
			logger.writeEntry("/v1/responses", 500, "gpt-test", int64(id), []byte(marker+strings.Repeat("x", 6000)))
		}(i)
	}
	wg.Wait()
	logger.close()

	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Fatalf("expected rotation during concurrent writes: %v", err)
	}
	content := readExistingLogs(t, logPath, 2)
	for i := 0; i < writers; i++ {
		marker := fmt.Sprintf("concurrent-%03d", i)
		if count := strings.Count(content, marker); count != 1 {
			t.Fatalf("marker %q count = %d, want 1", marker, count)
		}
	}
}

func seedLogFile(t *testing.T, path string, size int64, marker string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(marker), 0o644); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	if size > int64(len(marker)) {
		if err := os.Truncate(path, size); err != nil {
			t.Fatalf("truncate %s: %v", path, err)
		}
	}
}

func assertLogMarker(t *testing.T, path, marker string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()

	prefix := make([]byte, 4096)
	n, err := file.Read(prefix)
	if err != nil && err != io.EOF {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(prefix[:n]), marker) {
		t.Fatalf("%s does not contain marker %q", path, marker)
	}
}

func assertLogMarkerAbsent(t *testing.T, path, marker string) {
	t.Helper()
	content := readLogFile(t, path)
	if strings.Contains(content, marker) {
		t.Fatalf("%s still contains deleted marker %q", path, marker)
	}
}

func readLogFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func readExistingLogs(t *testing.T, logPath string, maxBackups int) string {
	t.Helper()
	var content strings.Builder
	for i := 0; i <= maxBackups; i++ {
		path := logPath
		if i > 0 {
			path = fmt.Sprintf("%s.%d", logPath, i)
		}
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content.Write(data)
	}
	return content.String()
}

func responseBody(t *testing.T, content string) string {
	t.Helper()
	const prefix = "Response:\n"
	start := strings.Index(content, prefix)
	if start < 0 {
		t.Fatal("log response section not found")
	}
	return strings.TrimSuffix(content[start+len(prefix):], "\n")
}
