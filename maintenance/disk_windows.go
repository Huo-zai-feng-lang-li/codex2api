//go:build windows

package maintenance

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// diskUsage returns disk statistics for the volume containing path on Windows.
func diskUsage(path string) (DiskInfo, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return DiskInfo{}, err
	}
	var free, total, totalFree uint64
	err = windows.GetDiskFreeSpaceEx(
		pathPtr,
		(*uint64)(unsafe.Pointer(&free)),
		(*uint64)(unsafe.Pointer(&total)),
		(*uint64)(unsafe.Pointer(&totalFree)),
	)
	if err != nil {
		return DiskInfo{}, err
	}
	used := total - totalFree
	pct := 0.0
	if total > 0 {
		pct = float64(used) / float64(total) * 100
	}
	return DiskInfo{
		TotalBytes:   total,
		UsedBytes:    used,
		FreeBytes:    totalFree,
		UsagePercent: pct,
	}, nil
}
