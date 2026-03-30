package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/html"
)

// Manager 管理模型配置与模型目录发现。
type Manager struct {
	ctx context.Context
}

type providerSpec struct {
	DefaultBaseURL string
	API            string
	DocsURL        string
}

// ProviderCatalogItem 表示一个可选模型。
type ProviderCatalogItem struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Reasoning     bool     `json:"reasoning"`
	Input         []string `json:"input"`
	ContextWindow int      `json:"contextWindow"`
	MaxTokens     int      `json:"maxTokens"`
}

// ProviderCatalogResult 表示某个 provider 的模型目录结果。
type ProviderCatalogResult struct {
	Provider string                `json:"provider"`
	BaseURL  string                `json:"baseUrl"`
	API      string                `json:"api"`
	DocsURL  string                `json:"docsUrl"`
	Models   []ProviderCatalogItem `json:"models"`
}

var providerSpecs = map[string]providerSpec{
	"nvidia": {
		DefaultBaseURL: "https://integrate.api.nvidia.com/v1",
		API:            "openai-completions",
		DocsURL:        "https://docs.api.nvidia.com/nim/reference/llm-apis",
	},
	"openai": {
		DefaultBaseURL: "https://api.openai.com/v1",
		API:            "openai-completions",
		DocsURL:        "https://platform.openai.com/docs/api-reference/models/list",
	},
	"anthropic": {
		DefaultBaseURL: "https://api.anthropic.com",
		API:            "anthropic-messages",
		DocsURL:        "https://docs.anthropic.com/en/api/models-list",
	},
	"google": {
		DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta",
		API:            "google-generative-ai",
		DocsURL:        "https://ai.google.dev/api/rest/generativelanguage/models/list",
	},
	"deepseek": {
		DefaultBaseURL: "https://api.deepseek.com",
		API:            "openai-completions",
		DocsURL:        "https://api-docs.deepseek.com/api/list-models",
	},
	"other": {
		DefaultBaseURL: "",
		API:            "openai-completions",
		DocsURL:        "",
	},
}

// NewManager 实例化模型管理器。
func NewManager() *Manager {
	return &Manager{}
}

// SetContext 设置 Wails 上下文。
func (m *Manager) SetContext(ctx context.Context) {
	m.ctx = ctx
}

// wslBashFast 快速执行命令，不解析用户配置文件。
func wslBashFast(command string) (string, error) {
	cmd := exec.Command("wsl", "-e", "bash", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// GetModelsConfig 获取模型配置。
func (m *Manager) GetModelsConfig() (map[string]interface{}, error) {
	cmd := "cat /root/.openclaw/openclaw.json 2>/dev/null || echo '{}'"
	output, err := wslBashFast(cmd)
	if err != nil {
		return nil, err
	}

	var config map[string]interface{}
	if err := json.Unmarshal([]byte(output), &config); err != nil {
		return nil, err
	}

	modelsConfig, ok := config["models"].(map[string]interface{})
	if !ok {
		modelsConfig = map[string]interface{}{
			"mode":      "merge",
			"providers": map[string]interface{}{},
		}
	}

	return modelsConfig, nil
}

// SaveModelsConfig 保存模型配置。
func (m *Manager) SaveModelsConfig(modelsConfig map[string]interface{}) error {
	cmd := "cat /root/.openclaw/openclaw.json 2>/dev/null || echo '{}'"
	output, err := wslBashFast(cmd)
	if err != nil {
		return err
	}

	var config map[string]interface{}
	if err := json.Unmarshal([]byte(output), &config); err != nil {
		return err
	}

	config["models"] = modelsConfig

	updatedConfig, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	cmd = fmt.Sprintf("mkdir -p /root/.openclaw && cat > /root/.openclaw/openclaw.json << 'EOF'\n%s\nEOF", string(updatedConfig))
	_, err = wslBashFast(cmd)
	return err
}

// GetProviderCatalog 根据 provider 拉取官方模型目录。
func (m *Manager) GetProviderCatalog(provider, baseURL, apiKey string) (ProviderCatalogResult, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))

	spec, ok := providerSpecs[provider]
	if !ok {
		return ProviderCatalogResult{}, fmt.Errorf("unsupported provider: %s", provider)
	}

	normalizedBaseURL := normalizeDefaultBaseURL(provider, baseURL)
	result := ProviderCatalogResult{
		Provider: provider,
		BaseURL:  normalizedBaseURL,
		API:      spec.API,
		DocsURL:  spec.DocsURL,
		Models:   []ProviderCatalogItem{},
	}

	switch provider {
	case "nvidia":
		models, err := m.fetchNVIDIAModels(normalizedBaseURL)
		if err != nil {
			return result, err
		}
		result.Models = models
	case "openai":
		models, err := m.fetchOpenAIModels(normalizedBaseURL, apiKey)
		if err != nil {
			return result, err
		}
		result.Models = models
	case "anthropic":
		models, err := m.fetchAnthropicModels(normalizedBaseURL, apiKey)
		if err != nil {
			return result, err
		}
		result.Models = models
	case "google":
		models, err := m.fetchGoogleModels(normalizedBaseURL, apiKey)
		if err != nil {
			return result, err
		}
		result.Models = models
	case "deepseek":
		models, err := m.fetchDeepSeekModels(normalizedBaseURL, apiKey)
		if err != nil {
			return result, err
		}
		result.Models = models
	case "other":
		// other 保持手动模式。
	default:
		return result, fmt.Errorf("provider catalog is not implemented for: %s", provider)
	}

	sort.Slice(result.Models, func(i, j int) bool {
		return strings.ToLower(result.Models[i].Name) < strings.ToLower(result.Models[j].Name)
	})

	return result, nil
}

