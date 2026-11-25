//go:build !windows

package admin

import (
	"errors"
	"time"
)

// tunnelState 汇总隧道状态，非 Windows 平台只做只读探测。
type tunnelState struct {
	Running   bool
	Ready     bool
	Autostart bool
	Paused    bool
	Supported bool
}

// tunnelAutostartSupported 非 Windows 平台不托管自启。
func tunnelAutostartSupported() bool { return false }

func tunnelAlive() bool { return false }
func tunnelReady() bool { return false }

func tunnelAutostartEnabled() bool { return false }

func tunnelPaused() bool { return false }

// setTunnelPaused 在非 Windows 平台直接拒绝。
func setTunnelPaused(paused bool) error {
	return errors.New("隧道进程托管目前仅支持 Windows")
}

// stopTunnelProcess 在非 Windows 平台直接拒绝。
func stopTunnelProcess() error {
	return errors.New("隧道进程托管目前仅支持 Windows")
}

// setTunnelAutostart 在非 Windows 平台直接拒绝。
func setTunnelAutostart(enabled bool) error {
	return errors.New("开机自启托管目前仅支持 Windows")
}

// ensureTunnelRunning 在非 Windows 平台不做进程托管。
func ensureTunnelRunning() error {
	return errors.New("隧道进程托管目前仅支持 Windows")
}

func waitTunnelReady(timeout time.Duration) bool { return false }

func startTunnelGuard() {}

func currentTunnelState() tunnelState {
	return tunnelState{Supported: false}
}
