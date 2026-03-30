package main

import (
	"context"

	"ClawManager/internal/dashboard"
	"ClawManager/internal/models"
	"ClawManager/internal/openclaw"
	"ClawManager/internal/plugins"
	"ClawManager/internal/wsl"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 是应用的主入口。
type App struct {
	ctx          context.Context
	wslMgr       *wsl.Manager
	openclawMgr  *openclaw.Manager
	dashboardMgr *dashboard.Manager
	pluginsMgr   *plugins.Manager
	modelsMgr    *models.Manager
}

// NewApp 创建应用实例。
func NewApp(w *wsl.Manager, o *openclaw.Manager, d *dashboard.Manager, p *plugins.Manager, m *models.Manager) *App {
	return &App{wslMgr: w, openclawMgr: o, dashboardMgr: d, pluginsMgr: p, modelsMgr: m}
}

// startup 在应用启动时注入上下文。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.openclawMgr.SetContext(ctx)
	a.wslMgr.SetContext(ctx)
	a.pluginsMgr.SetContext(ctx)
	a.modelsMgr.SetContext(ctx)
}

// GetInitialState 返回系统初始状态。
func (a *App) GetInitialState() map[string]interface{} {
	wslStatus, wslRunning := a.wslMgr.ProbeForInitialLoad()

	var openclawStatus wsl.OpenClawStatus
	if wslRunning {
		openclawStatus = a.openclawMgr.CheckOpenClawAssumingActiveSession()
	}

	return map[string]interface{}{
		"wsl":        wslStatus,
		"wslRunning": wslRunning,
		"openclaw":   openclawStatus,
	}
}

// OpenURL 在默认浏览器中打开链接。
func (a *App) OpenURL(url string) {
	wailsRuntime.BrowserOpenURL(a.ctx, url)
}

// WSL 代理方法。
func (a *App) CheckWSL() wsl.WSLInfo {
	return a.wslMgr.CheckWSL()
}

func (a *App) CheckWSLRunning() bool {
	return a.wslMgr.CheckWSLRunning()
}

func (a *App) StopWSL() map[string]interface{} {
	return a.wslMgr.StopWSL()
}

func (a *App) RunWSL() map[string]interface{} {
	return a.wslMgr.RunWSL()
}

func (a *App) RunWSLAsync() {
	a.wslMgr.RunWSLAsync()
}

func (a *App) InstallWSL() map[string]interface{} {
	return a.wslMgr.InstallWSL()
}

func (a *App) InstallUbuntu() {
	go a.wslMgr.InstallUbuntu()
}

// OpenClaw 代理方法。
func (a *App) CheckOpenClaw() wsl.OpenClawStatus {
	return a.openclawMgr.CheckOpenClaw()
}

func (a *App) GetGatewayStatus() map[string]interface{} {
	return a.openclawMgr.GetGatewayStatus()
}

func (a *App) StartGateway() map[string]interface{} {
	result := a.openclawMgr.StartGateway()
	if needsRestart, _ := result["needsRestart"].(bool); needsRestart {
		a.wslMgr.StopWSL()
		result["error"] = "已自动启用 systemd 并重启 WSL，请重新打开 WSL 终端后再点击启动"
	}
	return result
}

func (a *App) StopGateway() map[string]interface{} {
	return a.openclawMgr.StopGateway()
}

func (a *App) RestartGateway() map[string]interface{} {
	return a.openclawMgr.RestartGateway()
}

func (a *App) InstallOpenClaw() {
	a.openclawMgr.InstallOpenClaw()
}

func (a *App) UninstallOpenClaw() {
	a.openclawMgr.UninstallOpenClaw()
}

// Dashboard 代理方法。
func (a *App) CheckDashboard() dashboard.DashboardStatus {
	return a.dashboardMgr.CheckDashboard()
}

// GetModelsConfig 获取模型配置。
func (a *App) GetModelsConfig() map[string]interface{} {
	config, err := a.modelsMgr.GetModelsConfig()
	if err != nil {
		return map[string]interface{}{
			"mode":      "merge",
			"providers": map[string]interface{}{},
		}
	}
	return config
}

// SaveModelsConfig 保存模型配置。
func (a *App) SaveModelsConfig(modelsConfig map[string]interface{}) map[string]interface{} {
	err := a.modelsMgr.SaveModelsConfig(modelsConfig)
	if err != nil {
		return map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
		}
	}
	return map[string]interface{}{
		"ok": true,
	}
}

