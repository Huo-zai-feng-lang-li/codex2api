package maintenance

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codex2api/database"
)

// StorageStatus summarises disk and managed-storage health.
type StorageStatus string

const (
	StatusNormal   StorageStatus = "normal"
	StatusWarning  StorageStatus = "warning"
	StatusCritical StorageStatus = "critical"
	StatusUnknown  StorageStatus = "unknown"

	diskWarnPercent     = 80.0
	diskCriticalPercent = 90.0
)

// StorageSnapshot is the immutable capacity sample cached by the Manager.
type StorageSnapshot struct {
	Status    StorageStatus `json:"status"`
	SampledAt time.Time     `json:"sampled_at"`
	Disk      DiskInfo      `json:"disk"`
	Managed   ManagedInfo   `json:"managed"`
	Error     string        `json:"error,omitempty"`
}

// DiskInfo contains raw disk statistics.
type DiskInfo struct {
	MountPoint   string  `json:"mount_point"`
	TotalBytes   uint64  `json:"total_bytes"`
	UsedBytes    uint64  `json:"used_bytes"`
	FreeBytes    uint64  `json:"free_bytes"`
	UsagePercent float64 `json:"usage_percent"`
}

// ManagedInfo breaks down managed data by category.
// ImagesBytes == -1 means remote storage (unknown).
type ManagedInfo struct {
	DatabaseBytes int64 `json:"database_bytes"`
	LogsBytes     int64 `json:"logs_bytes"`
	ImagesBytes   int64 `json:"images_bytes"`
	TotalBytes    int64 `json:"total_bytes"`
}

// RetentionConfig holds per-table retention windows.
type RetentionConfig struct {
	UsageLogs        time.Duration
	SecurityEvents   time.Duration
	PromptFilterLogs time.Duration
	AccountEvents    time.Duration
}

// Config is injected by main at startup.
type Config struct {
	DB        *database.DB
	DBPath    string // absolute SQLite path; empty for Postgres
	LogDir    string // LOG_DIR value
	ImagesDir string // local image dir; empty = remote (unknown)
	Retention RetentionConfig
}

// Manager orchestrates periodic retention pruning and capacity sampling.
type Manager struct {
	cfg      Config
	mu       sync.Mutex  // serialises prune to prevent re-entry
	snapshot atomic.Value // *StorageSnapshot
	once     sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// New creates a Manager; call Start() to activate.
func New(cfg Config) *Manager {
	m := &Manager{
		cfg:    cfg,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	m.snapshot.Store(&StorageSnapshot{Status: StatusUnknown, SampledAt: time.Now()})
	return m
}

// Start launches background tasks. Safe to call only once.
func (m *Manager) Start() {
	m.once.Do(func() { go m.run() })
}

// Stop cancels background tasks and waits for the goroutine to exit.
func (m *Manager) Stop() {
	close(m.stopCh)
	<-m.doneCh
}

// Snapshot returns the latest capacity snapshot without blocking.
func (m *Manager) Snapshot() *StorageSnapshot {
	if v := m.snapshot.Load(); v != nil {
		return v.(*StorageSnapshot)
	}
	return &StorageSnapshot{Status: StatusUnknown, SampledAt: time.Now()}
}

// ─── background loop ──────────────────────────────────────────────────────────

func (m *Manager) run() {
	defer close(m.doneCh)
	// Immediate first pass.
	m.doSample()
	m.doPrune()

	cleanTicker := time.NewTicker(time.Hour)
	sampleTicker := time.NewTicker(time.Minute)
	defer cleanTicker.Stop()
	defer sampleTicker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-cleanTicker.C:
			m.doPrune()
		case <-sampleTicker.C:
			m.doSample()
		}
	}
}

// doPrune executes one database retention sweep, serialised by m.mu.
func (m *Manager) doPrune() {
	if m.cfg.DB == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	policy := database.RetentionPolicy{
		UsageLogs:        m.cfg.Retention.UsageLogs,
		SecurityEvents:   m.cfg.Retention.SecurityEvents,
		PromptFilterLogs: m.cfg.Retention.PromptFilterLogs,
		AccountEvents:    m.cfg.Retention.AccountEvents,
	}
	result, err := m.cfg.DB.PruneOperationalDataBefore(ctx, policy, time.Now())
	if err != nil {
		log.Printf("[maintenance] 保留清理失败: %v", err)
		return
	}
	if result.UsageLogs > 0 || result.SecurityEvents > 0 ||
		result.PromptFilterLogs > 0 || result.AccountEvents > 0 {
		log.Printf("[maintenance] 清理完成: usage_logs=%d security_events=%d prompt_filter_logs=%d account_events=%d",
			result.UsageLogs, result.SecurityEvents, result.PromptFilterLogs, result.AccountEvents)
	}
}

