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
	Alias       string `yaml:"alias"`
	Name        string `yaml:"name"`
	DisplayName string `yaml:"display_name"`
}

// APIKeyEntry is one weighted pool member.
type APIKeyEntry struct {
	Key      string `yaml:"key"`
	Weight   int    `yaml:"weight"`
	ProxyURL string `yaml:"proxy_url"`
}

func (entry APIKeyEntry) normalizedWeight() int {
	if entry.Weight <= 0 {
		return 1
	}
	return entry.Weight
}

// pluginConfig mirrors plugins.configs.<id> for the commandcode plugin.
type pluginConfig struct {
	Enabled                bool          `yaml:"enabled"`
	Priority               int           `yaml:"priority"`
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
	base := strings.TrimRight(c.apiBaseValue(), "/")
	if strings.HasSuffix(base, "/provider/v1") {
		base = strings.TrimSuffix(base, "/provider/v1")
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
	if c != nil && strings.TrimSpace(c.DeviceProjectDir) != "" {
		return slugifyProjectPath(c.DeviceProjectDir)
	}
	if c != nil && strings.TrimSpace(c.ProjectSlug) != "" {
		return strings.TrimSpace(c.ProjectSlug)
	}
	return slugifyProjectPath(defaultDeviceProjectDir)
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
	out := make([]APIKeyEntry, 0, len(c.APIKeys)+1)
	for _, entry := range c.APIKeys {
		if strings.TrimSpace(entry.Key) == "" {
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
	if k := strings.TrimSpace(req.AuthAttributes["api_key"]); k != "" {
		return []APIKeyEntry{{Key: k, Weight: 1, ProxyURL: c.ProxyURL}}
	}
	if req.AuthMetadata != nil {
		if k, ok := req.AuthMetadata["api_key"].(string); ok && strings.TrimSpace(k) != "" {
			return []APIKeyEntry{{Key: strings.TrimSpace(k), Weight: 1, ProxyURL: c.ProxyURL}}
		}
	}
	return nil
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
		{Name: "api_key", Type: stringType, Description: "Legacy single Command Code API key (user_xxx)."},
		{Name: "api_keys", Type: arrayType, Description: "Weighted key pool: [{key, weight, proxy_url}]."},
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
