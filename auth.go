package plugin

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// AuthProvider turns a commandcode JSON file into the host's normal auth
// records. This is the bridge that makes Command Code credentials visible to
// the core mixed-provider scheduler; the plugin config remains the source of
// defaults, while each concrete key is represented as a regular auth.
type AuthProvider struct {
	cfg *pluginConfig
}

func NewAuthProvider(cfg *pluginConfig) *AuthProvider { return &AuthProvider{cfg: cfg} }

func (p *AuthProvider) Identifier() string { return Provider }

func (p *AuthProvider) ParseAuth(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	requested := strings.ToLower(strings.TrimSpace(req.Provider))
	if requested != "" && !isCommandCodeProvider(requested) {
		return pluginapi.AuthParseResponse{}, nil
	}
	raw := decodeObject(req.RawJSON)
	typ := strings.ToLower(strings.TrimSpace(asString(raw["type"])))
	if typ != Provider && typ != "command-code" && typ != "command_code" {
		return pluginapi.AuthParseResponse{}, nil
	}
	entries := authKeyEntries(raw)
	if len(entries) == 0 {
		return pluginapi.AuthParseResponse{Handled: true}, nil
	}

	parsed := make([]pluginapi.AuthData, 0, len(entries))
	for index, entry := range entries {
		if strings.TrimSpace(entry.Key) == "" {
			continue
		}
		metadata := cloneMap(raw)
		metadata["type"] = Provider
		metadata["api_key"] = strings.TrimSpace(entry.Key)
		// Keep the source pool intact. The host may persist or refresh a
		// virtual auth record; dropping api_keys here would collapse a shared
		// source file to whichever key happened to be selected first.
		metadata["auth_kind"] = "apikey"
		if entry.Weight != 0 {
			metadata["weight"] = entry.Weight
		}
		proxyURL := firstNonEmpty(entry.ProxyURL, strings.TrimSpace(asString(raw["proxy_url"])))
		if proxyURL != "" {
			metadata["proxy_url"] = proxyURL
		}
		if entry.Disabled {
			metadata["disabled"] = true
		}

		attrs := map[string]string{
			"api_key":   strings.TrimSpace(entry.Key),
			"auth_kind": "apikey",
			"provider":  Provider,
		}
		baseURL := firstNonEmpty(entry.BaseURL, strings.TrimSpace(asString(raw["base_url"])))
		if baseURL != "" {
			metadata["base_url"] = baseURL
			attrs["base_url"] = baseURL
		}
		if proxyURL != "" {
			attrs["proxy_url"] = proxyURL
		}
		// Per-entry priority is the most specific setting, followed by the
		// source file and finally the plugin-wide default.
		priority := entry.Priority
		if priority == 0 {
			priority = firstInt(raw["priority"], 0)
		}
		if priority == 0 && p != nil && p.cfg != nil {
			priority = p.cfg.Priority
		}
		if priority != 0 {
			attrs["priority"] = strconv.Itoa(priority)
		}
		if entry.Weight != 0 {
			attrs["weight"] = strconv.Itoa(entry.Weight)
		}
		prefix := strings.Trim(strings.TrimSpace(firstNonEmpty(entry.Prefix, asString(raw["prefix"]))), "/")
		if prefix != "" {
			attrs["prefix"] = prefix
		}
		// Cooldown policy: a per-entry override wins, then the source file,
		// then the plugin-wide default. Leaving it unset keeps host policy.
		if entry.DisableCooling != nil {
			metadata["disable_cooling"] = *entry.DisableCooling
		} else if p != nil && p.cfg != nil && p.cfg.DisableCooling != nil {
			metadata["disable_cooling"] = *p.cfg.DisableCooling
		}

		id := ""
		if len(entries) > 1 {
			seed := strings.TrimSpace(req.FileName)
			if seed == "" {
				seed = filepath.Base(strings.TrimSpace(req.Path))
			}
			if seed == "" {
				seed = "commandcode-plugin.json"
			}
			id = seed + "#" + strconv.Itoa(index)
		}
		parsed = append(parsed, pluginapi.AuthData{
			Provider:    Provider,
			ID:          id,
			FileName:    req.FileName,
			Label:       commandCodeAuthLabel(index, len(entries)),
			Prefix:      prefix,
			ProxyURL:    proxyURL,
			Disabled:    entry.Disabled || asBool(raw["disabled"]),
			StorageJSON: encodeJSON(metadata),
			Metadata:    metadata,
			Attributes:  attrs,
		})
	}
	if len(parsed) == 0 {
		return pluginapi.AuthParseResponse{Handled: true}, nil
	}
	return pluginapi.AuthParseResponse{Handled: true, Auths: parsed}, nil
}

