//go:build windows

package admin

import (
	"strings"
	"testing"
)

// TestBuildTunnelLaunchScriptParamOrder 锁死参数顺序：
// --metrics/--logfile 是 tunnel 级参数必须写在 run 之前，--token 是 run 的子命令参数。
// 顺序写错时 cloudflared 会以 exit 0 静默退出（只往 stdout 打 usage），极难排查。
func TestBuildTunnelLaunchScriptParamOrder(t *testing.T) {
	script := buildTunnelLaunchScript(`D:\cloudflared\cloudflared.exe`, "tok123")

	if !strings.Contains(script, `tunnel --metrics 127.0.0.1:20260`) {
		t.Fatalf("metrics 必须紧跟 tunnel 子命令，实际脚本：\n%s", script)
	}
	runIdx := strings.Index(script, " run ")
	metricsIdx := strings.Index(script, "--metrics")
	logfileIdx := strings.Index(script, "--logfile")
	closeIdx := strings.Index(script, `""`)
	if runIdx < 0 || metricsIdx < 0 || logfileIdx < 0 || closeIdx < 0 {
		t.Fatalf("脚本缺少必要参数：\n%s", script)
	}
	if !(metricsIdx < runIdx && logfileIdx < runIdx) {
		t.Fatalf("--metrics/--logfile 必须位于 run 之前：\n%s", script)
	}
	if !strings.Contains(script, `run --token tok123`) {
		t.Fatalf("--token 必须位于 run 之后：\n%s", script)
	}
}

// TestTunnelPausedFlagRoundTrip 锁死“停止标记”的写入/清除语义：
// 标记存在时守护必须放弃自动拉起，清除后恢复。
func TestTunnelPausedFlagRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if tunnelPausedAt(dir) {
		t.Fatal("空目录不应被判定为已暂停")
	}
	if err := setTunnelPausedAt(dir, true); err != nil {
		t.Fatalf("写标记失败：%v", err)
	}
	if !tunnelPausedAt(dir) {
		t.Fatal("写入标记后应判定为已暂停")
	}
	if err := setTunnelPausedAt(dir, false); err != nil {
		t.Fatalf("清标记失败：%v", err)
	}
	if tunnelPausedAt(dir) {
		t.Fatal("清除标记后不应仍判定为已暂停")
	}
}

// TestBuildTunnelLaunchScriptRespectsStopFlag 登录自启必须尊重“手动停止”标记，
// 否则用户停了隧道，一重启又被 VBS 拉起来。
func TestBuildTunnelLaunchScriptRespectsStopFlag(t *testing.T) {
	script := buildTunnelLaunchScript(`D:\cloudflared\cloudflared.exe`, "tok")
	if !strings.Contains(script, "FileExists") || !strings.Contains(script, "stopped.flag") {
		t.Fatalf("启动脚本缺少停止标记检查：\n%s", script)
	}
	if !strings.Contains(script, "WScript.Quit") {
		t.Fatalf("标记存在时必须退出而不是继续启动：\n%s", script)
	}
}

// TestBuildTunnelLaunchScriptHiddenWindow 启动脚本必须以隐藏窗口方式运行，否则每次登录都会弹黑框。
func TestBuildTunnelLaunchScriptHiddenWindow(t *testing.T) {
	script := buildTunnelLaunchScript(`D:\cloudflared\cloudflared.exe`, "tok")
	if !strings.Contains(script, ", 0, False") {
		t.Fatalf("VBS Run 必须使用 windowStyle=0 且不等待：\n%s", script)
	}
	if !strings.Contains(script, `WScript.Shell`) {
		t.Fatalf("脚本未使用 WScript.Shell：\n%s", script)
	}
}
