// Package plugin implements a CLIProxyAPI provider plugin for Command Code.
//
// It ports the full commandcode-proxy-master business logic into the plugin
// ABI: deterministic per-key device fingerprints, lifecycle initialization,
// per-key sessions, the CLI request envelope, alpha/generate NDJSON streaming,
// and OpenAI-compatible output normalization. The host still owns the
// Anthropic/Responses endpoint translation; this executor emits standard
// OpenAI chunks so the host's translators can render thinking, tools and usage
// on every route.
package plugin

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	// Provider is the host-facing plugin/provider key.
	Provider = "commandcode"

	// executorFormat is the payload protocol spoken by this executor.
	executorFormat = "openai"
)

// pluginVersion is injected by the release build through ldflags.
var pluginVersion = "1.0.0"

// CommandCodePlugin wires all plugin capabilities behind one instance.
type CommandCodePlugin struct {
	models     *ModelProvider
	router     *Router
	translator *Translator
	executor   *Executor
	auth       *AuthProvider
	cfg        *pluginConfig
}

// Build creates the host-facing descriptor and handler from the plugin YAML.
func Build(configYAML []byte) (pluginapi.Plugin, *CommandCodePlugin) {
	cfg := parseConfig(configYAML)
	p := &CommandCodePlugin{
		models: NewModelProvider(cfg),
		cfg:    cfg,
	}
	p.router = NewRouter(cfg, p.models)
	p.translator = NewTranslator(cfg)
	p.executor = NewExecutor(cfg, p.translator)
	p.auth = NewAuthProvider(cfg)
	p.models.startRefresh()
	return pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             "CommandCode Provider",
			Version:          pluginVersion,
			Author:           "ZhuHuan521",
			GitHubRepository: "https://github.com/ZhuHuan521/commandcode-go-cpa-plugin",
			ConfigFields:     configFields(),
		},
		Capabilities: pluginapi.Capabilities{
			ModelProvider:         p.models,
			AuthProvider:          p.auth,
			ModelRouter:           p.router,
			Executor:              p.executor,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeBoth,
			ExecutorInputFormats:  []string{executorFormat},
			ExecutorOutputFormats: []string{executorFormat},
			RequestTranslator:     p.translator,
			ResponseTranslator:    p.translator,
			UsagePlugin:           p,
		},
	}, p
}

// Close releases background workers owned by the plugin instance.
func (p *CommandCodePlugin) Close() {
	if p == nil || p.models == nil {
		return
	}
	p.models.Close()
}

// Identifier returns the provider key.
func (p *CommandCodePlugin) Identifier() string { return Provider }

// StaticModels advertises commandcode's model catalog.
func (p *CommandCodePlugin) StaticModels(ctx context.Context, req pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return p.models.StaticModels(ctx, req)
}

// ModelsForAuth mirrors static models for per-auth discovery.
func (p *CommandCodePlugin) ModelsForAuth(ctx context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	return p.models.ModelsForAuth(ctx, req)
}

// ParseAuth recognizes commandcode JSON auth files. Keeping the parser in the
// plugin lets the host's normal auth-file synthesizer attach priority, weight,
// cooldown and per-key metadata to the core scheduler.
func (p *CommandCodePlugin) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	return p.auth.ParseAuth(ctx, req)
}

func (p *CommandCodePlugin) StartLogin(ctx context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	return p.auth.StartLogin(ctx, req)
}

func (p *CommandCodePlugin) PollLogin(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return p.auth.PollLogin(ctx, req)
}

func (p *CommandCodePlugin) RefreshAuth(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	return p.auth.RefreshAuth(ctx, req)
}

// RouteModel sends owned model names to this executor.
func (p *CommandCodePlugin) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
	return p.router.RouteModel(ctx, req)
}

// TranslateRequest converts canonical payloads into the Command Code envelope.
func (p *CommandCodePlugin) TranslateRequest(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
	return p.translator.TranslateRequest(ctx, req)
}

// TranslateResponse normalizes Command Code responses into canonical payloads.
func (p *CommandCodePlugin) TranslateResponse(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
	return p.translator.TranslateResponse(ctx, req)
}

// Execute performs a non-streaming upstream call.
func (p *CommandCodePlugin) Execute(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return p.executor.Execute(ctx, req)
}

// ExecuteStream performs a streaming upstream call.
func (p *CommandCodePlugin) ExecuteStream(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	return p.executor.ExecuteStream(ctx, req)
}

// CountTokens estimates tokens locally.
func (p *CommandCodePlugin) CountTokens(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return p.executor.CountTokens(ctx, req)
}

// HttpRequest bridges raw executor HTTP through the plugin transport.
func (p *CommandCodePlugin) HttpRequest(ctx context.Context, req pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	return p.executor.HttpRequest(ctx, req)
}

// HandleUsage receives host usage records after completion.
func (p *CommandCodePlugin) HandleUsage(_ context.Context, _ pluginapi.UsageRecord) {}

var (
	_ pluginapi.ModelProvider      = (*CommandCodePlugin)(nil)
	_ pluginapi.AuthProvider       = (*CommandCodePlugin)(nil)
	_ pluginapi.ModelRouter        = (*CommandCodePlugin)(nil)
	_ pluginapi.RequestTranslator  = (*CommandCodePlugin)(nil)
	_ pluginapi.ResponseTranslator = (*CommandCodePlugin)(nil)
	_ pluginapi.ProviderExecutor   = (*CommandCodePlugin)(nil)
	_ pluginapi.UsagePlugin        = (*CommandCodePlugin)(nil)
)

// normalizeModel strips provider prefixes, alias suffixes and whitespace.
func normalizeModel(model string) string {
	m := strings.TrimSpace(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if i := strings.Index(m, "("); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	return strings.ToLower(m)
}
