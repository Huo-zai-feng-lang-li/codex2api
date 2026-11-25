//go:build windows

package admin

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// tunnelMetricsAddr 是本服务拉起的 cloudflared 独占的 metrics 地址。
	// 与默认 20242 错开，避免与机器上其他 cloudflared 实例冲突，并作为"是否是我这一条隧道"的探针。
	tunnelMetricsAddr = "127.0.0.1:20260"
	// tunnelStartupVBSName 是投放到启动目录的文件名。
	tunnelStartupVBSName = "Codex2apiTunnel.vbs"
	// tunnelGuardInterval 是守护协程的巡检周期。
	tunnelGuardInterval = 15 * time.Second
	// tunnelProbeTimeout 是单次就绪探测的超时。
	tunnelProbeTimeout = 700 * time.Millisecond
)

var (
	tunnelGuardOnce sync.Once
)

// tunnelDataDir 返回隧道运行态目录：<exe 目录>/data/tunnel。
func tunnelDataDir() string {
	exe, err := os.Executable()
	if err != nil {
		return filepath.Join("data", "tunnel")
	}
	return filepath.Join(filepath.Dir(exe), "data", "tunnel")
}

func tunnelTokenPath() string { return filepath.Join(tunnelDataDir(), "tunnel-token.txt") }
func tunnelFlagPath() string  { return filepath.Join(tunnelDataDir(), "autostart.enabled") }
func tunnelLogPath() string   { return filepath.Join(tunnelDataDir(), "cloudflared.log") }

// tunnelStartupDir 返回当前用户的启动目录（登录时自动执行）。
func tunnelStartupDir() string {
	if appData := os.Getenv("APPDATA"); appData != "" {
		return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
}

func tunnelStartupPath() string { return filepath.Join(tunnelStartupDir(), tunnelStartupVBSName) }

// tunnelAutostartSupported 报告当前平台是否支持自启管理。
func tunnelAutostartSupported() bool { return true }

// tunnelToken 按优先级取隧道 token：环境变量 > data/tunnel/tunnel-token.txt。
func tunnelToken() (string, error) {
	if v := strings.TrimSpace(os.Getenv("CODEX_TUNNEL_TOKEN")); v != "" {
		return v, nil
	}
	raw, err := os.ReadFile(tunnelTokenPath())
	if err != nil {
		return "", fmt.Errorf("未找到隧道 token，请把 tunnel token 写入 %s 或设置 CODEX_TUNNEL_TOKEN", tunnelTokenPath())
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("隧道 token 文件为空")
	}
	return token, nil
}

// cloudflaredBinary 按优先级定位 cloudflared.exe：环境变量 > PATH > 常见安装位置。
func cloudflaredBinary() (string, error) {
	if v := strings.TrimSpace(os.Getenv("CODEX_CLOUDFLARED_PATH")); v != "" {
		if _, err := os.Stat(v); err == nil {
			return v, nil
		}
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p, nil
	}
	candidates := []string{`D:\cloudflared\cloudflared.exe`}
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(base, "cloudflared.exe"),
			filepath.Join(base, "..", "cloudflared", "cloudflared.exe"),
		)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", errors.New("未找到 cloudflared.exe，请设置 CODEX_CLOUDFLARED_PATH")
}

