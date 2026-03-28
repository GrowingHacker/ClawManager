package wsl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall" // 引入 syscall 用于隐藏 Windows 下的 cmd 黑框

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// OpenClawChecker 定义 OpenClaw 检查接口，避免循环导入
type OpenClawChecker interface {
	CheckOpenClaw() OpenClawStatus
	RestartGateway() map[string]interface{}
}

// OpenClawStatus 定义 OpenClaw 状态结构
type OpenClawStatus struct {
	Installed      bool   `json:"installed"`
	Version        string `json:"version"`
	GatewayRunning bool   `json:"gatewayRunning"`
	Error          string `json:"error"`
}

// Manager 管理 WSL 逻辑
type Manager struct {
	ctx         context.Context
	openclawMgr OpenClawChecker
}

// NewManager 创建一个新的 Manager
func NewManager() *Manager {
	return &Manager{}
}

// SetContext 设置 Wails 上下文
func (m *Manager) SetContext(ctx context.Context) {
	m.ctx = ctx
}

// SetOpenClawManager 注入 OpenClaw 管理器
func (m *Manager) SetOpenClawManager(checker OpenClawChecker) {
	m.openclawMgr = checker
}

// WSLInfo 包含 WSL 运行状态信息
type WSLInfo struct {
	Installed       bool   `json:"installed"`
	DistroInstalled bool   `json:"distroInstalled"`
	Version         string `json:"version"`
	Error           string `json:"error"`
}

// CheckWSL 检查 WSL2 是否安装并返回版本信息
func (m *Manager) CheckWSL() WSLInfo {
	cmd := exec.Command("wsl", "--status")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()

	installed := true
	if err != nil {
		cmd2 := exec.Command("wsl", "--version")
		cmd2.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_, err = cmd2.CombinedOutput()
		if err != nil {
			installed = false
		}
	}

	distroInstalled := false
	if installed {
		// 检查是否已安装 Ubuntu
		cmd3 := exec.Command("wsl", "--list", "--quiet")
		cmd3.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out3, _ := cmd3.CombinedOutput()

		// 处理 UTF-16 可能带来的空字节问题
		cleanOut := strings.ReplaceAll(string(out3), "\x00", "")
		if strings.Contains(strings.ToLower(cleanOut), "ubuntu") {
			distroInstalled = true
		}
	}

	statusMsg := "系统已安装并启用 WSL"
	if !installed {
		statusMsg = "WSL 未安装或未启用"
	} else if !distroInstalled {
		statusMsg = "WSL 已安装，但尚未安装 Ubuntu 发行版"
	}

	_ = out
	return WSLInfo{
		Installed:       installed,
		DistroInstalled: distroInstalled,
		Version:         statusMsg,
	}
}

// CheckWSLRunning 如果当前有活跃的 WSL 进程则返回 true
func (m *Manager) CheckWSLRunning() bool {
	cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq wsl.exe", "/NH")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "wsl.exe")
}

// StopWSL 关闭所有正在运行的 WSL 实例
func (m *Manager) StopWSL() map[string]interface{} {
	cmd := exec.Command("wsl", "--shutdown")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	err := cmd.Run()
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	return map[string]interface{}{"ok": true, "message": "WSL 2 已关闭"}
}

// RunWSL 打开一个新的 WSL 终端窗口
func (m *Manager) RunWSL() map[string]interface{} {
	cmd := exec.Command("wsl", "sleep", "infinity")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	err := cmd.Start()
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}

	result := map[string]interface{}{"ok": true, "message": "WSL 2 已在后台启动"}

	if m.openclawMgr != nil {
		combinedCmd := `command -v openclaw >/dev/null 2>&1 && /usr/local/bin/openclaw gateway restart 2>&1 || true`
		checkCmd := exec.Command("wsl", "-e", "bash", "-c", combinedCmd)
		checkCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := checkCmd.CombinedOutput()

		if err == nil && strings.Contains(string(out), "Restarted systemd service") {
			result["openclawRestarted"] = true
			result["message"] = "WSL 2 已启动，OpenClaw Gateway 已重启"
		}
	}

	return result
}

