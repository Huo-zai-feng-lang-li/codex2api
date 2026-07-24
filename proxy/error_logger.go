package proxy

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/security"
)

// fileLogger 单个日志文件实例
type fileLogger struct {
	once       sync.Once
	mu         sync.Mutex
	logger     *log.Logger
	file       *os.File
	path       string
	maxSize    int64
	maxBackups int
}

var (
	badRequestLogger  = &fileLogger{path: "bad_request.log"}  // 400 错误
	serverErrorLogger = &fileLogger{path: "server_error.log"} // 5xx 错误
)

const (
	defaultLogDir          = "logs"
	defaultErrorLogMaxSize = int64(50 << 20)
	defaultErrorLogBackups = 5
	bytesPerMiB            = int64(1 << 20)
	maxErrorLogSizeMiB     = (1<<63 - 1) / bytesPerMiB
)

// ErrorLogDir returns the directory used to store error log files.
// It reads LOG_DIR env var and falls back to the default "logs" directory.
func ErrorLogDir() string {
	if dir := strings.TrimSpace(os.Getenv("LOG_DIR")); dir != "" {
		return dir
	}
	return defaultLogDir
}

func errorLogDir() string { return ErrorLogDir() }

func errorLogMaxSize() int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("ERROR_LOG_MAX_MB")), 10, 64)
	if err != nil || value <= 0 || value > maxErrorLogSizeMiB {
		return defaultErrorLogMaxSize
	}
	return value * bytesPerMiB
}

func errorLogMaxBackups() int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ERROR_LOG_MAX_BACKUPS")))
	if err != nil || value <= 0 {
		return defaultErrorLogBackups
	}
	return value
}

func (fl *fileLogger) init() *log.Logger {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	return fl.initLocked()
}

func (fl *fileLogger) initLocked() *log.Logger {
	if security.FileLogsDisabled() {
		return nil
	}
	fl.once.Do(func() {
		if fl.maxSize <= 0 {
			fl.maxSize = errorLogMaxSize()
		}
		if fl.maxBackups <= 0 {
			fl.maxBackups = errorLogMaxBackups()
		}
		if err := fl.openLocked(); err != nil {
			log.Printf("打开日志文件 %s 失败: %v", fl.path, err)
		}
	})
	return fl.logger
}

func (fl *fileLogger) close() {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if err := fl.closeLocked(); err != nil {
		fmt.Fprintf(os.Stderr, "关闭日志文件 %s 失败: %v\n", fl.path, err)
	}
}

// writeEntry 写一条错误日志（自动脱敏敏感信息）
func (fl *fileLogger) writeEntry(endpoint string, statusCode int, model string, accountID int64, body []byte) {
	// 脱敏日志内容
	safeEndpoint := security.SanitizeLog(endpoint)
	safeModel := security.SanitizeLog(model)
	bodyStr := string(body)

	// 检查并脱敏响应体中的敏感信息
	bodyStr = security.MaskSensitiveData(bodyStr)
	bodyStr = security.SafeTruncate(bodyStr, 5000) // 限制日志大小

	ts := time.Now().Format("2006/01/02 15:04:05")
	entry := fmt.Sprintf("========== %s ==========\nEndpoint: %s\nStatus: %d\nModel: %s\nAccount: %d\nResponse:\n%s\n",
		ts, safeEndpoint, statusCode, safeModel, accountID, bodyStr)

	fl.mu.Lock()
	defer fl.mu.Unlock()
	if fl.initLocked() == nil {
		return
	}
	if err := fl.rotateBeforeWriteLocked(int64(len(entry))); err != nil {
		log.Printf("轮转日志文件 %s 失败: %v", fl.path, err)
	}
	if fl.logger != nil {
		fl.logger.Print(entry)
	}
}

func (fl *fileLogger) openLocked() error {
	dir := errorLogDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建日志目录: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, fl.path), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	fl.file = f
	fl.logger = log.New(f, "", 0)
	return nil
}

func (fl *fileLogger) closeLocked() error {
	if fl.file == nil {
		return nil
	}
	err := fl.file.Close()
	fl.file = nil
	fl.logger = nil
	return err
}

func (fl *fileLogger) rotateBeforeWriteLocked(incomingSize int64) error {
	info, err := fl.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 || incomingSize <= fl.maxSize-info.Size() {
		return nil
	}
	return fl.rotateLocked()
}

func (fl *fileLogger) rotateLocked() error {
	logPath := filepath.Join(errorLogDir(), fl.path)
	if err := fl.closeLocked(); err != nil {
		return fl.reopenLocked(fmt.Errorf("关闭当前日志: %w", err))
	}

	oldest := fmt.Sprintf("%s.%d", logPath, fl.maxBackups)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return fl.reopenLocked(fmt.Errorf("删除最老备份: %w", err))
	}
	for i := fl.maxBackups - 1; i >= 1; i-- {
		source := fmt.Sprintf("%s.%d", logPath, i)
		target := fmt.Sprintf("%s.%d", logPath, i+1)
		if err := os.Rename(source, target); err != nil && !os.IsNotExist(err) {
			return fl.reopenLocked(fmt.Errorf("移动备份 %d: %w", i, err))
		}
	}
	if err := os.Rename(logPath, logPath+".1"); err != nil {
		return fl.reopenLocked(fmt.Errorf("保存当前日志: %w", err))
	}
	return fl.openLocked()
}

func (fl *fileLogger) reopenLocked(cause error) error {
	if err := fl.openLocked(); err != nil {
		return fmt.Errorf("%v；重新打开日志失败: %w", cause, err)
	}
	return cause
}

// logUpstreamError 根据状态码分发到对应日志文件
func logUpstreamError(endpoint string, statusCode int, model string, accountID int64, body []byte) {
	switch {
	case statusCode == 400:
		badRequestLogger.writeEntry(endpoint, statusCode, model, accountID, body)
	case statusCode >= 500:
		serverErrorLogger.writeEntry(endpoint, statusCode, model, accountID, body)
	}
}

// CloseErrorLogger 关闭所有错误日志文件（程序退出时调用）
func CloseErrorLogger() {
	badRequestLogger.close()
	serverErrorLogger.close()
}
