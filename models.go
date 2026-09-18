package plugin

import (
	"context"
	"encoding/json"
	"fmt"
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

	mu       sync.RWMutex
	dynamic  []ModelEntry
	fetched  time.Time
	started  bool
	stop     chan struct{}
	stopOnce sync.Once
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
	p.stop = make(chan struct{})
	stop := p.stop
	p.mu.Unlock()
	go func() {
		defer func() {
			p.mu.Lock()
			p.started = false
			p.mu.Unlock()
		}()
		for {
			_ = p.refreshHTTP()
			timer := time.NewTimer(p.cfg.modelRefreshInterval())
			select {
			case <-stop:
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
		}
	}()
}

// Close stops the background catalog refresh worker. It is safe to call more
// than once and is used by the ABI shutdown/reconfigure path.
func (p *ModelProvider) Close() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		p.mu.RLock()
		stop := p.stop
		p.mu.RUnlock()
		if stop != nil {
			close(stop)
		}
	})
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
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if errRead != nil {
		return nil, errRead
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("commandcode model endpoint returned HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func parseProviderModels(body []byte) ([]ModelEntry, error) {
	var envelope struct {
		Data   []json.RawMessage `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		var rawList []json.RawMessage
		if listErr := json.Unmarshal(body, &rawList); listErr != nil {
			return nil, err
		}
		envelope.Data = rawList
	}
	rawModels := envelope.Data
	if len(rawModels) == 0 {
		rawModels = envelope.Models
	}
	entries := make([]ModelEntry, 0, len(rawModels))
	seen := make(map[string]struct{}, len(rawModels))
	for _, rawModel := range rawModels {
		var object struct {
			ID               string         `json:"id"`
			Name             string         `json:"name"`
			Alias            string         `json:"alias"`
			DisplayName      string         `json:"display_name"`
			ContextLength    int64          `json:"context_length"`
			MaxContextLength int64          `json:"max_context_length"`
			Thinking         *ModelThinking `json:"thinking"`
		}
		id := ""
		if errDecode := json.Unmarshal(rawModel, &object); errDecode == nil {
			id = firstNonEmpty(object.ID, object.Name, object.Alias)
		} else {
			var text string
			if errText := json.Unmarshal(rawModel, &text); errText == nil {
				id = text
			}
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		display := strings.TrimSpace(object.DisplayName)
		if display == "" {
			display = id
		}
		contextLength := object.ContextLength
		if contextLength <= 0 {
			contextLength = object.MaxContextLength
		}
		entries = append(entries, ModelEntry{
			Alias:            id,
			Name:             id,
			DisplayName:      display,
			MaxContextLength: contextLength,
			Thinking:         object.Thinking,
		})
	}
	return entries, nil
}

func (p *ModelProvider) refreshWithHostClient(ctx context.Context, client pluginapi.HostHTTPClient, apiKey, authBaseURL string) {
	if client == nil || strings.TrimSpace(apiKey) == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	headers.Set("x-cli-environment", "production")
	headers.Set("x-command-code-version", p.cfg.protocolVersion())
	baseURL := normalizeCommandCodeBaseURL(authBaseURL)
	if baseURL == "" {
		baseURL = p.cfg.baseURL()
	}
	resp, err := client.Do(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     baseURL + "/provider/v1/models",
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
	return pluginapi.ModelResponse{Provider: Provider, Models: p.modelsWithIDs()}, nil
}

func (p *ModelProvider) ModelsForAuth(ctx context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	if p == nil {
		return pluginapi.ModelResponse{Provider: Provider}, nil
	}
	key := strings.TrimSpace(req.Attributes["api_key"])
	var ms []APIKeyEntry
	if p.cfg != nil {
		ms = p.cfg.members(pluginapi.ExecutorRequest{AuthAttributes: req.Attributes, AuthMetadata: req.Metadata})
	}
	if len(ms) > 0 {
		key = strings.TrimSpace(ms[0].Key)
	}
	if key != "" && p.cfg != nil {
		authBaseURL := strings.TrimSpace(req.Attributes["base_url"])
		if authBaseURL == "" && req.Metadata != nil {
			authBaseURL = strings.TrimSpace(asString(req.Metadata["base_url"]))
		}
		p.refreshWithHostClient(ctx, req.HTTPClient, key, authBaseURL)
	}
	return pluginapi.ModelResponse{
		Provider:   Provider,
		Models:     p.modelsWithEntries(p.catalogEntriesForAuth(req.Metadata)),
		AuthUpdate: commandCodeAuthUpdate(req),
	}, nil
}

func normalizeCommandCodeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return ""
	}
	lower := strings.ToLower(base)
	for _, suffix := range []string{
		"/provider/v1/chat/completions",
		"/provider/v1/responses",
		"/provider/v1/models",
		"/provider/v1",
	} {
		if strings.HasSuffix(lower, suffix) {
			return strings.TrimRight(base[:len(base)-len(suffix)], "/")
		}
	}
	return base
}

// commandCodeAuthUpdate repairs the auth classification applied by older host
// file synthesis. Command Code records contain API keys, but the generic file
// path historically stamped plugin-parsed records as OAuth before model
// discovery. Returning an update lets the host persist the correct kind while
// retaining the original source JSON (including a multi-key api_keys pool).
func commandCodeAuthUpdate(req pluginapi.AuthModelRequest) pluginapi.AuthData {
	attrs := cloneStringMap(req.Attributes)
	currentKind := firstStringValue(attrs, "auth_kind")
	if currentKind == "" {
		currentKind = firstAnyStringValue(req.Metadata, "auth_kind")
	}
	normalizedKind := strings.ToLower(strings.TrimSpace(currentKind))
	if normalizedKind == "" || normalizedKind == "apikey" || normalizedKind == "api_key" || normalizedKind == "api-key" {
		return pluginapi.AuthData{}
	}
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs["auth_kind"] = "apikey"
	return pluginapi.AuthData{
		Provider:    Provider,
		ID:          req.AuthID,
		StorageJSON: append([]byte(nil), req.StorageJSON...),
		Attributes:  attrs,
	}
}

func (p *ModelProvider) models() []pluginapi.ModelInfo {
	return p.modelsWithIDs()
}

func (p *ModelProvider) modelsForAuth() []pluginapi.ModelInfo {
	return p.modelsWithIDs()
}

// catalogEntriesForAuth merges model declarations carried by one auth file into
// the plugin-wide catalog. Auth files may contain aliases that are valid only
// for one credential, so those entries must be registered on that auth rather
// than silently discarded in favor of the global config.
func (p *ModelProvider) catalogEntriesForAuth(metadata map[string]any) []ModelEntry {
	authEntries := authModelEntries(metadata)
	if len(authEntries) == 0 {
		return p.catalogEntries()
	}

	entries := make([]ModelEntry, 0, len(authEntries))
	seen := make(map[string]struct{}, len(authEntries))
	for _, entry := range authEntries {
		key := modelEntryKey(entry)
		if key == "" || hasModelEntryKey(seen, key) {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, entry)
	}
	for _, entry := range p.catalogEntries() {
		key := modelEntryKey(entry)
		if key == "" || hasModelEntryKey(seen, key) {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, entry)
	}
	return entries
}

func hasModelEntryKey(seen map[string]struct{}, key string) bool {
	_, exists := seen[key]
	return exists
}

func authModelEntries(metadata map[string]any) []ModelEntry {
	if len(metadata) == 0 {
		return nil
	}
	values := asSlice(metadata["models"])
	if len(values) == 0 {
		if typed, ok := metadata["models"].([]map[string]any); ok {
			values = make([]any, len(typed))
			for index := range typed {
				values[index] = typed[index]
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	entries := make([]ModelEntry, 0, len(values))
	for _, value := range values {
		item := asMap(value)
		alias := strings.TrimSpace(asString(item["alias"]))
		name := strings.TrimSpace(asString(item["name"]))
		if name == "" {
			name = alias
		}
		if name == "" {
			continue
		}
		entries = append(entries, ModelEntry{
			Alias:            alias,
			Name:             name,
			DisplayName:      strings.TrimSpace(asString(item["display_name"])),
			Priority:         int(asNumber(item["priority"])),
			MaxContextLength: modelContextLength(item),
			Thinking:         modelThinkingFromAny(item["thinking"]),
			TestModel:        strings.TrimSpace(firstNonEmpty(asString(item["test_model"]), asString(item["test-model"]), asString(item["testModel"]))),
		})
	}
	return entries
}

func modelContextLength(item map[string]any) int64 {
	for _, key := range []string{"max_context_length", "max-context-length", "maxContextLength", "context_length"} {
		if value := int64(asNumber(item[key])); value > 0 {
			return value
		}
	}
	return 0
}

func modelThinkingFromAny(raw any) *ModelThinking {
	item := asMap(raw)
	if len(item) == 0 {
		return nil
	}
	thinking := &ModelThinking{
		Min:            int(asNumber(item["min"])),
		Max:            int(asNumber(item["max"])),
		ZeroAllowed:    asBool(item["zero_allowed"]) || asBool(item["zeroAllowed"]),
		DynamicAllowed: asBool(item["dynamic_allowed"]) || asBool(item["dynamicAllowed"]),
	}
	if values, ok := item["levels"].([]any); ok {
		for _, value := range values {
			if level := strings.TrimSpace(asString(value)); level != "" {
				thinking.Levels = append(thinking.Levels, level)
			}
		}
	}
	if len(thinking.Levels) == 0 && thinking.Min == 0 && thinking.Max == 0 && !thinking.ZeroAllowed && !thinking.DynamicAllowed {
		return nil
	}
	return thinking
}

// catalogEntries returns configured entries first, then the latest endpoint
// snapshot. A configured entry wins on a case-insensitive model-name collision.
func (p *ModelProvider) catalogEntries() []ModelEntry {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	dynamic := append([]ModelEntry(nil), p.dynamic...)
	p.mu.RUnlock()
	var entries []ModelEntry
	if p.cfg != nil {
		entries = append(entries, p.cfg.effectiveModels()...)
	}
	seen := make(map[string]struct{}, len(entries)+len(dynamic))
	for _, entry := range entries {
		if key := modelEntryKey(entry); key != "" {
			seen[key] = struct{}{}
		}
	}
	for _, entry := range dynamic {
		key := modelEntryKey(entry)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, entry)
	}
	return entries
}

func modelEntryName(entry ModelEntry) string {
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = strings.TrimSpace(entry.Alias)
	}
	return name
}

// modelEntryKey is used for catalog de-duplication and preserves vendor
// namespaces, so vendor-a/foo and vendor-b/foo remain distinct entries.
func modelEntryKey(entry ModelEntry) string {
	return strings.ToLower(strings.TrimSpace(modelEntryName(entry)))
}

func (p *ModelProvider) owns(model string) bool {
	key := normalizeModel(model)
	if key == "" {
		return false
	}
	for _, entry := range p.catalogEntries() {
		if key == normalizeModel(modelEntryName(entry)) || key == normalizeModel(entry.Alias) {
			return true
		}
	}
	return false
}

// publicModelName resolves an upstream name to the single client-facing ID
// advertised in the model catalog, matching the host's configured-provider
// alias behavior.
func (p *ModelProvider) publicModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	key := normalizeModel(model)
	for _, entry := range p.catalogEntries() {
		if key != normalizeModel(modelEntryName(entry)) && key != normalizeModel(entry.Alias) {
			continue
		}
		if alias := strings.TrimSpace(entry.Alias); alias != "" {
			return alias
		}
		return modelEntryName(entry)
	}
	return model
}

func (p *ModelProvider) modelsWithIDs() []pluginapi.ModelInfo {
	return p.modelsWithEntries(p.catalogEntries())
}

func (p *ModelProvider) modelsWithEntries(entries []ModelEntry) []pluginapi.ModelInfo {
	models := make([]pluginapi.ModelInfo, 0, len(entries)*2)
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := modelEntryName(entry)
		if name == "" {
			continue
		}
		publicID := strings.TrimSpace(entry.Alias)
		if publicID == "" {
			publicID = name
		}
		key := strings.ToLower(publicID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		display := strings.TrimSpace(entry.DisplayName)
		if display == "" {
			display = name
		}
		info := pluginapi.ModelInfo{
			ID:                         publicID,
			Object:                     "model",
			OwnedBy:                    "commandcode",
			Type:                       "chat",
			DisplayName:                display,
			Name:                       publicID,
			Description:                display,
			ContextLength:              entry.contextLength(),
			SupportedGenerationMethods: []string{"chatCompletions"},
			SupportedInputModalities:   []string{"text", "image"},
			SupportedOutputModalities:  []string{"text"},
			SupportedParameters:        []string{"temperature", "top_p", "max_tokens", "stop", "tools", "reasoning_effort"},
		}
		info.Thinking = modelThinkingSupport(entry.Thinking)
		models = append(models, info)
	}
	return models
}

func modelThinkingSupport(thinking *ModelThinking) *pluginapi.ThinkingSupport {
	if thinking == nil {
		return nil
	}
	levels := make([]string, 0, len(thinking.Levels))
	seen := make(map[string]struct{}, len(thinking.Levels))
	for _, raw := range thinking.Levels {
		level := strings.ToLower(strings.TrimSpace(raw))
		if level == "" {
			continue
		}
		if _, exists := seen[level]; exists {
			continue
		}
		seen[level] = struct{}{}
		levels = append(levels, level)
	}
	zeroAllowed := thinking.ZeroAllowed
	dynamicAllowed := thinking.DynamicAllowed
	for _, level := range levels {
		if level == "none" {
			zeroAllowed = true
		}
		if level == "auto" {
			dynamicAllowed = true
		}
	}
	return &pluginapi.ThinkingSupport{
		Min:            thinking.Min,
		Max:            thinking.Max,
		ZeroAllowed:    zeroAllowed,
		DynamicAllowed: dynamicAllowed,
		Levels:         levels,
	}
}
