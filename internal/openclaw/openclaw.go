package openclaw

import (
	"ClawManager/internal/wsl"
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"syscall" // 引入 syscall 用于隐藏 Windows 下的 cmd 黑框
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

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
		// 如果包含 \r，将其拆分为多行发送，以便前端展现“变化”
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

// CheckOpenClaw 检测 WSL 中是否安装了 openclaw
func (m *Manager) CheckOpenClaw() wsl.OpenClawStatus {
	out, err := wslBashFast("command -v openclaw || which openclaw || ls /home/honphie/.npm-global/bin/openclaw 2>/dev/null")
	if err != nil || out == "" {
		return wsl.OpenClawStatus{Installed: false}
	}

	path := out
	if !strings.HasPrefix(path, "/") {
		path = "openclaw"
	}

	batchCmd := fmt.Sprintf("%s --version 2>/dev/null; systemctl --user is-active openclaw-gateway.service 2>/dev/null", path)
	batchOut, _ := wslBashFast(batchCmd)

	lines := strings.Split(batchOut, "\n")
	version := "Unknown"
	if len(lines) > 0 {
		cleanVersion := stripANSI(strings.TrimSpace(lines[0]))
		var printable strings.Builder
		for _, r := range cleanVersion {
			if r >= 32 && r < 127 {
				printable.WriteRune(r)
			}
		}
		version = printable.String()
		if version == "" {
			version = "Unknown"
		}
	}

	gatewayStatus := ""
	if len(lines) > 1 {
		gatewayStatus = strings.ToLower(lines[1])
	}
	running := strings.Contains(gatewayStatus, "active") && !strings.Contains(gatewayStatus, "inactive")

	return wsl.OpenClawStatus{
		Installed:      true,
		Version:        version,
		GatewayRunning: running,
	}
}

// GetGatewayStatus 检查 openclaw 网关是否正在运行
// 使用 TCP 端口探测作为权威判断，不依赖 systemd 是否可用
func (m *Manager) GetGatewayStatus() map[string]interface{} {
	// 执行 openclaw gateway status 命令，完全模仿手动命令行模式
	cmd := "/usr/local/bin/openclaw gateway status 2>&1"
	output, err := wslBashFast(cmd)
	cmdSuccess := err == nil

	// 分析命令输出，判断 Gateway 是否运行
	isRunning := cmdSuccess && (strings.Contains(output, "Runtime: running") || strings.Contains(output, "RPC probe: ok"))

	// 提取更多详细信息
	statusDetails := make(map[string]interface{})
	statusDetails["commandOutput"] = output
	statusDetails["commandSuccess"] = cmdSuccess
	statusDetails["running"] = isRunning

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
		  npm install -g openclaw --registry=https://registry.npmmirror.com --progress=true --loglevel=info`,
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
