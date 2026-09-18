package plugin

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	defaultAPIBase             = "https://api.commandcode.ai"
	defaultProtocolVersion     = "1.53.1"
	defaultDeviceProjectDir    = `C:\Users\dev\projects\app`
	defaultModelRefreshMinutes = 5
)

// ModelEntry maps a client-facing alias to the vendor model name.
type ModelEntry struct {
	Alias                 string         `yaml:"alias"`
	Name                  string         `yaml:"name"`
	DisplayName           string         `yaml:"display_name"`
	Priority              int            `yaml:"priority"`
	MaxContextLength      int64          `yaml:"max_context_length"`
	MaxContextLengthKebab int64          `yaml:"max-context-length"`
	MaxContextLengthCamel int64          `yaml:"maxContextLength"`
	Thinking              *ModelThinking `yaml:"thinking"`
	TestModel             string         `yaml:"test_model"`
}

// ModelThinking mirrors the host's named reasoning capability metadata.
type ModelThinking struct {
	Levels         []string `yaml:"levels"`
	Min            int      `yaml:"min"`
	Max            int      `yaml:"max"`
	ZeroAllowed    bool     `yaml:"zero_allowed"`
	DynamicAllowed bool     `yaml:"dynamic_allowed"`
}

func (entry ModelEntry) contextLength() int64 {
	for _, value := range []int64{entry.MaxContextLength, entry.MaxContextLengthKebab, entry.MaxContextLengthCamel} {
		if value > 0 {
			return value
		}
	}
	return 0
}

// APIKeyEntry is one weighted pool member.
type APIKeyEntry struct {
	Key      string `yaml:"key"`
	Weight   int    `yaml:"weight"`
	Priority int    `yaml:"priority"`
	ProxyURL string `yaml:"proxy_url"`
	Disabled bool   `yaml:"disabled"`
}

func (entry APIKeyEntry) normalizedWeight() int {
	if entry.Weight <= 0 {
		return 1
	}
	return entry.Weight
}

// pluginConfig mirrors plugins.configs.<id> for the commandcode plugin.
type pluginConfig struct {
	Enabled  bool `yaml:"enabled"`
	Priority int  `yaml:"priority"`
	// SharedScheduling registers Command Code models as ordinary provider
	// routes so the host can select them together with built-in providers.
	// Explicit commandcode/<model> requests remain available as a pin.
	SharedScheduling       *bool         `yaml:"shared_scheduling"`
	Models                 []ModelEntry  `yaml:"models"`
	BaseURL                string        `yaml:"base_url"`
	ProjectSlug            string        `yaml:"project_slug"`
	ProtocolVersion        string        `yaml:"protocol_version"`
	FingerprintSalt        string        `yaml:"fingerprint_salt"`
	DeviceProjectDir       string        `yaml:"device_project_dir"`
	CLIMode                string        `yaml:"cli_mode"`
	CLISessionMode         string        `yaml:"cli_session_mode"`
	EmptySystemPlaceholder *bool         `yaml:"empty_system_placeholder"`
	UseProviderModels      *bool         `yaml:"use_provider_models"`
	ModelRefreshIntervalMS int64         `yaml:"model_refresh_interval_ms"`
	ZDR                    bool          `yaml:"zdr"`
	StreamIdleMS           int64         `yaml:"stream_idle_ms"`
	NonStreamIdleMS        int64         `yaml:"nonstream_idle_ms"`
	MaxBodyMB              int64         `yaml:"max_body_mb"`
	MaxInflight            int64         `yaml:"max_inflight"`
	ClientDrainTimeoutMS   int64         `yaml:"client_drain_timeout_ms"`
	APIKey                 string        `yaml:"api_key"`
	APIKeys                []APIKeyEntry `yaml:"api_keys"`
	ProxyURL               string        `yaml:"proxy_url"`

	claimed  map[string]struct{}
	rewrites map[string]string
}

