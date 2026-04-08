package openclaw

import (
	"ClawManager/internal/wsl"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"syscall" // 引入 syscall 用于隐藏 Windows 下的 cmd 黑框
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const gatewayListenPort = 18789

// hostListenGateway 从 Windows 侧探测本机 Gateway 端口（与 Dashboard 一致，依赖 WSL2 localhost 转发）。
func hostListenGateway() bool {
	addr := fmt.Sprintf("127.0.0.1:%d", gatewayListenPort)
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// inferGatewayRunningFromStatusOutput 根据 openclaw gateway status 的文本与 hostPortOpen 综合判断 Gateway 是否真正可用。
// 不能仅凭 "Runtime: running"：CLI 可能在 RPC 失败、端口未监听时仍打印该文案。
func inferGatewayRunningFromStatusOutput(cmdSuccess bool, output string, hostPortOpen bool) bool {
	if !cmdSuccess {
		return false
	}
	if strings.Contains(output, "RPC probe: ok") {
		return true
	}
	lower := strings.ToLower(output)
	if strings.Contains(lower, "rpc probe: failed") && !hostPortOpen {
		return false
	}
	if strings.Contains(lower, "not listening") && !hostPortOpen {
		return false
	}
	return hostPortOpen
}

// stripANSI 去除 ANSI 转义序列
func stripANSI(s string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return ansi.ReplaceAllString(s, "")
}

// Manager 管理 OpenClaw 逻辑
type Manager struct {
	ctx    context.Context
	wslMgr *wsl.Manager
}

// NewManager 实例化 OpenClaw 管理器
func NewManager(wslMgr *wsl.Manager) *Manager {
	return &Manager{wslMgr: wslMgr}
}

// SetContext 设置 Wails 上下文
func (m *Manager) SetContext(ctx context.Context) {
	m.ctx = ctx
}

type InstallStep struct {
	Index  int    `json:"index"`
	Total  int    `json:"total"`
	Label  string `json:"label"`
	Status string `json:"status"` // "running" | "done" | "error"
	Output string `json:"output"`
}

// wslBash 在 WSL bash 中运行命令，使用交互式 shell（会加载 .bashrc）
func wslBash(command string) (string, error) {
	cmd := exec.Command("wsl", "-e", "bash", "--login", "-c", command)
	// 隐藏 Windows 下调用 wsl.exe 时弹出的 cmd 窗口
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// wslBashFast 快速运行命令，不解析用户配置文件
func wslBashFast(command string) (string, error) {
	cmd := exec.Command("wsl", "-e", "bash", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// wslBashStream 运行命令并实时通过回调发送输出日志
func wslBashStream(command string, onLog func(string)) error {
	// 显式指定 -u root 确保在所有机器上行为一致
	cmd := exec.Command("wsl", "-d", "Ubuntu", "-u", "root", "-e", "bash", "--login", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout // 合并输出

	if err := cmd.Start(); err != nil {
		return err
	}

	// 预编译正则用于去除 ANSI 转义字符 (如 \x1b[38;2;...m)
	re := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

	reader := bufio.NewReader(stdout)
	for {
		// 使用 ReadString('\r') 或 ReadBytes 可捕捉进度更新
		// 但为了兼容多种 shell 输出，我们采用按字符/按块读取并手动切分
		line, err := reader.ReadString('\n')
		if err != nil {
			// 处理最后残留的字符
			if line != "" {
				cleanLine := re.ReplaceAllString(line, "")
				onLog(cleanLine)
			}
			break
		}

		// 检查行内是否包含 \r (进度条常见)
		// 如果包含 \r，将其拆分为多行发送，以便前端展现"变化"
		if strings.Contains(line, "\r") {
			parts := strings.Split(line, "\r")
			for _, part := range parts {
				if part != "" {
					cleanLine := re.ReplaceAllString(part, "")
					onLog(cleanLine)
				}
			}
		} else {
			cleanLine := re.ReplaceAllString(line, "")
			onLog(cleanLine)
		}
	}

	return cmd.Wait()
}

// isGatewayRunning 稳健地判断网关服务是否处于活跃状态
func isGatewayRunning(out string) bool {
	lower := strings.ToLower(out)
	if strings.Contains(lower, "not running") || strings.Contains(lower, "inactive") || strings.Contains(lower, "stopped") {
		return false
	}
	return strings.Contains(lower, "running") || strings.Contains(lower, "active")
}

// probePort 通过 TCP 端口探测真正确认 gateway 是否在监听 18789
// 这是最权威的检测方式，不依赖 systemd 是否可用
func probePort() bool {
	out, err := wslBashFast(`ss -tlnp 'sport = :18789' 2>/dev/null | grep -c 18789 || echo 0`)
	if err != nil {
		return false
	}
	count := strings.TrimSpace(out)
	return count != "" && count != "0"
}

// ensureConfig 首次安装时写入完整的 openclaw 网关配置
// token 采用固定格式生成，确保每日唯一且可追溯
func ensureConfig() {
	autoToken := "claw_manager_" + time.Now().Format("20060102") + "_static_tk"

	// 合并多个命令为一个，减少 WSL 进程启动次数
	// 用 openclaw config set 写入，由工具自身保证 schema 合法
	combinedCmd := `mkdir -p /root/.openclaw && ` +
		`/usr/local/bin/openclaw config set gateway.mode local gateway.bind loopback gateway.port 18789 gateway.auth.mode token gateway.auth.token ` + autoToken + ` 2>/dev/null || true`

	wslBashFast(combinedCmd)
}

// CheckOpenClaw 检测 WSL 中是否安装了 openclaw。
// 修复 1：发送任何 wsl 命令前先检查 WSL 进程是否存在，
//
//	避免 wslBashFast 的副作用将 WSL 冷启动，导致调用方读到假阳性的 wslRunning。
//
// 修复 2：将 gatewayRunning 的判断从模糊的 contains("active") 改为精确匹配 "active"，
//
//	防止 systemd 服务处于过渡态 "activating" 时被误判为已运行。
func (m *Manager) CheckOpenClaw() wsl.OpenClawStatus {
	// 修复 1：WSL 进程不存在则直接返回，不发任何命令
	if !m.wslMgr.CheckWSLRunning() {
		return wsl.OpenClawStatus{Installed: false}
	}
	return m.checkOpenClawCore()
}

// CheckOpenClawAssumingActiveSession 由已确认存在 WSL 会话的调用方使用（例如启动时的 GetInitialState），
// 避免再次执行 wsl --list --running，少一次 wsl.exe 往返。
func (m *Manager) CheckOpenClawAssumingActiveSession() wsl.OpenClawStatus {
	return m.checkOpenClawCore()
}

func (m *Manager) checkOpenClawCore() wsl.OpenClawStatus {
	// 合并 3 个串行 WSL 调用为 1 个，用分隔符区分各段输出
	combinedCmd := `BIN=$(command -v openclaw || which openclaw 2>/dev/null || ls /home/honphie/.npm-global/bin/openclaw 2>/dev/null) || exit 1; ` +
		`echo "__PATH__:$BIN"; ` +
		`echo "__VER__:$($BIN --version 2>/dev/null)"; ` +
		`echo "__STATUS_BEGIN__"; ` +
		`/usr/local/bin/openclaw gateway status 2>&1; ` +
		`echo "__STATUS_END__"`

	out, err := wslBashFast(combinedCmd)
	if err != nil || out == "" {
		return wsl.OpenClawStatus{Installed: false}
	}

	// 解析合并输出
	version := "Unknown"
	var statusOut string

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "__VER__:") {
			raw := strings.TrimPrefix(line, "__VER__:")
			cleanVersion := stripANSI(strings.TrimSpace(raw))
			var printable strings.Builder
			for _, r := range cleanVersion {
				if r >= 32 && r < 127 {
					printable.WriteRune(r)
				}
			}
			if v := printable.String(); v != "" {
				version = v
			}
		}
	}

	if idx := strings.Index(out, "__STATUS_BEGIN__"); idx >= 0 {
		tail := out[idx+len("__STATUS_BEGIN__"):]
		if end := strings.Index(tail, "__STATUS_END__"); end >= 0 {
			statusOut = strings.TrimSpace(tail[:end])
		} else {
			statusOut = strings.TrimSpace(tail)
		}
	}

	hostPortOpen := hostListenGateway()
	isRunning := inferGatewayRunningFromStatusOutput(true, statusOut, hostPortOpen)

	return wsl.OpenClawStatus{
		Installed:      true,
		Version:        version,
		GatewayRunning: isRunning,
	}
}

// GetGatewayStatus 检查 openclaw 网关是否正在运行
// 以 RPC 探测与本机端口为准，避免仅匹配 "Runtime: running" 造成假阳性。
func (m *Manager) GetGatewayStatus() map[string]interface{} {
	// 执行 openclaw gateway status 命令，完全模仿手动命令行模式
	cmd := "/usr/local/bin/openclaw gateway status 2>&1"
	output, err := wslBashFast(cmd)
	cmdSuccess := err == nil

	hostPortOpen := hostListenGateway()
	isRunning := inferGatewayRunningFromStatusOutput(cmdSuccess, output, hostPortOpen)

	// 提取更多详细信息
	statusDetails := make(map[string]interface{})
	statusDetails["commandOutput"] = output
	statusDetails["commandSuccess"] = cmdSuccess
	statusDetails["running"] = isRunning
	statusDetails["hostPortOpen"] = hostPortOpen

	// 从输出中提取关键信息
	if cmdSuccess {
		// 提取服务类型
		if strings.Contains(output, "Service: systemd") {
			statusDetails["serviceType"] = "systemd"
			statusDetails["serviceEnabled"] = strings.Contains(output, "Service: systemd (enabled)")
		}

		// 提取监听端口
		portRegex := regexp.MustCompile(`port=(\d+)`)
		if portMatches := portRegex.FindStringSubmatch(output); len(portMatches) > 1 {
			statusDetails["port"] = portMatches[1]
		}

		// 提取 PID
		pidRegex := regexp.MustCompile(`pid (\d+)`)
		if pidMatches := pidRegex.FindStringSubmatch(output); len(pidMatches) > 1 {
			statusDetails["pid"] = pidMatches[1]
		}

		// 提取运行状态
		runtimeRegex := regexp.MustCompile(`Runtime: ([a-zA-Z]+)`)
		if runtimeMatches := runtimeRegex.FindStringSubmatch(output); len(runtimeMatches) > 1 {
			statusDetails["runtimeState"] = runtimeMatches[1]
		}

		// 提取 RPC 探测结果
		rpcRegex := regexp.MustCompile(`RPC probe: ([a-zA-Z]+)`)
		if rpcMatches := rpcRegex.FindStringSubmatch(output); len(rpcMatches) > 1 {
			statusDetails["rpcStatus"] = rpcMatches[1]
		}

		// 提取监听地址
		listeningRegex := regexp.MustCompile(`Listening: ([\d.]+:\d+)`)
		if listeningMatches := listeningRegex.FindStringSubmatch(output); len(listeningMatches) > 1 {
			statusDetails["listeningAddress"] = listeningMatches[1]
		}
	}

	return statusDetails
}

// StartGateway 启动 openclaw 网关
// 返回字段说明：
//
//	ok            bool   — 整体操作是否成功
//	running       bool   — gateway 端口是否真正在监听
//	needsRestart  bool   — 已启用 systemd，需要用户重启 WSL 后再试
//	error         string — 错误描述
//	log           string — gateway 日志末尾（仅失败时）
func (m *Manager) StartGateway() map[string]interface{} {
	cmd := "/usr/local/bin/openclaw gateway start 2>&1"
	output, err := wslBashFast(cmd)

	if err != nil {
		return map[string]interface{}{
			"ok":            false,
			"running":       false,
			"commandOutput": output,
			"error":         "Gateway 启动失败: " + err.Error(),
		}
	}

	// openclaw gateway start 成功时会输出 "Restarted systemd service"
	isRunning := strings.Contains(output, "Restarted systemd service") ||
		strings.Contains(output, "Started systemd service")

	if !isRunning {
		return map[string]interface{}{
			"ok":            false,
			"running":       false,
			"commandOutput": output,
			"error":         "Gateway 启动异常，请查看输出",
		}
	}

	return map[string]interface{}{
		"ok":            true,
		"running":       true,
		"commandOutput": output,
		"message":       "Gateway 服务已成功启动",
	}
}

// FirstStartGateway 首次启动 openclaw 网关，执行完整的配置和服务注册
// 仅在安装 OpenClaw 后自动运行一次
func (m *Manager) FirstStartGateway() map[string]interface{} {
	// Step 1: systemd 检查，首次安装必须
	if !m.wslMgr.IsSystemdEnabled() {
		err := m.wslMgr.EnableSystemd()
		if err != nil {
			return map[string]interface{}{
				"ok":           false,
				"running":      false,
				"needsRestart": false,
				"error":        "自动写入 systemd 配置失败: " + err.Error(),
			}
		}
		return map[string]interface{}{
			"ok":           false,
			"running":      false,
			"needsRestart": true,
			"error":        "已自动启用 systemd，需要重启 WSL 后再试",
		}
	}

	// Step 2: 首次完整配置
	ensureConfig()

	// Step 3: doctor --repair + daemon-reload（尽力而为）
	doctorOutput, _ := wslBashFast("/usr/local/bin/openclaw doctor --repair 2>/dev/null || true && systemctl --user daemon-reload 2>/dev/null || true")

	// Step 4: 启动网关，直接解析输出判断结果
	startOutput, startErr := wslBashFast("/usr/local/bin/openclaw gateway start 2>&1")

	if startErr != nil {
		logOut, _ := wslBashFast("tail -30 /tmp/openclaw/openclaw-$(date +%Y-%m-%d).log 2>/dev/null")
		return map[string]interface{}{
			"ok":           false,
			"running":      false,
			"doctorOutput": doctorOutput,
			"startOutput":  startOutput,
			"error":        "Gateway 启动命令执行失败: " + startErr.Error(),
			"log":          logOut,
		}
	}

	isRunning := strings.Contains(startOutput, "Restarted systemd service") ||
		strings.Contains(startOutput, "Started systemd service")

	if !isRunning {
		logOut, _ := wslBashFast("tail -30 /tmp/openclaw/openclaw-$(date +%Y-%m-%d).log 2>/dev/null")
		return map[string]interface{}{
			"ok":           false,
			"running":      false,
			"doctorOutput": doctorOutput,
			"startOutput":  startOutput,
			"error":        "Gateway 启动异常，请查看输出",
			"log":          logOut,
		}
	}

	return map[string]interface{}{
		"ok":           true,
		"running":      true,
		"doctorOutput": doctorOutput,
		"startOutput":  startOutput,
		"message":      "Gateway 服务已成功启动",
	}
}

// StopGateway 停止 openclaw 网关
func (m *Manager) StopGateway() map[string]interface{} {
	output, err := wslBashFast("/usr/local/bin/openclaw gateway stop 2>&1")

	if err != nil {
		return map[string]interface{}{
			"ok":            false,
			"commandOutput": output,
			"error":         "停止命令执行失败: " + err.Error(),
		}
	}

	if strings.Contains(output, "Stopped systemd service") {
		return map[string]interface{}{
			"ok":            true,
			"commandOutput": output,
			"message":       "Gateway 服务已成功停止",
		}
	}

	// 命令执行成功但输出不符合预期，收集日志辅助排查
	logOut, _ := wslBashFast("tail -30 /tmp/openclaw/openclaw-$(date +%Y-%m-%d).log 2>/dev/null")
	return map[string]interface{}{
		"ok":            false,
		"commandOutput": output,
		"log":           logOut,
		"error":         "Gateway 停止异常，请查看输出",
	}
}

func (m *Manager) RestartGateway() map[string]interface{} {
	output, err := wslBashFast("/usr/local/bin/openclaw gateway restart 2>&1")

	if err != nil {
		return map[string]interface{}{
			"ok":            false,
			"commandOutput": output,
			"error":         "重启命令执行失败: " + err.Error(),
		}
	}

	if strings.Contains(output, "Restarted systemd service") {
		return map[string]interface{}{
			"ok":            true,
			"commandOutput": output,
			"message":       "Gateway 服务已成功重启",
		}
	}

	logOut, _ := wslBashFast("tail -30 /tmp/openclaw/openclaw-$(date +%Y-%m-%d).log 2>/dev/null")
	return map[string]interface{}{
		"ok":            false,
		"commandOutput": output,
		"log":           logOut,
		"error":         "Gateway 重启异常，请查看输出",
	}
}

func (m *Manager) StreamGatewayLogs() {
	logFile := "/tmp/openclaw/openclaw-$(date +%Y-%m-%d).log"
	cmd := exec.Command("wsl", "-e", "bash", "-c", "tail -f "+logFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		runtime.EventsEmit(m.ctx, "gateway:log", map[string]interface{}{
			"level": "error",
			"text":  "Failed to create pipe: " + err.Error(),
		})
		return
	}

	if err := cmd.Start(); err != nil {
		runtime.EventsEmit(m.ctx, "gateway:log", map[string]interface{}{
			"level": "error",
			"text":  "Failed to start tail: " + err.Error(),
		})
		return
	}

	re := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if line != "" {
				cleanLine := re.ReplaceAllString(line, "")
				cleanLine = strings.TrimSpace(cleanLine)
				if cleanLine != "" {
					level := detectLogLevel(cleanLine)
					runtime.EventsEmit(m.ctx, "gateway:log", map[string]interface{}{
						"level": level,
						"text":  cleanLine,
					})
				}
			}
			break
		}

		cleanLine := re.ReplaceAllString(line, "")
		cleanLine = strings.TrimSpace(cleanLine)
		if cleanLine != "" {
			level := detectLogLevel(cleanLine)
			runtime.EventsEmit(m.ctx, "gateway:log", map[string]interface{}{
				"level": level,
				"text":  cleanLine,
			})
		}
	}

	cmd.Wait()
	runtime.EventsEmit(m.ctx, "gateway:log", map[string]interface{}{
		"level": "system",
		"text":  "Log stream ended",
	})
}

func detectLogLevel(text string) string {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "error") || strings.Contains(lower, "err]") || strings.Contains(lower, "failed") {
		return "error"
	}
	if strings.Contains(lower, "warn") || strings.Contains(lower, "warning") {
		return "warn"
	}
	if strings.Contains(lower, "debug") || strings.Contains(lower, "trace") {
		return "debug"
	}
	return "info"
}

func StopGatewayLogStream() error {
	return exec.Command("wsl", "-e", "bash", "-c", "pkill -f 'tail -f /tmp/openclaw' 2>/dev/null || true").Run()
}

// InstallOpenClaw 执行 4 步 openclaw 安装流程，并触发进度事件
func (m *Manager) InstallOpenClaw() {
	// 生成固定格式的token，用于安装过程中的配置
	autoToken := "claw_manager_" + time.Now().Format("20060102") + "_static_tk"

	steps := []struct {
		label string
		cmd   string
	}{
		{
			label: "配置系统环境 (Node.js v22.14+)",
			// 1. 清理旧源 2. 安装基础工具 3. 下载并解压 Node 4. 配置包管理器镜像
			cmd: `sudo rm -f /etc/apt/sources.list.d/nodesource.* && \
		  sudo apt-get update -y && sudo apt-get install -y curl wget git xz-utils && \
		  (wget -q --show-progress --progress=dot:giga https://mirrors.tuna.tsinghua.edu.cn/nodejs-release/v22.14.0/node-v22.14.0-linux-x64.tar.xz -O /tmp/node-v22.tar.xz || \
		   wget -q --show-progress --progress=dot:giga https://mirrors.aliyun.com/nodejs-release/v22.14.0/node-v22.14.0-linux-x64.tar.xz -O /tmp/node-v22.tar.xz) && \
		  sudo tar -xJf /tmp/node-v22.tar.xz -C /usr/local --strip-components=1 && \
		  sudo npm config set registry https://registry.npmmirror.com -g && \
		  npm config set registry https://registry.npmmirror.com`,
		},
		{
			label: "安装 OpenClaw (镜像加速)",
			// 注入 SHARP_BINARY_HOST 镜像，解决 sharp 库下载 libvips 慢的问题
			// 使用 sudo -E 确保环境变量能透传给 npm
			cmd: `sudo -E SHARP_BINARY_HOST=https://npmmirror.com/mirrors/sharp-libvips \
		  npm install -g openclaw --registry=https://registry.npmmirror.com --no-audit --no-fund --loglevel=info`,
		},
		{
			label: "自动配置网关参数",
			// 用 openclaw config set 写入，由工具自身保证 schema 合法
			// 设置认证模式为 token，并写入固定格式的 token
			cmd: `mkdir -p /root/.openclaw && ` +
				`/usr/local/bin/openclaw config set gateway.bind loopback 2>&1 || true && ` +
				`/usr/local/bin/openclaw config set gateway.port 18789 2>&1 || true && ` +
				`/usr/local/bin/openclaw config set gateway.auth.mode token 2>&1 || true && ` +
				`/usr/local/bin/openclaw config set gateway.auth.token ` + autoToken + ` 2>&1 || true`,
		},
		{
			label: "初始化系统服务与环境配置",
			// 1. 设置网关模式 2. 运行 onboard 补全目录结构
			cmd: `/usr/local/bin/openclaw config set gateway.mode local && /usr/local/bin/openclaw onboard --non-interactive --accept-risk --skip-ui 2>&1 || true`,
		},
	}

	total := len(steps)
	for i, step := range steps {
		// 触发事件：步骤开始运行
		runtime.EventsEmit(m.ctx, "install:step", InstallStep{
			Index:  i,
			Total:  total,
			Label:  step.label,
			Status: "running",
		})

		// 动态输出日志：采用流式执行
		err := wslBashStream(step.cmd, func(line string) {
			// 将每一行输出推送到前端
			runtime.EventsEmit(m.ctx, "install:log", line)
		})

		time.Sleep(100 * time.Millisecond)

		if err != nil && i < 2 { // 步骤 3 (onboard) 是尽力而为
			runtime.EventsEmit(m.ctx, "install:step", InstallStep{
				Index:  i,
				Total:  total,
				Label:  step.label,
				Status: "error",
			})
			runtime.EventsEmit(m.ctx, "install:done", map[string]interface{}{
				"ok":    false,
				"step":  i,
				"error": "执行失败，请检查控制台获取详细日志",
			})
			return
		}

		runtime.EventsEmit(m.ctx, "install:step", InstallStep{
			Index:  i,
			Total:  total,
			Label:  step.label,
			Status: "done",
		})
	}

	// 安装完成后，执行首次启动逻辑
	runtime.EventsEmit(m.ctx, "install:log", "正在执行首次启动配置...")
	firstStartResult := m.FirstStartGateway()
	if firstStartResult["needsRestart"] == true {
		runtime.EventsEmit(m.ctx, "install:log", "已自动启用 systemd，需要重启 WSL 后才能使用 Gateway 服务")
	} else if firstStartResult["running"] == true {
		runtime.EventsEmit(m.ctx, "install:log", "Gateway 服务首次启动成功")
	} else {
		runtime.EventsEmit(m.ctx, "install:log", "首次启动遇到问题: "+firstStartResult["error"].(string))
		runtime.EventsEmit(m.ctx, "install:log", "系统已完成基本配置，您可以在重启 WSL 后手动启动 Gateway 服务")
	}

	runtime.EventsEmit(m.ctx, "install:done", map[string]interface{}{"ok": true})
}

// UninstallOpenClaw 执行 OpenClaw 卸载流程
func (m *Manager) UninstallOpenClaw() {
	// 卸载命令序列
	steps := []struct {
		label string
		cmd   string
	}{
		{
			label: "停止并清理 OpenClaw 服务",
			cmd: `openclaw uninstall --all --yes --non-interactive 2>/dev/null || true; \
		systemctl --user disable --now openclaw-gateway.service 2>/dev/null || true; \
		rm -f ~/.config/systemd/user/openclaw-gateway.service 2>/dev/null || true; \
		systemctl --user daemon-reload 2>/dev/null || true`,
		},
		{
			label: "删除 OpenClaw 配置与数据文件",
			cmd:   `rm -rf ~/.openclaw ~/.config/openclaw ~/.local/share/openclaw 2>/dev/null || true`,
		},
		{
			label: "从全局移除 OpenClaw 包",
			cmd: `npm rm -g openclaw 2>/dev/null || true; \
		pnpm remove -g openclaw 2>/dev/null || true; \
		bun remove -g openclaw 2>/dev/null || true`,
		},
	}

	total := len(steps)
	for i, step := range steps {
		// 触发事件：步骤开始运行
		runtime.EventsEmit(m.ctx, "install:step", InstallStep{
			Index:  i,
			Total:  total,
			Label:  step.label,
			Status: "running",
		})

		// 动态输出日志：采用流式执行
		err := wslBashStream(step.cmd, func(line string) {
			// 将每一行输出推送到前端
			runtime.EventsEmit(m.ctx, "install:log", line)
		})

		time.Sleep(100 * time.Millisecond)

		if err != nil {
			// 卸载过程中遇到错误，仍然继续执行后续步骤
			runtime.EventsEmit(m.ctx, "install:step", InstallStep{
				Index:  i,
				Total:  total,
				Label:  step.label,
				Status: "error",
				Output: err.Error(),
			})
		} else {
			runtime.EventsEmit(m.ctx, "install:step", InstallStep{
				Index:  i,
				Total:  total,
				Label:  step.label,
				Status: "done",
			})
		}
	}

	// 卸载完成后触发事件
	runtime.EventsEmit(m.ctx, "install:done", map[string]interface{}{
		"ok": true,
	})
}

// GetAvailableVersions 获取远程可用的版本信息
func (m *Manager) GetAvailableVersions() map[string]interface{} {
	resp, err := http.Get("https://registry.npmmirror.com/openclaw")
	if err != nil {
		return map[string]interface{}{"ok": false, "error": "获取版本失败: " + err.Error()}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return map[string]interface{}{"ok": false, "error": "解析版本数据失败: " + err.Error()}
	}

	var result struct {
		DistTags map[string]string      `json:"dist-tags"`
		Versions map[string]interface{} `json:"versions"`
		Time     map[string]string      `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return map[string]interface{}{"ok": false, "error": "解析 JSON 失败: " + err.Error()}
	}

	var versions []string
	for v := range result.Versions {
		versions = append(versions, v)
	}

	return map[string]interface{}{
		"ok":       true,
		"latest":   result.DistTags["latest"],
		"versions": versions,
		"times":    result.Time,
	}
}

// InstallVersion 执行特定版本的 OpenClaw 一键覆盖安装
func (m *Manager) InstallVersion(targetVersion string) map[string]interface{} {
	steps := []struct {
		label string
		cmd   string
	}{
		{
			label: "准备环境并清理旧的系统服务",
			cmd:   `openclaw gateway stop 2>/dev/null || true`,
		},
		{
			label: fmt.Sprintf("下载并安装 OpenClaw %s", targetVersion),
			cmd:   fmt.Sprintf(`sudo -E SHARP_BINARY_HOST=https://npmmirror.com/mirrors/sharp-libvips npm install -g openclaw@%s --registry=https://registry.npmmirror.com --no-audit --no-fund --loglevel=info`, targetVersion),
		},
	}

	total := len(steps)
	for i, step := range steps {
		runtime.EventsEmit(m.ctx, "update:step", InstallStep{
			Index:  i,
			Total:  total,
			Label:  step.label,
			Status: "running",
		})

		err := wslBashStream(step.cmd, func(line string) {
			runtime.EventsEmit(m.ctx, "update:log", line)
		})

		time.Sleep(100 * time.Millisecond)

		if err != nil && i == 1 { // 步骤 1 (安装) 是关键
			runtime.EventsEmit(m.ctx, "update:step", InstallStep{
				Index:  i,
				Total:  total,
				Label:  step.label,
				Status: "error",
			})
			return map[string]interface{}{
				"ok":    false,
				"error": "执行失败，请检查控制台获取详细日志: " + err.Error(),
			}
		}

		runtime.EventsEmit(m.ctx, "update:step", InstallStep{
			Index:  i,
			Total:  total,
			Label:  step.label,
			Status: "done",
		})
	}

	// 尝试重启网关
	runtime.EventsEmit(m.ctx, "update:log", "正在启动 Gateway 服务...")
	startResult := m.RestartGateway()
	if startResult["ok"] == true {
		runtime.EventsEmit(m.ctx, "update:log", "Gateway 服务已成功启动")
	} else {
		// Restart 失败可能因为服务先前根本没在跑, 这里可以退而求其次尝试 Start
		startResultInner := m.StartGateway()
		if startResultInner["ok"] == true {
			runtime.EventsEmit(m.ctx, "update:log", "Gateway 服务已成功启动")
		} else {
			runtime.EventsEmit(m.ctx, "update:log", "自动启动 Gateway 遇到了问题。您可以后续手动启动。")
		}
	}

	return map[string]interface{}{"ok": true}
}
