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

type FeishuConfig struct {
	Enabled           bool   `json:"enabled"`
	AccountID         string `json:"accountId"`
	AppID             string `json:"appId"`
	AppSecret         string `json:"appSecret"`
	BotName           string `json:"botName"`
	Domain            string `json:"domain"`
	ConnectionMode    string `json:"connectionMode"`
	DMPolicy          string `json:"dmPolicy"`
	VerificationToken string `json:"verificationToken"`
}

func hiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}
	return cmd
}

func wslBashFast(command string) (string, error) {
	cmd := hiddenCommand("wsl", "bash", "-lc", command)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
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

func (m *Manager) GetFeishuConfig() (FeishuConfig, error) {
	config := FeishuConfig{
		Enabled:        true,
		AccountID:      "main",
		Domain:         "feishu",
		ConnectionMode: "websocket",
		DMPolicy:       "pairing",
	}

	output, err := wslBashFast("cat /root/.openclaw/openclaw.json 2>/dev/null || echo '{}'")
	if err != nil {
		return config, err
	}

	var root map[string]interface{}
	if err := json.Unmarshal([]byte(output), &root); err != nil {
		return config, err
	}

	channels, _ := root["channels"].(map[string]interface{})
	feishu, _ := channels["feishu"].(map[string]interface{})
	if feishu == nil {
		return config, nil
	}

	if enabled, ok := feishu["enabled"].(bool); ok {
		config.Enabled = enabled
	}
	if domain, ok := feishu["domain"].(string); ok && strings.TrimSpace(domain) != "" {
		config.Domain = domain
	}
	if connectionMode, ok := feishu["connectionMode"].(string); ok && strings.TrimSpace(connectionMode) != "" {
		config.ConnectionMode = connectionMode
	}
	if dmPolicy, ok := feishu["dmPolicy"].(string); ok && strings.TrimSpace(dmPolicy) != "" {
		config.DMPolicy = dmPolicy
	}
	if verificationToken, ok := feishu["verificationToken"].(string); ok {
		config.VerificationToken = verificationToken
	}
	if defaultAccount, ok := feishu["defaultAccount"].(string); ok && strings.TrimSpace(defaultAccount) != "" {
		config.AccountID = defaultAccount
	}

	accounts, _ := feishu["accounts"].(map[string]interface{})
	if accounts == nil {
		return config, nil
	}

	account, _ := accounts[config.AccountID].(map[string]interface{})
	if account == nil {
		for accountID, raw := range accounts {
			candidate, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			config.AccountID = accountID
			account = candidate
			break
		}
	}
	if account == nil {
		return config, nil
	}

	if appID, ok := account["appId"].(string); ok {
		config.AppID = appID
	}
	if appSecret, ok := account["appSecret"].(string); ok {
		config.AppSecret = appSecret
	}
	if botName, ok := account["botName"].(string); ok {
		config.BotName = botName
	}
	if accountDomain, ok := account["domain"].(string); ok && strings.TrimSpace(accountDomain) != "" {
		config.Domain = accountDomain
	}

	return config, nil
}

func (m *Manager) SaveFeishuConfig(feishuConfig FeishuConfig) error {
	output, err := wslBashFast("cat /root/.openclaw/openclaw.json 2>/dev/null || echo '{}'")
	if err != nil {
		return err
	}

	var root map[string]interface{}
	if err := json.Unmarshal([]byte(output), &root); err != nil {
		return err
	}

	channels, _ := root["channels"].(map[string]interface{})
	if channels == nil {
		channels = map[string]interface{}{}
		root["channels"] = channels
	}

	feishu, _ := channels["feishu"].(map[string]interface{})
	if feishu == nil {
		feishu = map[string]interface{}{}
		channels["feishu"] = feishu
	}

	accountID := strings.TrimSpace(feishuConfig.AccountID)
	if accountID == "" {
		accountID = "main"
	}

	domain := strings.TrimSpace(feishuConfig.Domain)
	if domain == "" {
		domain = "feishu"
	}

	connectionMode := strings.TrimSpace(feishuConfig.ConnectionMode)
	if connectionMode == "" {
		connectionMode = "websocket"
	}

	dmPolicy := strings.TrimSpace(feishuConfig.DMPolicy)
	if dmPolicy == "" {
		dmPolicy = "pairing"
	}

	feishu["enabled"] = feishuConfig.Enabled
	feishu["defaultAccount"] = accountID
	feishu["domain"] = domain
	feishu["connectionMode"] = connectionMode
	feishu["dmPolicy"] = dmPolicy

	if connectionMode == "webhook" && strings.TrimSpace(feishuConfig.VerificationToken) != "" {
		feishu["verificationToken"] = strings.TrimSpace(feishuConfig.VerificationToken)
	} else {
		delete(feishu, "verificationToken")
	}

	accounts, _ := feishu["accounts"].(map[string]interface{})
	if accounts == nil {
		accounts = map[string]interface{}{}
		feishu["accounts"] = accounts
	}

	account, _ := accounts[accountID].(map[string]interface{})
	if account == nil {
		account = map[string]interface{}{}
		accounts[accountID] = account
	}

	account["appId"] = strings.TrimSpace(feishuConfig.AppID)
	account["appSecret"] = strings.TrimSpace(feishuConfig.AppSecret)

	if strings.TrimSpace(feishuConfig.BotName) != "" {
		account["botName"] = strings.TrimSpace(feishuConfig.BotName)
	} else {
		delete(account, "botName")
	}

	if domain == "lark" {
		account["domain"] = "lark"
	} else {
		delete(account, "domain")
	}

	updatedConfig, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}

	cmd := fmt.Sprintf("mkdir -p /root/.openclaw && cat > /root/.openclaw/openclaw.json << 'EOF'\n%s\nEOF", string(updatedConfig))
	_, err = wslBashFast(cmd)
	return err
}

// ToggleFeishuPlugin 启用或禁用飞书插件
func (m *Manager) ToggleFeishuPlugin(enabled bool) error {
	plugins, err := m.GetPlugins()
	if err != nil {
		return err
	}
	for _, plugin := range plugins {
		joined := fmt.Sprintf("%s %s", plugin.ID, plugin.Name)
		joined = strings.ToLower(joined)
		if strings.Contains(joined, "feishu") || strings.Contains(joined, "lark") {
			if enabled && !plugin.Enabled {
				return m.EnablePlugin(plugin.ID)
			} else if !enabled && plugin.Enabled {
				return m.DisablePlugin(plugin.ID)
			}
		}
	}
	return nil
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