func boolDefault(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

func intDefault(v int64, fallback int64) int64 {
	if v <= 0 {
		return fallback
	}
	return v
}

func parseConfig(raw []byte) *pluginConfig {
	cfg := &pluginConfig{
		ProtocolVersion:        defaultProtocolVersion,
		DeviceProjectDir:       defaultDeviceProjectDir,
		CLIMode:                "agent",
		CLISessionMode:         "interactive",
		ModelRefreshIntervalMS: defaultModelRefreshMinutes * 60 * 1000,
		StreamIdleMS:           30_000,
		NonStreamIdleMS:        90_000,
		MaxBodyMB:              100,
	}
	if len(raw) > 0 {
		_ = yaml.Unmarshal(raw, cfg)
	}
	if p := os.Getenv("CC_FINGERPRINT_SALT"); p != "" {
		cfg.FingerprintSalt = p
	}
	if p := os.Getenv("CC_DEVICE_PROJECT_DIR"); p != "" {
		cfg.DeviceProjectDir = p
	}
	if p := os.Getenv("CC_CLI_MODE"); p != "" {
		cfg.CLIMode = p
	}
	if p := os.Getenv("CC_CLI_SESSION_MODE"); p != "" {
		cfg.CLISessionMode = p
	}
	if p := os.Getenv("CC_STREAM_IDLE_MS"); p != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil && n > 0 {
			cfg.StreamIdleMS = n
		}
	}
	if p := os.Getenv("CC_NONSTREAM_IDLE_MS"); p != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil && n > 0 {
			cfg.NonStreamIdleMS = n
		}
	}
	cfg.buildIndexes()
	return cfg
}

func (c *pluginConfig) effectiveModels() []ModelEntry {
	if c == nil {
		return defaultModelEntries()
	}
	if len(c.Models) > 0 {
		return c.Models
	}
	return defaultModelEntries()
}

func (c *pluginConfig) buildIndexes() {
	if c == nil {
		return
	}
	c.claimed = make(map[string]struct{})
	c.rewrites = make(map[string]string)
	for _, entry := range c.effectiveModels() {
		alias := normalizeModel(entry.Alias)
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = strings.TrimSpace(entry.Alias)
		}
		if alias != "" {
			c.claimed[alias] = struct{}{}
		}
		if name != "" {
			c.claimed[normalizeModel(name)] = struct{}{}
			if strings.TrimSpace(entry.Name) != "" {
				c.rewrites[alias] = entry.Name
				c.rewrites[normalizeModel(entry.Name)] = entry.Name
			}
		}
	}
}

func (c *pluginConfig) ensureIndexes() {
	if c != nil && c.claimed == nil {
		c.buildIndexes()
	}
}

func (c *pluginConfig) modelSet() map[string]struct{} {
	if c == nil {
		return map[string]struct{}{}
	}
	c.ensureIndexes()
	return c.claimed
}

func (c *pluginConfig) upstreamName(model string) string {
	if c == nil {
		return ""
	}
	c.ensureIndexes()
	return c.rewrites[normalizeModel(model)]
}

func (c *pluginConfig) baseURL() string {
	base := normalizeCommandCodeBaseURL(c.apiBaseValue())
	if base == "" {
		return defaultAPIBase
	}
	return base
}

func (c *pluginConfig) apiBaseValue() string {
	if c != nil && strings.TrimSpace(c.BaseURL) != "" {
		return c.BaseURL
	}
	return defaultAPIBase
}

func (c *pluginConfig) protocolVersion() string {
	if c != nil && strings.TrimSpace(c.ProtocolVersion) != "" {
		return c.ProtocolVersion
	}
	return defaultProtocolVersion
}