func (m *Manager) fetchNVIDIAModels(baseURL string) ([]ProviderCatalogItem, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, providerSpecs["nvidia"].DocsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("request failed: %s", resp.Status)
	}

	models := extractNVIDIAModelsFromHTML(body)
	if len(models) == 0 {
		return nil, fmt.Errorf("未能从 NVIDIA 官方文档解析到模型列表")
	}

	return models, nil
}

func (m *Manager) fetchOpenAIModels(baseURL, apiKey string) ([]ProviderCatalogItem, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("OpenAI 官方模型目录需要 API Key")
	}

	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}

	if err := m.doJSONRequest(openAIModelsURL(baseURL), map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(apiKey),
	}, &payload); err != nil {
		return nil, err
	}

	models := make([]ProviderCatalogItem, 0, len(payload.Data))
	for _, item := range payload.Data {
		if !isOpenAITextModel(item.ID) {
			continue
		}
		models = append(models, ProviderCatalogItem{
			ID:            item.ID,
			Name:          item.ID,
			Reasoning:     inferReasoning(item.ID, item.ID),
			Input:         []string{"text"},
			ContextWindow: 131072,
			MaxTokens:     16384,
		})
	}

	return models, nil
}

func (m *Manager) fetchAnthropicModels(baseURL, apiKey string) ([]ProviderCatalogItem, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("Anthropic 官方模型目录需要 API Key")
	}

	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}

	if err := m.doJSONRequest(anthropicModelsURL(baseURL), map[string]string{
		"x-api-key":         strings.TrimSpace(apiKey),
		"anthropic-version": "2023-06-01",
	}, &payload); err != nil {
		return nil, err
	}

	models := make([]ProviderCatalogItem, 0, len(payload.Data))
	for _, item := range payload.Data {
		name := strings.TrimSpace(item.DisplayName)
		if name == "" {
			name = item.ID
		}
		models = append(models, ProviderCatalogItem{
			ID:            item.ID,
			Name:          name,
			Reasoning:     true,
			Input:         []string{"text"},
			ContextWindow: 200000,
			MaxTokens:     8192,
		})
	}

	return models, nil
}

