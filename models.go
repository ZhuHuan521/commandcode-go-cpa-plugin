package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// defaultModelEntries mirrors commandcode-proxy-master's hardcoded catalog.
func defaultModelEntries() []ModelEntry {
	return []ModelEntry{
		{Alias: "claude-sonnet-4-6", Name: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6"},
		{Alias: "claude-opus-4-8", Name: "claude-opus-4-8", DisplayName: "Claude Opus 4.8"},
		{Alias: "claude-opus-4-7", Name: "claude-opus-4-7", DisplayName: "Claude Opus 4.7"},
		{Alias: "claude-haiku-4-5-20251001", Name: "claude-haiku-4-5-20251001", DisplayName: "Claude Haiku 4.5"},
		{Alias: "gpt-5.5", Name: "gpt-5.5", DisplayName: "GPT-5.5"},
		{Alias: "gpt-5.4", Name: "gpt-5.4", DisplayName: "GPT-5.4"},
		{Alias: "gpt-5.4-mini", Name: "gpt-5.4-mini", DisplayName: "GPT-5.4 Mini"},
		{Alias: "gpt-5.3-codex", Name: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex"},
		{Alias: "deepseek/deepseek-v4-pro", Name: "deepseek/deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro"},
		{Alias: "deepseek/deepseek-v4-flash", Name: "deepseek/deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash"},
		{Alias: "moonshotai/Kimi-K2.6", Name: "moonshotai/Kimi-K2.6", DisplayName: "Kimi K2.6"},
		{Alias: "moonshotai/Kimi-K2.5", Name: "moonshotai/Kimi-K2.5", DisplayName: "Kimi K2.5"},
		{Alias: "zai-org/GLM-5.1", Name: "zai-org/GLM-5.1", DisplayName: "GLM 5.1"},
		{Alias: "zai-org/GLM-5", Name: "zai-org/GLM-5", DisplayName: "GLM 5"},
		{Alias: "MiniMaxAI/MiniMax-M3", Name: "MiniMaxAI/MiniMax-M3", DisplayName: "MiniMax M3"},
		{Alias: "MiniMaxAI/MiniMax-M2.7", Name: "MiniMaxAI/MiniMax-M2.7", DisplayName: "MiniMax M2.7"},
		{Alias: "MiniMaxAI/MiniMax-M2.5", Name: "MiniMaxAI/MiniMax-M2.5", DisplayName: "MiniMax M2.5"},
		{Alias: "Qwen/Qwen3.6-Max-Preview", Name: "Qwen/Qwen3.6-Max-Preview", DisplayName: "Qwen 3.6 Max Preview"},
		{Alias: "Qwen/Qwen3.6-Plus", Name: "Qwen/Qwen3.6-Plus", DisplayName: "Qwen 3.6 Plus"},
		{Alias: "Qwen/Qwen3.7-Max", Name: "Qwen/Qwen3.7-Max", DisplayName: "Qwen 3.7 Max"},
		{Alias: "stepfun/Step-3.7-Flash", Name: "stepfun/Step-3.7-Flash", DisplayName: "Step 3.7 Flash"},
		{Alias: "stepfun/Step-3.5-Flash", Name: "stepfun/Step-3.5-Flash", DisplayName: "Step 3.5 Flash"},
		{Alias: "xiaomi/mimo-v2.5-pro", Name: "xiaomi/mimo-v2.5-pro", DisplayName: "MiMo V2.5 Pro"},
		{Alias: "xiaomi/mimo-v2.5", Name: "xiaomi/mimo-v2.5", DisplayName: "MiMo V2.5"},
		{Alias: "google/gemini-3.5-flash", Name: "google/gemini-3.5-flash", DisplayName: "Gemini 3.5 Flash"},
		{Alias: "google/gemini-3.1-flash-lite", Name: "google/gemini-3.1-flash-lite", DisplayName: "Gemini 3.1 Flash Lite"},
	}
}

// ModelProvider advertises static + dynamically fetched model metadata.
type ModelProvider struct {
	cfg *pluginConfig

	mu      sync.RWMutex
	dynamic []ModelEntry
	fetched time.Time
	started bool
}

func NewModelProvider(cfg *pluginConfig) *ModelProvider {
	return &ModelProvider{cfg: cfg}
}

func (p *ModelProvider) startRefresh() {
	if p == nil || p.cfg == nil || !p.cfg.useProviderModels() {
		return
	}
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()
	go func() {
		for {
			_ = p.refreshHTTP()
			time.Sleep(p.cfg.modelRefreshInterval())
		}
	}()
}

func (p *ModelProvider) refreshHTTP() error {
	members := p.cfg.members(pluginapi.ExecutorRequest{})
	if len(members) == 0 {
		return nil
	}
	client := &http.Client{Timeout: 10 * time.Second}
	body, err := p.fetchModelsHTTP(client, strings.TrimSpace(members[0].Key))
	if err != nil {
		return err
	}
	entries, err := parseProviderModels(body)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		p.mu.Lock()
		p.dynamic = entries
		p.fetched = time.Now()
		p.mu.Unlock()
	}
	return nil
}

func (p *ModelProvider) fetchModelsHTTP(client *http.Client, apiKey string) ([]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, p.cfg.baseURL()+"/provider/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-command-code-version", p.cfg.protocolVersion())
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

func parseProviderModels(body []byte) ([]ModelEntry, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	entries := make([]ModelEntry, 0, len(out.Data))
	for _, model := range out.Data {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		entries = append(entries, ModelEntry{Alias: id, Name: id, DisplayName: id})
	}
	return entries, nil
}

func (p *ModelProvider) refreshWithHostClient(client pluginapi.HostHTTPClient, apiKey string) {
	if client == nil || strings.TrimSpace(apiKey) == "" {
		return
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	headers.Set("x-cli-environment", "production")
	headers.Set("x-command-code-version", p.cfg.protocolVersion())
	resp, err := client.Do(context.Background(), pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     p.cfg.baseURL() + "/provider/v1/models",
		Headers: headers,
	})
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	entries, errParse := parseProviderModels(resp.Body)
	if errParse != nil || len(entries) == 0 {
		return
	}
	p.mu.Lock()
	p.dynamic = entries
	p.fetched = time.Now()
	p.mu.Unlock()
}

func (p *ModelProvider) StaticModels(_ context.Context, _ pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{Provider: Provider, Models: p.models()}, nil
}

func (p *ModelProvider) ModelsForAuth(_ context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	key := strings.TrimSpace(req.Attributes["api_key"])
	ms := p.cfg.members(pluginapi.ExecutorRequest{AuthAttributes: req.Attributes, AuthMetadata: req.Metadata})
	if len(ms) > 0 {
		key = strings.TrimSpace(ms[0].Key)
	}
	if key != "" {
		p.refreshWithHostClient(req.HTTPClient, key)
	}
	return pluginapi.ModelResponse{Provider: Provider, Models: p.models()}, nil
}

func (p *ModelProvider) models() []pluginapi.ModelInfo {
	p.mu.RLock()
	entries := append([]ModelEntry(nil), p.cfg.effectiveModels()...)
	seen := make(map[string]struct{}, len(entries)*2)
	for _, entry := range p.dynamic {
		key := normalizeModel(entry.Name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, entry)
	}
	p.mu.RUnlock()

	models := make([]pluginapi.ModelInfo, 0, len(entries))
	ids := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = strings.TrimSpace(entry.Alias)
		}
		if name == "" {
			continue
		}
		id := Provider + "/" + name
		if _, exists := ids[id]; exists {
			continue
		}
		ids[id] = struct{}{}
		display := strings.TrimSpace(entry.DisplayName)
		if display == "" {
			display = name
		}
		models = append(models, pluginapi.ModelInfo{
			ID:                         id,
			Object:                     "model",
			OwnedBy:                    "commandcode",
			Type:                       "chat",
			DisplayName:                display + " via CommandCode",
			Name:                       id,
			Description:                display + " via CommandCode",
			SupportedGenerationMethods: []string{"chatCompletions"},
			SupportedInputModalities:   []string{"text", "image"},
			SupportedOutputModalities:  []string{"text"},
			SupportedParameters:        []string{"temperature", "top_p", "max_tokens", "stop", "tools", "reasoning_effort"},
			Thinking: &pluginapi.ThinkingSupport{
				DynamicAllowed: true,
				Levels:         []string{"none", "auto", "low", "medium", "high", "max"},
			},
		})
	}
	return models
}