func (c *pluginConfig) projectSlug() string {
	if c != nil && strings.TrimSpace(c.ProjectSlug) != "" {
		return strings.TrimSpace(c.ProjectSlug)
	}
	if c != nil && strings.TrimSpace(c.DeviceProjectDir) != "" {
		return slugifyProjectPath(c.DeviceProjectDir)
	}
	return slugifyProjectPath(defaultDeviceProjectDir)
}

func (c *pluginConfig) sharedScheduling() bool {
	return boolDefault(c.SharedScheduling, true)
}

// hasConfiguredKey reports whether the plugin config can execute without a
// host-selected auth record. Static model registration uses this to decide
// whether bare model IDs are safe to expose as a direct fallback route.
func (c *pluginConfig) hasConfiguredKey() bool {
	if c == nil {
		return false
	}
	if strings.TrimSpace(c.APIKey) != "" {
		return true
	}
	for _, entry := range c.APIKeys {
		if !entry.Disabled && strings.TrimSpace(entry.Key) != "" {
			return true
		}
	}
	return false
}

func (c *pluginConfig) streamIdle() time.Duration {
	return time.Duration(intDefault(c.StreamIdleMS, 30_000)) * time.Millisecond
}

func (c *pluginConfig) nonStreamIdle() time.Duration {
	return time.Duration(intDefault(c.NonStreamIdleMS, 90_000)) * time.Millisecond
}

func (c *pluginConfig) emptySystemPlaceholder() bool {
	return boolDefault(c.EmptySystemPlaceholder, true)
}

func (c *pluginConfig) useProviderModels() bool {
	return boolDefault(c.UseProviderModels, true)
}

func (c *pluginConfig) modelRefreshInterval() time.Duration {
	return time.Duration(intDefault(c.ModelRefreshIntervalMS, defaultModelRefreshMinutes*60*1000)) * time.Millisecond
}

func (c *pluginConfig) members(req pluginapi.ExecutorRequest) []APIKeyEntry {
	if c == nil {
		return nil
	}
	// A request selected by the host scheduler carries the concrete credential
	// in AuthAttributes/AuthMetadata. Prefer it over the plugin-wide fallback;
	// otherwise every scheduler choice would silently use the first configured key.
	if entries := apiKeyEntriesFromAuth(req); len(entries) > 0 {
		for index := range entries {
			entries[index].ProxyURL = firstNonEmpty(entries[index].ProxyURL, c.ProxyURL)
		}
		return entries
	}
	out := make([]APIKeyEntry, 0, len(c.APIKeys)+1)
	for _, entry := range c.APIKeys {
		if strings.TrimSpace(entry.Key) == "" || entry.Disabled {
			continue
		}
		entry.ProxyURL = firstNonEmpty(entry.ProxyURL, c.ProxyURL)
		out = append(out, entry)
	}
	if len(out) > 0 {
		return out
	}
	if strings.TrimSpace(c.APIKey) != "" {
		return []APIKeyEntry{{Key: strings.TrimSpace(c.APIKey), Weight: 1, ProxyURL: c.ProxyURL}}
	}
	return nil
}

func apiKeyEntriesFromAuth(req pluginapi.ExecutorRequest) []APIKeyEntry {
	if key := firstStringValue(req.AuthAttributes, "api_key", "api-key", "key"); key != "" {
		return []APIKeyEntry{{
			Key:      key,
			Weight:   parseIntValue(req.AuthAttributes["weight"]),
			ProxyURL: strings.TrimSpace(req.AuthAttributes["proxy_url"]),
		}}
	}
	if req.AuthMetadata != nil {
		if key := firstAnyStringValue(req.AuthMetadata, "api_key", "api-key", "key"); key != "" {
			return []APIKeyEntry{{Key: key, ProxyURL: strings.TrimSpace(asString(req.AuthMetadata["proxy_url"]))}}
		}
		if raw, ok := req.AuthMetadata["api_keys"]; ok {
			if entries := decodeAPIKeyEntries(raw); len(entries) > 0 {
				return entries
			}
		}
	}
	return nil
}

