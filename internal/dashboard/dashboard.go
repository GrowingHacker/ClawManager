package dashboard

import (
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const defaultPort = 18789

type Manager struct {
	mu          sync.Mutex
	cachedToken string
}

func NewManager() *Manager { return &Manager{} }

type DashboardStatus struct {
	Running bool   `json:"running"`
	Port    int    `json:"port"`
	URL     string `json:"url"`
	FullURL string `json:"fullUrl"`
}

func wslBashFast(command string) (string, error) {
	cmd := exec.Command("wsl", "-e", "bash", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000,
		HideWindow:    true,
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func isPortOpen() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", defaultPort), 500*time.Millisecond)
	if err == nil {
		conn.Close()
		return true
	}
	return false
}

func buildURLs(token string) (string, string) {
	base := fmt.Sprintf("http://localhost:%d", defaultPort)
	if token == "" {
		return base, base
	}
	return base, fmt.Sprintf("%s/#token=%s", base, token)
}

func (m *Manager) getToken() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cachedToken
}

func (m *Manager) setToken(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cachedToken = token
}

// tryTokenFromConfig 从 openclaw 配置文件中读取 gateway.auth.token
func tryTokenFromConfig() string {
	data, err := wslBashFast("cat /root/.openclaw/openclaw.json 2>/dev/null || cat ~/.openclaw/openclaw.json 2>/dev/null")
	if err != nil || data == "" {
		return ""
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(data), &cfg); err == nil {
		for _, path := range [][]string{
			{"gateway", "auth", "token"},
			{"dashboard", "token"},
			{"auth", "token"},
			{"server", "token"},
		} {
			node := interface{}(cfg)
			for _, key := range path {
				mm, ok := node.(map[string]interface{})
				if !ok {
					node = nil
					break
				}
				node = mm[key]
			}
			if s, ok := node.(string); ok && s != "" {
				return s
			}
		}
	}
	re := regexp.MustCompile(`"token"\s*:\s*"([^"]+)"`)
	if ms := re.FindStringSubmatch(data); len(ms) > 1 {
		return ms[1]
	}
	return ""
}

// CheckDashboard 检查 Dashboard 运行状态，返回当前 URL（含 token）
func (m *Manager) CheckDashboard() DashboardStatus {
	running := isPortOpen()
	token := m.getToken()

	if running && token == "" {
		if t := tryTokenFromConfig(); t != "" {
			m.setToken(t)
			token = t
		}
	}
	if !running {
		m.setToken("")
		token = ""
	}

	baseURL, fullURL := buildURLs(token)
	return DashboardStatus{
		Running: running,
		Port:    defaultPort,
		URL:     baseURL,
		FullURL: fullURL,
	}
}