func (m *Manager) fetchGoogleModels(baseURL, apiKey string) ([]ProviderCatalogItem, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("Google 官方模型目录需要 API Key")
	}

	type googleModel struct {
		Name                       string   `json:"name"`
		BaseModelID                string   `json:"baseModelId"`
		DisplayName                string   `json:"displayName"`
		InputTokenLimit            int      `json:"inputTokenLimit"`
		OutputTokenLimit           int      `json:"outputTokenLimit"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	}

	var all []googleModel
	pageToken := ""

	for {
		endpoint, err := googleModelsURL(baseURL, strings.TrimSpace(apiKey), pageToken)
		if err != nil {
			return nil, err
		}

		var payload struct {
			Models        []googleModel `json:"models"`
			NextPageToken string        `json:"nextPageToken"`
		}

		if err := m.doJSONRequest(endpoint, nil, &payload); err != nil {
			return nil, err
		}

		all = append(all, payload.Models...)
		if strings.TrimSpace(payload.NextPageToken) == "" {
			break
		}
		pageToken = payload.NextPageToken
	}

	seen := map[string]struct{}{}
	models := make([]ProviderCatalogItem, 0, len(all))

	for _, item := range all {
		if !supportsGenerateContent(item.SupportedGenerationMethods) {
			continue
		}

		id := strings.TrimSpace(item.BaseModelID)
		if id == "" {
			id = strings.TrimPrefix(strings.TrimSpace(item.Name), "models/")
		}
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}

		name := strings.TrimSpace(item.DisplayName)
		if name == "" {
			name = id
		}

		contextWindow := item.InputTokenLimit
		if contextWindow <= 0 {
			contextWindow = 131072
		}
		maxTokens := item.OutputTokenLimit
		if maxTokens <= 0 {
			maxTokens = 8192
		}

		models = append(models, ProviderCatalogItem{
			ID:            id,
			Name:          name,
			Reasoning:     inferReasoning(id, name),
			Input:         []string{"text"},
			ContextWindow: contextWindow,
			MaxTokens:     maxTokens,
		})
	}

	return models, nil
}

func (m *Manager) fetchDeepSeekModels(baseURL, apiKey string) ([]ProviderCatalogItem, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("DeepSeek 官方模型目录需要 API Key")
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := m.doJSONRequest(deepSeekModelsURL(baseURL), map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(apiKey),
	}, &payload); err != nil {
		return nil, err
	}

	models := make([]ProviderCatalogItem, 0, len(payload.Data))
	for _, item := range payload.Data {
		contextWindow := 128000
		maxTokens := 8192
		reasoning := inferReasoning(item.ID, item.ID)

		if strings.Contains(strings.ToLower(item.ID), "reasoner") {
			maxTokens = 64000
			reasoning = true
		}

		models = append(models, ProviderCatalogItem{
			ID:            item.ID,
			Name:          item.ID,
			Reasoning:     reasoning,
			Input:         []string{"text"},
			ContextWindow: contextWindow,
			MaxTokens:     maxTokens,
		})
	}

	return models, nil
}

func (m *Manager) doJSONRequest(endpoint string, headers map[string]string, target interface{}) error {
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "application/json")
	for key, value := range headers {
		if strings.TrimSpace(value) == "" {
			continue
		}
		req.Header.Set(key, value)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("request failed: %s - %s", resp.Status, strings.TrimSpace(string(body)))
	}

	if err := json.Unmarshal(body, target); err != nil {
		return err
	}

	return nil
}

func normalizeDefaultBaseURL(provider, baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL != "" {
		return baseURL
	}
	return providerSpecs[provider].DefaultBaseURL
}

func openAIModelsURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch {
	case strings.HasSuffix(baseURL, "/models"):
		return baseURL
	case strings.HasSuffix(baseURL, "/v1"):
		return baseURL + "/models"
	default:
		return baseURL + "/v1/models"
	}
}

func anthropicModelsURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch {
	case strings.HasSuffix(baseURL, "/models"):
		return baseURL
	case strings.HasSuffix(baseURL, "/v1"):
		return baseURL + "/models"
	default:
		return baseURL + "/v1/models"
	}
}

func deepSeekModelsURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, "/models") {
		return baseURL
	}
	return baseURL + "/models"
}

func googleModelsURL(baseURL, apiKey, pageToken string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch {
	case strings.HasSuffix(baseURL, "/models"):
	case strings.HasSuffix(baseURL, "/v1beta"):
		baseURL += "/models"
	default:
		baseURL += "/v1beta/models"
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}

	query := parsed.Query()
	query.Set("key", apiKey)
	query.Set("pageSize", "1000")
	if strings.TrimSpace(pageToken) != "" {
		query.Set("pageToken", pageToken)
	}
	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}

func supportsGenerateContent(methods []string) bool {
	if len(methods) == 0 {
		return true
	}
	for _, method := range methods {
		switch strings.ToLower(strings.TrimSpace(method)) {
		case "generatecontent", "streamgeneratecontent":
			return true
		}
	}
	return false
}

func isOpenAITextModel(id string) bool {
	lowerID := strings.ToLower(strings.TrimSpace(id))
	if lowerID == "" {
		return false
	}

	excluded := []string{
		"embedding",
		"whisper",
		"transcribe",
		"tts",
		"speech",
		"moderation",
		"image",
		"realtime",
	}
	for _, token := range excluded {
		if strings.Contains(lowerID, token) {
			return false
		}
	}

	return true
}

func inferReasoning(id, name string) bool {
	joined := strings.ToLower(strings.TrimSpace(id + " " + name))
	for _, token := range []string{"reason", "reasoner", "thinking", "think", "r1", "o1", "o3", "o4"} {
		if strings.Contains(joined, token) {
			return true
		}
	}
	return false
}

func extractNVIDIAModelsFromHTML(body []byte) []ProviderCatalogItem {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil
	}

	pattern := regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9._-]*(?:\s*/\s*|\s*/?)([a-z0-9][a-z0-9._:-]*(?:[-_/][a-z0-9][a-z0-9._:-]*)*)$`)
	seen := map[string]struct{}{}
	models := []ProviderCatalogItem{}

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			text := normalizeWhitespace(n.Data)
			if text == "" {
				return
			}
			if !strings.Contains(text, "/") {
				return
			}
			if strings.Contains(strings.ToLower(text), "create ") || strings.Contains(strings.ToLower(text), "request ") || strings.Contains(strings.ToLower(text), "status ") {
				return
			}

			matches := pattern.FindStringSubmatch(text)
			if len(matches) != 2 {
				return
			}

			id := strings.ReplaceAll(text, " / ", "/")
			id = strings.ReplaceAll(id, " /", "/")
			id = strings.ReplaceAll(id, "/ ", "/")
			id = strings.TrimSpace(id)
			lowerID := strings.ToLower(id)

			// 从 id 中提取更友好的名称（取 / 后面的部分）
			name := id
			if idx := strings.Index(id, "/"); idx != -1 && idx < len(id)-1 {
				name = strings.TrimSpace(id[idx+1:])
			}

			if strings.HasPrefix(lowerID, "nvidia/") || strings.HasPrefix(lowerID, "meta/") || strings.HasPrefix(lowerID, "mistralai/") ||
				strings.HasPrefix(lowerID, "qwen/") || strings.HasPrefix(lowerID, "openai/") || strings.HasPrefix(lowerID, "deepseek-ai/") ||
				strings.HasPrefix(lowerID, "google/") || strings.HasPrefix(lowerID, "microsoft/") || strings.HasPrefix(lowerID, "moonshotai/") ||
				strings.HasPrefix(lowerID, "ibm/") || strings.HasPrefix(lowerID, "baai/") || strings.HasPrefix(lowerID, "snowflake/") ||
				strings.HasPrefix(lowerID, "bytedance/") || strings.HasPrefix(lowerID, "ai21labs/") || strings.HasPrefix(lowerID, "aisingapore/") ||
				strings.HasPrefix(lowerID, "abacusai/") || strings.HasPrefix(lowerID, "minimaxai/") || strings.HasPrefix(lowerID, "black-forest-labs/") ||
				strings.HasPrefix(lowerID, "baichuan-inc/") || strings.HasPrefix(lowerID, "gotocompany/") || strings.HasPrefix(lowerID, "igenius/") ||
				strings.HasPrefix(lowerID, "institute-of-science-tokyo/") || strings.HasPrefix(lowerID, "marin/") || strings.HasPrefix(lowerID, "mediatek/") ||
				strings.HasPrefix(lowerID, "opengpt-x/") || strings.HasPrefix(lowerID, "rakuten/") || strings.HasPrefix(lowerID, "seallms/") ||
				strings.HasPrefix(lowerID, "sarvamai/") || strings.HasPrefix(lowerID, "speakleash/") || strings.HasPrefix(lowerID, "stepfun-ai/") ||
				strings.HasPrefix(lowerID, "stockmark/") || strings.HasPrefix(lowerID, "tokyotech-llm/") || strings.HasPrefix(lowerID, "thudm/") ||
				strings.HasPrefix(lowerID, "tiiuae/") || strings.HasPrefix(lowerID, "upstage/") || strings.HasPrefix(lowerID, "utter-project/") ||
				strings.HasPrefix(lowerID, "yentinglin/") || strings.HasPrefix(lowerID, "z-ai/") || strings.HasPrefix(lowerID, "hive/") {
				if isNVIDIATextCandidate(lowerID) {
					if _, ok := seen[lowerID]; !ok {
						seen[lowerID] = struct{}{}
						models = append(models, ProviderCatalogItem{
							ID:            id,
							Name:          name,
							Reasoning:     inferReasoning(id, name),
							Input:         []string{"text"},
							ContextWindow: 131072,
							MaxTokens:     16384,
						})
					}
				}
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(doc)
	return models
}

func normalizeWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func isNVIDIATextCandidate(id string) bool {
	excluded := []string{
		"embed",
		"embedding",
		"rerank",
		"retriev",
		"vision",
		"visual",
		"flux",
		"clip",
		"grounding",
		"detect",
		"translate",
		"search",
		"guardrail",
		"safety-guard",
	}
	for _, token := range excluded {
		if strings.Contains(id, token) {
			return false
		}
	}
	return true
}
