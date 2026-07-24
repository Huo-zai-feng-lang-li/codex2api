//go:build !windows

package maintenance

import "golang.org/x/sys/unix"

// diskUsage returns disk statistics for the volume containing path.
func diskUsage(path string) (DiskInfo, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return DiskInfo{}, err
	}
	//nolint:unconvert
	total := uint64(stat.Blocks) * uint64(stat.Bsize)
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	used := total - free
	pct := 0.0
	if total > 0 {
		pct = float64(used) / float64(total) * 100
	}
	return DiskInfo{
		TotalBytes:   total,
		UsedBytes:    used,
		FreeBytes:    free,
		UsagePercent: pct,
	}, nil
}