// doSample collects capacity metrics and stores the snapshot atomically.
func (m *Manager) doSample() {
	m.snapshot.Store(m.collectSnapshot())
}

func (m *Manager) collectSnapshot() *StorageSnapshot {
	disk, diskErr := m.collectDisk()
	managed, managedErr := m.collectManaged()

	errParts := []string{}
	if diskErr != nil {
		errParts = append(errParts, diskErr.Error())
	}
	if managedErr != nil {
		errParts = append(errParts, managedErr.Error())
	}

	status := computeStatus(disk, diskErr)
	return &StorageSnapshot{
		Status:    status,
		SampledAt: time.Now(),
		Disk:      disk,
		Managed:   managed,
		Error:     strings.Join(errParts, "; "),
	}
}

// StatusFromPercent maps a disk usage percentage to a StorageStatus.
// Exported for testing boundary conditions.
func StatusFromPercent(pct float64) StorageStatus {
	switch {
	case pct >= diskCriticalPercent:
		return StatusCritical
	case pct >= diskWarnPercent:
		return StatusWarning
	default:
		return StatusNormal
	}
}

func computeStatus(disk DiskInfo, diskErr error) StorageStatus {
	if diskErr != nil {
		return StatusUnknown
	}
	return StatusFromPercent(disk.UsagePercent)
}

func (m *Manager) collectDisk() (DiskInfo, error) {
	target := m.cfg.DBPath
	if target == "" {
		target = m.cfg.LogDir
	}
	if target == "" {
		target = "."
	}
	if abs, err := filepath.Abs(target); err == nil {
		target = abs
	}
	// Use the directory, not the file itself.
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		target = filepath.Dir(target)
	}
	disk, err := diskUsage(target)
	mount := filepath.VolumeName(target)
	if mount == "" {
		mount = "/"
	}
	disk.MountPoint = mount
	return disk, err
}

func (m *Manager) collectManaged() (ManagedInfo, error) {
	var info ManagedInfo
	var errs []string

	if m.cfg.DBPath != "" {
		dbBytes, err := sqliteFileGroupSize(m.cfg.DBPath)
		if err != nil {
			errs = append(errs, "db: "+err.Error())
		}
		info.DatabaseBytes = dbBytes
	}

	if m.cfg.LogDir != "" {
		logsBytes, err := dirSize(m.cfg.LogDir)
		if err != nil {
			errs = append(errs, "logs: "+err.Error())
		}
		info.LogsBytes = logsBytes
	}

	if m.cfg.ImagesDir != "" {
		imgBytes, err := dirSize(m.cfg.ImagesDir)
		if err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, "images: "+err.Error())
			}
			// directory not yet created → treat as 0
		} else {
			info.ImagesBytes = imgBytes
		}
	} else {
		info.ImagesBytes = -1 // remote storage: unknown
	}

	info.TotalBytes = info.DatabaseBytes + info.LogsBytes + max64(info.ImagesBytes, 0)

	if len(errs) > 0 {
		return info, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return info, nil
}

// ─── filesystem helpers ───────────────────────────────────────────────────────

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		if !d.IsDir() {
			if fi, e := d.Info(); e == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total, err
}

func sqliteFileGroupSize(path string) (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if fi, err := os.Stat(path + suffix); err == nil {
			total += fi.Size()
		}
	}
	if total == 0 {
		if _, err := os.Stat(path); err != nil {
			return 0, err
		}
	}
	return total, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ─── env-var defaults ─────────────────────────────────────────────────────────

func parseDurationDays(envKey string, defaultDays int) time.Duration {
	v := strings.TrimSpace(os.Getenv(envKey))
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return time.Duration(defaultDays) * 24 * time.Hour
	}
	return time.Duration(n) * 24 * time.Hour
}

// DefaultRetentionConfig reads env vars; securityEventDays comes from system settings.
func DefaultRetentionConfig(securityEventDays int) RetentionConfig {
	if securityEventDays <= 0 {
		securityEventDays = 30
	}
	return RetentionConfig{
		UsageLogs:        parseDurationDays("RETENTION_USAGE_LOG_DAYS", 30),
		SecurityEvents:   time.Duration(securityEventDays) * 24 * time.Hour,
		PromptFilterLogs: parseDurationDays("RETENTION_PROMPT_LOG_DAYS", 30),
		AccountEvents:    parseDurationDays("RETENTION_ACCOUNT_EVENT_DAYS", 90),
	}
}
