package models

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// Manager 管理模型配置逻辑
type Manager struct {
	ctx context.Context
}

// NewManager 实例化模型管理器
func NewManager() *Manager {
	return &Manager{}
}

// SetContext 设置 Wails 上下文
func (m *Manager) SetContext(ctx context.Context) {
	m.ctx = ctx
}

// wslBashFast 快速运行命令，不解析用户配置文件
func wslBashFast(command string) (string, error) {
	cmd := exec.Command("wsl", "-e", "bash", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// GetModelsConfig 获取模型配置
func (m *Manager) GetModelsConfig() (map[string]interface{}, error) {
	// 读取配置文件内容
	cmd := "cat /root/.openclaw/openclaw.json 2>/dev/null || echo '{}'"
	output, err := wslBashFast(cmd)
	if err != nil {
		return nil, err
	}

	// 解析JSON
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(output), &config); err != nil {
		return nil, err
	}

	// 获取models配置，如果不存在则返回默认配置
	modelsConfig, ok := config["models"].(map[string]interface{})
	if !ok {
		modelsConfig = map[string]interface{}{
			"mode":      "merge",
			"providers": map[string]interface{}{},
		}
	}

	return modelsConfig, nil
}

// SaveModelsConfig 保存模型配置
func (m *Manager) SaveModelsConfig(modelsConfig map[string]interface{}) error {
	// 读取现有配置
	cmd := "cat /root/.openclaw/openclaw.json 2>/dev/null || echo '{}'"
	output, err := wslBashFast(cmd)
	if err != nil {
		return err
	}

	var config map[string]interface{}
	if err := json.Unmarshal([]byte(output), &config); err != nil {
		return err
	}

	// 更新models配置
	config["models"] = modelsConfig

	// 将更新后的配置写回文件
	updatedConfig, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	// 使用heredoc写入配置文件
	cmd = fmt.Sprintf("mkdir -p /root/.openclaw && cat > /root/.openclaw/openclaw.json << 'EOF'\n%s\nEOF", string(updatedConfig))
	_, err = wslBashFast(cmd)
	return err
}