// GetProviderCatalog 拉取 provider 的官方模型目录。
func (a *App) GetProviderCatalog(provider string, baseURL string, apiKey string) map[string]interface{} {
	catalog, err := a.modelsMgr.GetProviderCatalog(provider, baseURL, apiKey)
	if err != nil {
		return map[string]interface{}{
			"ok":       false,
			"provider": provider,
			"baseUrl":  catalog.BaseURL,
			"api":      catalog.API,
			"docsUrl":  catalog.DocsURL,
			"models":   []models.ProviderCatalogItem{},
			"error":    err.Error(),
		}
	}

	return map[string]interface{}{
		"ok":       true,
		"provider": catalog.Provider,
		"baseUrl":  catalog.BaseURL,
		"api":      catalog.API,
		"docsUrl":  catalog.DocsURL,
		"models":   catalog.Models,
	}
}

// Plugins 代理方法。
func (a *App) GetPlugins() ([]plugins.Plugin, error) {
	return a.pluginsMgr.GetPlugins()
}

func (a *App) GetPluginDetail(id string) (string, error) {
	return a.pluginsMgr.GetPluginDetail(id)
}

func (a *App) EnablePlugin(id string) error {
	return a.pluginsMgr.EnablePlugin(id)
}

func (a *App) DisablePlugin(id string) error {
	return a.pluginsMgr.DisablePlugin(id)
}

func (a *App) InstallPlugin(pluginName string) error {
	return a.pluginsMgr.InstallPlugin(pluginName)
}

func (a *App) UninstallPlugin(pluginId string) error {
	return a.pluginsMgr.UninstallPlugin(pluginId)
}

func (a *App) InstallCustomPlugin(command string) error {
	return a.pluginsMgr.InstallCustomPlugin(command)
}

func (a *App) GetFeishuConfig() map[string]interface{} {
	config, err := a.pluginsMgr.GetFeishuConfig()
	if err != nil {
		return map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
		}
	}

	return map[string]interface{}{
		"ok":                true,
		"enabled":           config.Enabled,
		"accountId":         config.AccountID,
		"appId":             config.AppID,
		"appSecret":         config.AppSecret,
		"botName":           config.BotName,
		"domain":            config.Domain,
		"connectionMode":    config.ConnectionMode,
		"dmPolicy":          config.DMPolicy,
		"verificationToken": config.VerificationToken,
	}
}

func (a *App) SaveFeishuConfig(feishuConfig map[string]interface{}) map[string]interface{} {
	config := plugins.FeishuConfig{
		Enabled:           true,
		AccountID:         "main",
		Domain:            "feishu",
		ConnectionMode:    "websocket",
		DMPolicy:          "pairing",
		VerificationToken: "",
	}

	if value, ok := feishuConfig["enabled"].(bool); ok {
		config.Enabled = value
	}
	if value, ok := feishuConfig["accountId"].(string); ok {
		config.AccountID = value
	}
	if value, ok := feishuConfig["appId"].(string); ok {
		config.AppID = value
	}
	if value, ok := feishuConfig["appSecret"].(string); ok {
		config.AppSecret = value
	}
	if value, ok := feishuConfig["botName"].(string); ok {
		config.BotName = value
	}
	if value, ok := feishuConfig["domain"].(string); ok {
		config.Domain = value
	}
	if value, ok := feishuConfig["connectionMode"].(string); ok {
		config.ConnectionMode = value
	}
	if value, ok := feishuConfig["dmPolicy"].(string); ok {
		config.DMPolicy = value
	}
	if value, ok := feishuConfig["verificationToken"].(string); ok {
		config.VerificationToken = value
	}

	if err := a.pluginsMgr.SaveFeishuConfig(config); err != nil {
		return map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
		}
	}

	return map[string]interface{}{
		"ok": true,
	}
}

func (a *App) ToggleFeishuPlugin(enabled bool) map[string]interface{} {
	if err := a.pluginsMgr.ToggleFeishuPlugin(enabled); err != nil {
		return map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
		}
	}
	return map[string]interface{}{
		"ok": true,
	}
}

// WeixinAuth 微信授权。
func (a *App) WeixinAuth() {
	a.pluginsMgr.WeixinAuth()
}