// RunWSLAsync 异步启动 WSL 并通过事件通知前端进度
func (m *Manager) RunWSLAsync() {
	go func() {
		runtime.EventsEmit(m.ctx, "wsl:phase", "wsl")

		cmd := exec.Command("wsl", "sleep", "infinity")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		err := cmd.Start()
		if err != nil {
			runtime.EventsEmit(m.ctx, "wsl:done", map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}

		result := map[string]interface{}{"ok": true, "message": "WSL 2 已在后台启动"}

		if m.openclawMgr != nil {
			runtime.EventsEmit(m.ctx, "wsl:phase", "openclaw")
			combinedCmd := `command -v openclaw >/dev/null 2>&1 && /usr/local/bin/openclaw gateway restart 2>&1 || true`
			checkCmd := exec.Command("wsl", "-e", "bash", "-c", combinedCmd)
			checkCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			out, err := checkCmd.CombinedOutput()

			if err == nil && strings.Contains(string(out), "Restarted systemd service") {
				result["openclawRestarted"] = true
				result["message"] = "WSL 2 已启动，OpenClaw Gateway 已重启"
			}
		}

		runtime.EventsEmit(m.ctx, "wsl:done", result)
	}()
}

// InstallWSL 触发仅安装内核的 WSL 安装 (不带发行版)
func (m *Manager) InstallWSL() map[string]interface{} {
	// 使用 --no-distribution 避免从商店下载
	cmd := exec.Command("powershell", "-Command",
		"Start-Process", "powershell",
		"-ArgumentList", `"-NoProfile -Command wsl --install --no-distribution"`,
		"-Verb", "RunAs",
		"-Wait",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	err := cmd.Run()
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	return map[string]interface{}{"ok": true, "message": "WSL 基础环境已安装，请重启电脑后继续"}
}

// DownloadRootFS 从镜像站下载 Ubuntu RootFS 压缩包
func (m *Manager) DownloadRootFS() (string, error) {
	// 使用正确的 24.04 (Noble) RootFS (tar.xz 格式)
	url := "https://mirrors.tuna.tsinghua.edu.cn/ubuntu-cloud-images/noble/current/noble-server-cloudimg-amd64-root.tar.xz"
	tmpFile := filepath.Join(os.TempDir(), "ubuntu-24.04-noble-wsl-rootfs.tar.xz")

	// 创建 HTTP 客户端并增加 User-Agent，避免被镜像站屏蔽
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	// 如果文件已存在且大小正常，建议重新下载（或者根据需要增加断点续传）
	out, err := os.Create(tmpFile)
	if err != nil {
		return "", err
	}
	defer out.Close()

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败: %s", resp.Status)
	}

	size := resp.ContentLength
	counter := &WriteCounter{
		Total:   uint64(size),
		Ctx:     m.ctx,
		EventID: "wsl:download-progress",
	}

	if _, err = io.Copy(out, io.TeeReader(resp.Body, counter)); err != nil {
		return "", err
	}

	return tmpFile, nil
}

// ImportDistro 导入下载好的发行版
func (m *Manager) ImportDistro(tarPath string) error {
	// 确定安装路径: %AppData%\ClawManager\wsl\Ubuntu
	appData := os.Getenv("APPDATA")
	installPath := filepath.Join(appData, "ClawManager", "wsl", "Ubuntu")

	// 检查是否已存在名为 Ubuntu 的分发
	checkCmd := exec.Command("wsl", "--list", "--quiet")
	checkCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	outCheck, _ := checkCmd.CombinedOutput()
	cleanOut := strings.ReplaceAll(string(outCheck), "\x00", "")
	if strings.Contains(strings.ToLower(cleanOut), "ubuntu") {
		// 如果已存在，自动注销以确保新安装成功
		unregisterCmd := exec.Command("wsl", "--unregister", "Ubuntu")
		unregisterCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = unregisterCmd.Run()
	}

	// 确保安装目录存在且为空（注销后目录可能还残留文件）
	_ = os.RemoveAll(installPath)
	if err := os.MkdirAll(installPath, 0755); err != nil {
		return err
	}

	// wsl --import <Distro> <InstallLocation> <FileName>
	// 注意：新版 WSL 支持直接导入 .tar.xz
	cmd := exec.Command("wsl", "--import", "Ubuntu", installPath, tarPath, "--version", "2")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("导入失败: %v, 输出: %s", err, string(out))
	}

	// 导入成功后尝试清理临时文件
	_ = os.Remove(tarPath)

	return nil
}