func (p *AuthProvider) StartLogin(_ context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	return pluginapi.AuthLoginStartResponse{Provider: firstNonEmpty(req.Provider, Provider)}, fmt.Errorf("commandcode uses API keys; interactive login is not supported")
}

func (p *AuthProvider) PollLogin(_ context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusError,
		Message: "commandcode uses API keys; interactive login is not supported",
	}, nil
}

func (p *AuthProvider) RefreshAuth(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	provider := firstNonEmpty(req.AuthProvider, Provider)
	metadata := cloneMap(req.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["type"] = provider
	attrs := cloneStringMap(req.Attributes)
	if attrs == nil {
		attrs = map[string]string{}
	}
	return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
		Provider:    provider,
		ID:          req.AuthID,
		StorageJSON: append([]byte(nil), req.StorageJSON...),
		Metadata:    metadata,
		Attributes:  attrs,
		ProxyURL:    attrs["proxy_url"],
		Disabled:    asBool(metadata["disabled"]),
	}}, nil
}

type authKeyEntry struct {
	Key            string
	Weight         int
	Priority       int
	ProxyURL       string
	BaseURL        string
	Prefix         string
	Disabled       bool
	DisableCooling *bool
}

func authKeyEntries(raw map[string]any) []authKeyEntry {
	entries := make([]authKeyEntry, 0)
	if values, ok := raw["api_keys"].([]any); ok {
		for _, value := range values {
			item := asMap(value)
			key := strings.TrimSpace(firstNonEmpty(asString(item["key"]), asString(item["api_key"])))
			if key == "" {
				continue
			}
			entries = append(entries, authKeyEntry{
				Key:            key,
				Weight:         int(asNumber(item["weight"])),
				Priority:       int(asNumber(item["priority"])),
				ProxyURL:       strings.TrimSpace(asString(item["proxy_url"])),
				BaseURL:        strings.TrimSpace(asString(item["base_url"])),
				Prefix:         strings.Trim(strings.TrimSpace(asString(item["prefix"])), "/"),
				Disabled:       asBool(item["disabled"]),
				DisableCooling: boolPointerFromMap(item, "disable_cooling", "disable-cooling"),
			})
		}
	}
	if len(entries) > 0 {
		return entries
	}
	key := strings.TrimSpace(firstNonEmpty(
		asString(raw["api_key"]),
		asString(raw["api-key"]),
		asString(raw["key"]),
	))
	if key == "" {
		return nil
	}
	return []authKeyEntry{{
		Key:      key,
		Weight:   int(asNumber(raw["weight"])),
		Priority: int(asNumber(raw["priority"])),
		ProxyURL: strings.TrimSpace(firstNonEmpty(asString(raw["proxy_url"]), asString(raw["proxy-url"]))),
		BaseURL:  strings.TrimSpace(asString(raw["base_url"])),
		Prefix:   strings.Trim(strings.TrimSpace(asString(raw["prefix"])), "/"),
		Disabled: asBool(raw["disabled"]),
	}}
}

func firstInt(value any, fallback int) int {
	if parsed := int(asNumber(value)); parsed != 0 {
		return parsed
	}
	return fallback
}

func commandCodeAuthLabel(index, total int) string {
	if total <= 1 {
		return "CommandCode"
	}
	return "CommandCode #" + strconv.Itoa(index+1)
}

func isCommandCodeProvider(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case Provider, "command-code", "command_code":
		return true
	default:
		return false
	}
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

var _ pluginapi.AuthProvider = (*AuthProvider)(nil)