func decodeAPIKeyEntries(raw any) []APIKeyEntry {
	values, ok := raw.([]any)
	if !ok {
		if typed, okTyped := raw.([]map[string]any); okTyped {
			values = make([]any, len(typed))
			for i := range typed {
				values[i] = typed[i]
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	entries := make([]APIKeyEntry, 0, len(values))
	for _, value := range values {
		entry, okEntry := value.(map[string]any)
		if !okEntry {
			continue
		}
		key := firstAnyStringValue(entry, "key", "api_key", "api-key")
		if key == "" {
			continue
		}
		weight := int(asNumber(entry["weight"]))
		priority := int(asNumber(entry["priority"]))
		proxyURL := strings.TrimSpace(asString(entry["proxy_url"]))
		disabled := asBool(entry["disabled"])
		if disabled {
			continue
		}
		entries = append(entries, APIKeyEntry{Key: key, Weight: weight, Priority: priority, ProxyURL: proxyURL})
	}
	return entries
}

func firstStringValue(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstAnyStringValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(asString(values[key])); value != "" {
			return value
		}
	}
	return ""
}

func parseIntValue(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func configFields() []pluginapi.ConfigField {
	boolean := pluginapi.ConfigFieldTypeBoolean
	integer := pluginapi.ConfigFieldTypeInteger
	stringType := pluginapi.ConfigFieldTypeString
	arrayType := pluginapi.ConfigFieldTypeArray
	return []pluginapi.ConfigField{
		{Name: "shared_scheduling", Type: boolean, Description: "Register bare model names in the host scheduler; commandcode/<model> remains an explicit pin."},
		{Name: "api_key", Type: stringType, Description: "Legacy single Command Code API key (user_xxx)."},
		{Name: "api_keys", Type: arrayType, Description: "Weighted key pool: [{key, weight, priority, proxy_url}]."},
		{Name: "proxy_url", Type: stringType, Description: "Optional default proxy for keys without their own proxy_url."},
		{Name: "base_url", Type: stringType, Description: "Command Code API base URL, default https://api.commandcode.ai"},
		{Name: "models", Type: arrayType, Description: "Model claims as [{alias, name, display_name}]."},
		{Name: "project_slug", Type: stringType, Description: "Optional x-project-slug override."},
		{Name: "protocol_version", Type: stringType, Description: "Command Code wire protocol version, default 1.53.1."},
		{Name: "fingerprint_salt", Type: stringType, Description: "Rotates all per-key device identities when changed."},
		{Name: "device_project_dir", Type: stringType, Description: "Fake project directory used for workingDir and slug."},
		{Name: "cli_mode", Type: stringType, Description: "CLI envelope mode, e.g. agent."},
		{Name: "cli_session_mode", Type: stringType, Description: "Lifecycle session mode, e.g. interactive."},
		{Name: "empty_system_placeholder", Type: boolean, Description: "Send a space system block to suppress CC default prompt."},
		{Name: "use_provider_models", Type: boolean, Description: "Dynamically fetch /provider/v1/models when a key is configured."},
		{Name: "model_refresh_interval_ms", Type: integer, Description: "Model catalog cache refresh interval in milliseconds."},
		{Name: "zdr", Type: boolean, Description: "Request ZDR-only routing from Command Code."},
		{Name: "stream_idle_ms", Type: integer, Description: "Streaming upstream read idle timeout in milliseconds."},
		{Name: "nonstream_idle_ms", Type: integer, Description: "Non-streaming upstream read idle timeout in milliseconds."},
		{Name: "max_body_mb", Type: integer, Description: "Request body cap in megabytes; larger payloads are rejected."},
		{Name: "max_inflight", Type: integer, Description: "Process-wide concurrent request cap; 0 means unlimited."},
		{Name: "client_drain_timeout_ms", Type: integer, Description: "Drop stalled downstream clients after this many milliseconds; 0 disables."},
	}
}
