package proxy

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/codex2api/database"
)

const (
	securityCaptureCleanupInterval       = time.Hour
	securityCaptureCleanupTimeout        = 10 * time.Second
	securityCaptureMaxStorageBytes int64 = 2 * 1024 * 1024 * 1024
)

func StartSecurityCaptureCleanup(db *database.DB) func() {
	if db == nil {
		return func() {}
	}
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		runSecurityCaptureCleanup(db)
		ticker := time.NewTicker(securityCaptureCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				runSecurityCaptureCleanup(db)
			case <-stop:
				return
			}
		}
	}()
	return func() {
		once.Do(func() {
			close(stop)
		})
	}
}

func runSecurityCaptureCleanup(db *database.DB) {
	removedExpired, removedOverLimit, err := runSecurityCaptureCleanupWithLimit(db, time.Now(), securityCaptureMaxStorageBytes)
	if err != nil {
		log.Printf("原文审计清理失败: %v", err)
		return
	}
	if removedExpired > 0 || removedOverLimit > 0 {
		log.Printf("原文审计清理完成: expired=%d over_limit=%d", removedExpired, removedOverLimit)
	}
}

func runSecurityCaptureCleanupWithLimit(db *database.DB, now time.Time, maxStorageBytes int64) (int64, int64, error) {
	if db == nil {
		return 0, 0, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), securityCaptureCleanupTimeout)
	defer cancel()

	removedExpired, err := db.PruneSecurityCapturesBefore(ctx, now)
	if err != nil {
		return 0, 0, err
	}
	removedOverLimit, err := db.PruneSecurityCapturesToMaxBytes(ctx, maxStorageBytes)
	if err != nil {
		return removedExpired, 0, err
	}
	return removedExpired, removedOverLimit, nil
}
