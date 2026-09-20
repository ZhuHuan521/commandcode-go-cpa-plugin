package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestManagedAuthFilesMaterializeConfiguredKeys(t *testing.T) {
	cfg := parseConfig([]byte(`
shared_scheduling: true
priority: 7
base_url: https://cc.example
api_keys:
  - key: key-a
    weight: 3
    priority: 9
    proxy_url: http://proxy-a
  - key: key-b
    weight: 1
`))
	p := &CommandCodePlugin{cfg: cfg}
	files := p.ManagedAuthFiles()
	if len(files) != 2 {
		t.Fatalf("ManagedAuthFiles() len = %d, want 2", len(files))
	}
	byKey := map[string]managedAuthDocument{}
	for _, file := range files {
		if !strings.HasPrefix(file.Name, ManagedAuthFileNamePrefix) || !strings.HasSuffix(file.Name, ".json") {
			t.Fatalf("managed file name = %q", file.Name)
		}
		var doc managedAuthDocument
		if errUnmarshal := json.Unmarshal(file.JSON, &doc); errUnmarshal != nil {
			t.Fatalf("decode managed auth %s: %v", file.Name, errUnmarshal)
		}
		byKey[doc.APIKey] = doc
	}
	first := byKey["key-a"]
	if first.Type != Provider || first.AuthKind != "apikey" || first.Priority != 9 || first.Weight != 3 || first.ProxyURL != "http://proxy-a" {
		t.Fatalf("key-a document = %#v", first)
	}
	second := byKey["key-b"]
	if second.Type != Provider || second.AuthKind != "apikey" || second.Priority != 7 || second.Weight != 1 || second.ProxyURL != "" {
		t.Fatalf("key-b document = %#v", second)
	}
}

func TestManagedAuthFilesUseLegacyKey(t *testing.T) {
	p := &CommandCodePlugin{cfg: parseConfig([]byte("shared_scheduling: true\napi_key: user_test\n"))}
	files := p.ManagedAuthFiles()
	if len(files) != 1 {
		t.Fatalf("ManagedAuthFiles() len = %d, want 1", len(files))
	}
	var doc managedAuthDocument
	if errUnmarshal := json.Unmarshal(files[0].JSON, &doc); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if doc.APIKey != "user_test" || doc.ManagedBy != ManagedAuthMarker {
		t.Fatalf("managed document = %#v", doc)
	}
}

func TestManagedAuthFilesNoopForDirectRouting(t *testing.T) {
	p := &CommandCodePlugin{cfg: parseConfig([]byte("shared_scheduling: false\napi_key: user_test\n"))}
	if files := p.ManagedAuthFiles(); len(files) != 0 {
		t.Fatalf("ManagedAuthFiles() = %#v, want none", files)
	}
}

func TestCommandCodeModelsForAuthReturnsEmptyForApikey(t *testing.T) {
	cfg := parseConfig([]byte(`
shared_scheduling: true
models:
  - alias: deepseek-v4-flash
    name: deepseek/deepseek-v4-flash
`))
	p := NewModelProvider(cfg)
	resp, errModels := p.ModelsForAuth(t.Context(), pluginapi.AuthModelRequest{
		AuthID: "commandcode-managed-a",
		Metadata: map[string]any{
			"type":      Provider,
			"auth_kind": "apikey",
			"api_key":   "user_a",
			"base_url":  "https://cc.example",
			"priority":  11,
			"weight":    3,
			"proxy_url": "http://proxy-a",
		},
		Attributes: map[string]string{"path": "managed.json", "source": "managed.json"},
	})
	if errModels != nil {
		t.Fatalf("ModelsForAuth() error = %v", errModels)
	}
	// commandCodeAuthUpdate must not backfill base_url: the plugin executor
	// owns the upstream path, and leaking base_url into auth attributes makes
	// the host build <base_url>/chat/completions (404 at Command Code).
	if resp.AuthUpdate.Attributes != nil && len(resp.AuthUpdate.Attributes) > 0 {
		t.Fatalf("AuthUpdate.Attributes = %v, want empty for apikey records", resp.AuthUpdate.Attributes)
	}
}

func TestManagedAuthFilesCarryDisableCoolingOverride(t *testing.T) {
	cfg := parseConfig([]byte(`
shared_scheduling: true
disable_cooling: true
api_keys:
  - key: key-a
  - key: key-b
    disable_cooling: false
`))
	p := &CommandCodePlugin{cfg: cfg}
	files := p.ManagedAuthFiles()
	if len(files) != 2 {
		t.Fatalf("ManagedAuthFiles() len = %d, want 2", len(files))
	}
	byKey := map[string]map[string]any{}
	for _, file := range files {
		var doc map[string]any
		if errUnmarshal := json.Unmarshal(file.JSON, &doc); errUnmarshal != nil {
			t.Fatalf("decode managed auth %s: %v", file.Name, errUnmarshal)
		}
		byKey[asString(doc["api_key"])] = doc
	}
	// Plugin-wide override applies to keys without their own setting.
	if got, ok := byKey["key-a"]["disable_cooling"]; !ok || got != true {
		t.Fatalf("key-a disable_cooling = %v (present=%v), want true", got, ok)
	}
	// A per-key override wins over the plugin-wide default.
	if got, ok := byKey["key-b"]["disable_cooling"]; !ok || got != false {
		t.Fatalf("key-b disable_cooling = %v (present=%v), want false", got, ok)
	}
}

func TestManagedAuthFilesOmitDisableCoolingByDefault(t *testing.T) {
	cfg := parseConfig([]byte("shared_scheduling: true\napi_key: user_test\n"))
	p := &CommandCodePlugin{cfg: cfg}
	files := p.ManagedAuthFiles()
	if len(files) != 1 {
		t.Fatalf("ManagedAuthFiles() len = %d, want 1", len(files))
	}
	var doc map[string]any
	if errUnmarshal := json.Unmarshal(files[0].JSON, &doc); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if _, exists := doc["disable_cooling"]; exists {
		t.Fatalf("managed document unexpectedly pins disable_cooling: %s", string(files[0].JSON))
	}
}
