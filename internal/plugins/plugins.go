package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}
	return cmd
}

type Manager struct {
	ctx context.Context
}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) SetContext(ctx context.Context) {
	m.ctx = ctx
}

func stripANSI(s string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return ansi.ReplaceAllString(s, "")
}

type Plugin struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
	Enabled bool   `json:"enabled"`
}

// pluginsResponse 是 openclaw plugins list --json 的返回结构
type pluginsResponse struct {
	WorkspaceDir string   `json:"workspaceDir"`
	Plugins      []Plugin `json:"plugins"`
}

func (m *Manager) GetPlugins() ([]Plugin, error) {
	cmd := hiddenCommand("wsl", "bash", "-lc", "openclaw plugins list --json 2>&1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("command failed: %v, output: %s", err, string(out))
	}

	// 去掉可能的 ANSI 码和 \r
	clean := stripANSI(strings.ReplaceAll(string(out), "\r", ""))
	clean = strings.TrimSpace(clean)

	// 如果输出为空，返回空列表
	if clean == "" {
		return []Plugin{}, nil
	}

	// 找到 JSON 的起始位置，忽略前面的警告输出
	// 优先查找 {，因为实际 JSON 响应以 { 开头，而警告信息中的 [plugins] 会干扰查找
	startIndex := strings.Index(clean, "{")
	if startIndex == -1 {
		// 如果没有找到 {，再尝试查找 [（兼容数组格式）
		startIndex = strings.Index(clean, "[")
	}
	if startIndex != -1 {
		clean = clean[startIndex:]
	}

	// 尝试解析为包装结构
	var response pluginsResponse
	if err := json.Unmarshal([]byte(clean), &response); err == nil {
		return response.Plugins, nil
	}

	// 尝试解析为 JSON 数组（兼容旧格式）
	var plugins []Plugin
	if err := json.Unmarshal([]byte(clean), &plugins); err == nil {
		return plugins, nil
	}

	// 尝试解析为单个 JSON 对象
	var singlePlugin Plugin
	if err := json.Unmarshal([]byte(clean), &singlePlugin); err == nil {
		return []Plugin{singlePlugin}, nil
	}

	return nil, fmt.Errorf("unable to parse plugins output: %s", clean)
}

func (m *Manager) GetPluginDetail(id string) (string, error) {
	out, err := hiddenCommand("wsl", "bash", "-lc",
		"openclaw plugins inspect "+id+" --json").Output()
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(string(out), "\r", ""), nil
}

// 顺手把操作也封装好
func (m *Manager) EnablePlugin(id string) error {
	out, err := hiddenCommand("wsl", "bash", "-lc", fmt.Sprintf("openclaw plugins enable '%s' 2>&1", id)).CombinedOutput()
	outputStr := string(out)
	if err != nil {
		return fmt.Errorf("启用插件失败: %v, 输出: %s", err, outputStr)
	}
	lowerOut := strings.ToLower(outputStr)
	if strings.Contains(lowerOut, "error") || strings.Contains(lowerOut, "failed") {
		return fmt.Errorf("启用插件似乎失败了: %s", outputStr)
	}
	return nil
}

func (m *Manager) DisablePlugin(id string) error {
	out, err := hiddenCommand("wsl", "bash", "-lc", fmt.Sprintf("openclaw plugins disable '%s' 2>&1", id)).CombinedOutput()
	outputStr := string(out)
	if err != nil {
		return fmt.Errorf("禁用插件失败: %v, 输出: %s", err, outputStr)
	}
	lowerOut := strings.ToLower(outputStr)
	if strings.Contains(lowerOut, "error") || strings.Contains(lowerOut, "failed") {
		return fmt.Errorf("禁用插件似乎失败了: %s", outputStr)
	}
	return nil
}