// tunnelAlive 判断本服务这条隧道的 cloudflared 是否在跑（TCP 拨测 metrics 端口，约 1ms）。
func tunnelAlive() bool {
	conn, err := net.DialTimeout("tcp", tunnelMetricsAddr, tunnelProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// tunnelReady 判断隧道是否已与 Cloudflare 边缘建立连接（metrics /ready 返回 200）。
func tunnelReady() bool {
	client := &http.Client{Timeout: tunnelProbeTimeout}
	resp, err := client.Get("http://" + tunnelMetricsAddr + "/ready")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// tunnelAutostartEnabled 通过"启动目录脚本 + 开关标记文件"共同判断自启是否开启。
func tunnelAutostartEnabled() bool {
	if _, err := os.Stat(tunnelFlagPath()); err != nil {
		return false
	}
	if _, err := os.Stat(tunnelStartupPath()); err != nil {
		return false
	}
	return true
}

// startTunnelProcess 以隐藏、脱控制台的方式拉起 cloudflared（token 模式）。
func startTunnelProcess() error {
	token, err := tunnelToken()
	if err != nil {
		return err
	}
	bin, err := cloudflaredBinary()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(tunnelDataDir(), 0o755); err != nil {
		return err
	}
	// 关键：不能让 cloudflared 成为本服务的子进程。
	// 热替换脚本用 `taskkill /T` 按进程树强杀旧 PID，子进程会被一起带走。
	// 这里借一个立即退出的 wscript 做父进程，cloudflared 变成孤儿进程后与服务的生命周期解耦。
	launchPath := filepath.Join(tunnelDataDir(), "launch.vbs")
	if err := os.WriteFile(launchPath, []byte(buildTunnelLaunchScript(bin, token)), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("wscript.exe", "//B", "//Nologo", launchPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	return cmd.Run()
}

// ensureTunnelRunning 保证隧道进程存在；如需等待就绪，由调用方决定轮询时长。
func ensureTunnelRunning() error {
	if tunnelAlive() {
		return nil
	}
	return startTunnelProcess()
}

// waitTunnelReady 轮询等待隧道就绪，超时返回 false（不视为错误，仅状态未就绪）。
func waitTunnelReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if tunnelReady() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return tunnelReady()
}

// startTunnelGuard 启动守护协程：自启开启时，隧道掉线 15 秒内自动拉起。
func startTunnelGuard() {
	tunnelGuardOnce.Do(func() {
		go func() {
			ensure := func() {
				if !tunnelAutostartEnabled() {
					return
				}
				if tunnelAlive() {
					return
				}
				if err := startTunnelProcess(); err != nil {
					log.Printf("隧道守护：重启 cloudflared 失败：%v", err)
					return
				}
				log.Printf("隧道守护：cloudflared 已重新拉起")
			}
			ensure()
			ticker := time.NewTicker(tunnelGuardInterval)
			defer ticker.Stop()
			for range ticker.C {
				ensure()
			}
		}()
	})
}

// setTunnelAutostart 开关开机自启：写入/删除启动目录脚本与标记文件。
// 开启时立即把隧道拉起来，确保当前这次访问就是热连接。
func setTunnelAutostart(enabled bool) error {
	dir := tunnelStartupDir()
	if dir == "" {
		return errors.New("无法定位启动目录")
	}
	if !enabled {
		_ = os.Remove(tunnelStartupPath())
		_ = os.Remove(tunnelFlagPath())
		return nil
	}
	token, err := tunnelToken()
	if err != nil {
		return err
	}
	bin, err := cloudflaredBinary()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(tunnelDataDir(), 0o755); err != nil {
		return err
	}
	script := buildTunnelLaunchScript(bin, token)
	if err := os.WriteFile(tunnelStartupPath(), []byte(script), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(tunnelFlagPath(), []byte(time.Now().Format(time.RFC3339)), 0o644); err != nil {
		return err
	}
	startTunnelGuard()
	return nil
}

// buildTunnelLaunchScript 生成隐藏启动脚本（VBS 的 Run 第二参数 0 表示无窗口）。
func buildTunnelLaunchScript(bin, token string) string {
	quote := func(s string) string { return strings.ReplaceAll(s, `"`, `""`) }
	return fmt.Sprintf(`' Codex2api 公网隧道开机自启（由管理台生成，删除本文件即关闭自启）
Set sh = CreateObject("WScript.Shell")
sh.Run "%s tunnel --metrics %s --logfile ""%s"" run --token %s", 0, False
`, quote(bin), tunnelMetricsAddr, quote(tunnelLogPath()), quote(token))
}

// tunnelState 汇总隧道状态，供管理台展示。
type tunnelState struct {
	Running   bool // 进程在跑
	Ready     bool // 已与边缘建立连接
	Autostart bool // 已开启开机自启
	Supported bool
}

func currentTunnelState() tunnelState {
	startTunnelGuard()
	return tunnelState{
		Running:   tunnelAlive(),
		Ready:     tunnelReady(),
		Autostart: tunnelAutostartEnabled(),
		Supported: tunnelAutostartSupported(),
	}
}
