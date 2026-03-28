package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"ClawManager/internal/dashboard"
	"ClawManager/internal/models"
	"ClawManager/internal/openclaw"
	"ClawManager/internal/plugins"
	"ClawManager/internal/wsl"
)

//go:embed all:frontend
var assets embed.FS

func main() {
	wslMgr := wsl.NewManager()
	openclawMgr := openclaw.NewManager(wslMgr)
	wslMgr.SetOpenClawManager(openclawMgr)
	dashboardMgr := dashboard.NewManager()
	pluginsMgr := plugins.NewManager()
	modelsMgr := models.NewManager()

	app := NewApp(wslMgr, openclawMgr, dashboardMgr, pluginsMgr, modelsMgr)

	err := wails.Run(&options.App{
		Title:  "ClawManager",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{
			Theme: windows.Dark,
			CustomTheme: &windows.ThemeSettings{
				// 激活状态：青色边框
				DarkModeTitleBar:  0x1a0d02, // BGR: #020d1a
				DarkModeTitleText: 0xffd400, // BGR: #00d4ff (cyan)
				DarkModeBorder:    0xffd400, // BGR: #00d4ff (cyan)
				// 非激活状态：暗淡
				DarkModeTitleBarInactive:  0x100800,
				DarkModeTitleTextInactive: 0x7a5a2d,
				DarkModeBorderInactive:    0x3a2a10,
			},
		},
		Debug: options.Debug{
			OpenInspectorOnStartup: true,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