func (m *Manager) UpdateAllPlugins() error {
	out, err := hiddenCommand("wsl", "bash", "-lc", "openclaw plugins update --all 2>&1").CombinedOutput()
	outputStr := string(out)
	if err != nil {
		return fmt.Errorf("更新插件失败: %v, 输出: %s", err, outputStr)
	}
	lowerOut := strings.ToLower(outputStr)
	if strings.Contains(lowerOut, "error") || strings.Contains(lowerOut, "failed") {
		return fmt.Errorf("更新插件似乎失败了: %s", outputStr)
	}
	return nil
}

// InstallPlugin 安装插件
func (m *Manager) InstallPlugin(pluginName string) error {
	out, err := hiddenCommand("wsl", "bash", "-lc", fmt.Sprintf("yes | openclaw plugins install '%s' 2>&1", pluginName)).CombinedOutput()
	outputStr := string(out)
	if err != nil {
		return fmt.Errorf("安装插件失败: %v, 输出: %s", err, outputStr)
	}
	lowerOut := strings.ToLower(outputStr)
	if strings.Contains(lowerOut, "error") || strings.Contains(lowerOut, "failed") {
		return fmt.Errorf("安装插件似乎失败了: %s", outputStr)
	}
	return nil
}

// UninstallPlugin 卸载插件
func (m *Manager) UninstallPlugin(pluginId string) error {
	out, err := hiddenCommand("wsl", "bash", "-lc", fmt.Sprintf("yes | openclaw plugins uninstall '%s' 2>&1", pluginId)).CombinedOutput()
	outputStr := string(out)

	// 2. 检查命令返回值和输出关键词
	if err != nil {
		return fmt.Errorf("卸载命令执行出错: %v, 输出: %s", err, outputStr)
	}

	lowerOut := strings.ToLower(outputStr)
	if strings.Contains(lowerOut, "error") || strings.Contains(lowerOut, "failed") || strings.Contains(lowerOut, "not found") {
		return fmt.Errorf("卸载过程中遇到错误: %s", outputStr)
	}

	// 3. 二次验证：检查列表中是否依然存在该插件 (等待一小会儿确保磁盘/缓存更新)
	time.Sleep(1 * time.Second)
	plugins, listErr := m.GetPlugins()
	if listErr != nil {
		// 如果列表检查失败，我们也认为卸载可能有问题或者状态不明
		return fmt.Errorf("卸载后验证失败 (获取列表出错): %v", listErr)
	}

	for _, p := range plugins {
		if p.ID == pluginId || p.Name == pluginId {
			return fmt.Errorf("卸载失败：命令虽然执行但插件 '%s' 依然存在于列表中。输出: %s", pluginId, outputStr)
		}
	}

	return nil
}

// InstallCustomPlugin 执行自定义命令安装插件
func (m *Manager) InstallCustomPlugin(command string) error {
	out, err := hiddenCommand("wsl", "bash", "-lc", command+" 2>&1").CombinedOutput()
	outputStr := string(out)
	if err != nil {
		return fmt.Errorf("执行自定义命令失败: %v, 输出: %s", err, outputStr)
	}
	lowerOut := strings.ToLower(outputStr)
	if strings.Contains(lowerOut, "error") || strings.Contains(lowerOut, "failed") {
		return fmt.Errorf("自定义命令执行结果中包含错误提示: %s", outputStr)
	}
	return nil
}

// WeixinAuth 微信授权
func (m *Manager) WeixinAuth() {
	cmd := hiddenCommand("wsl", "bash", "-lc", "openclaw channels login --channel openclaw-weixin")

	// 合并 stdout + stderr 一起读（CLI 可能把内容输出到 stderr）
	cmd.Stderr = cmd.Stdout
	stdout, _ := cmd.StdoutPipe()

	cmd.Start()

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		runtime.EventsEmit(m.ctx, "weixinauth:log", line)

		// 提取 URL（根据实际输出格式调整）
		if strings.HasPrefix(line, "https://") {
			runtime.EventsEmit(m.ctx, "weixinauth:url", strings.TrimSpace(line))
		}
	}

	cmd.Wait()
	runtime.EventsEmit(m.ctx, "weixinauth:done", "")
}