// ConfigureMirrors 配置 Ubuntu 24.04 的国内镜像源 (DEB822 格式)
func (m *Manager) ConfigureMirrors() error {
	// 替换 archive.ubuntu.com 和 security.ubuntu.com 为清华源
	sourceFile := "/etc/apt/sources.list.d/ubuntu.sources"
	mirror := "mirrors.tuna.tsinghua.edu.cn"

	commands := []string{
		fmt.Sprintf("sed -i 's/archive.ubuntu.com/%s/g' %s", mirror, sourceFile),
		fmt.Sprintf("sed -i 's/security.ubuntu.com/%s/g' %s", mirror, sourceFile),
	}

	for _, c := range commands {
		cmd := exec.Command("wsl", "-d", "Ubuntu", "-u", "root", "bash", "-c", c)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("配置镜像源失败: %v, 输出: %s", err, string(out))
		}
	}

	return nil
}

// InstallUbuntu 进行完整的镜像下载、导入和配置流程
func (m *Manager) InstallUbuntu() {
	// 触发进度事件开始
	runtime.EventsEmit(m.ctx, "wsl:install-status", "正在从镜像站下载 Ubuntu 24.04 RootFS...")
	tarPath, err := m.DownloadRootFS()
	if err != nil {
		runtime.EventsEmit(m.ctx, "wsl:install-done", map[string]interface{}{"ok": false, "error": "下载失败: " + err.Error()})
		return
	}

	runtime.EventsEmit(m.ctx, "wsl:install-status", "正在导入发行版 (这可能需要几分钟)...")
	if err := m.ImportDistro(tarPath); err != nil {
		runtime.EventsEmit(m.ctx, "wsl:install-done", map[string]interface{}{"ok": false, "error": "导入失败: " + err.Error()})
		return
	}

	runtime.EventsEmit(m.ctx, "wsl:install-status", "正在自动配置国内镜像源与 systemd...")
	// 1. 配置 APT 镜像
	_ = m.ConfigureMirrors()

	// 2. 配置 systemd (这是 OpenClaw 运行的关键)
	wslConf := "[boot]\\nsystemd=true\\n"
	confCmd := fmt.Sprintf("printf \"%s\" > /etc/wsl.conf", wslConf)
	confExec := exec.Command("wsl", "-d", "Ubuntu", "-u", "root", "bash", "-c", confCmd)
	confExec.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = confExec.Run()

	// 3. 关键：执行 shutdown 强制重启以加载 wsl.conf
	runtime.EventsEmit(m.ctx, "wsl:install-status", "正在重置 WSL 环境以使配置生效...")
	cmd := exec.Command("wsl", "--shutdown")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()

	runtime.EventsEmit(m.ctx, "wsl:install-done", map[string]interface{}{"ok": true, "message": "Ubuntu 24.04 安装并完成初始化（含 systemd）！"})
}

// IsSystemdEnabled 检查 WSL 中 systemd 是否真正可用
func (m *Manager) IsSystemdEnabled() bool {
	cmd := exec.Command("wsl", "-e", "bash", "-c", "systemctl --user status 2>&1 | head -1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(out))
	// 如果输出包含 "failed to connect" 或 "not found"，说明 systemd 不可用
	return !strings.Contains(lower, "failed") && !strings.Contains(lower, "not found") && !strings.Contains(lower, "no such")
}

// EnableSystemd 在 WSL 中写入 /etc/wsl.conf 启用 systemd
func (m *Manager) EnableSystemd() error {
	cmd := `grep -q 'systemd=true' /etc/wsl.conf 2>/dev/null || (printf '[boot]\nsystemd=true\n' | sudo tee /etc/wsl.conf > /dev/null)`
	wslCmd := exec.Command("wsl", "-e", "bash", "-c", cmd)
	wslCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_, err := wslCmd.CombinedOutput()
	return err
}

// WriteCounter 用于跟踪下载进度
type WriteCounter struct {
	Total   uint64
	Current uint64
	Ctx     context.Context
	EventID string
}

func (wc *WriteCounter) Write(p []byte) (int, error) {
	n := len(p)
	wc.Current += uint64(n)
	if wc.Ctx != nil {
		percentage := float64(wc.Current) / float64(wc.Total) * 100
		runtime.EventsEmit(wc.Ctx, wc.EventID, percentage)
	}
	return n, nil
}
