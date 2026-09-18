package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestCommandCodeAuthProviderParsesWeightedKeys(t *testing.T) {
	p := NewAuthProvider(parseConfig([]byte("priority: 4\n")))
	resp, errParse := p.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: Provider,
		FileName: "commandcode.json",
		RawJSON: []byte(`{
          "type":"commandcode",
          "base_url":"https://cc.example",
          "priority":7,
          "api_keys":[
            {"key":"user_a","weight":3,"proxy_url":"http://proxy-a"},
            {"key":"user_b","weight":1}
          ]
        }`),
	})
	if errParse != nil {
		t.Fatalf("ParseAuth() error = %v", errParse)
	}
	if !resp.Handled || len(resp.Auths) != 2 {
		t.Fatalf("ParseAuth() = %#v, want two handled auths", resp)
	}
	if got := resp.Auths[0].Attributes["api_key"]; got != "user_a" {
		t.Fatalf("first api key = %q", got)
	}
	if got := resp.Auths[0].Attributes["priority"]; got != "7" {
		t.Fatalf("priority = %q", got)
	}
	if got := resp.Auths[0].Attributes["auth_kind"]; got != "apikey" {
		t.Fatalf("auth_kind = %q, want apikey", got)
	}
	if got := resp.Auths[0].Attributes["weight"]; got != "3" {
		t.Fatalf("weight = %q", got)
	}
	if got := resp.Auths[0].Attributes["base_url"]; got != "https://cc.example" {
		t.Fatalf("base_url = %q", got)
	}
	var stored map[string]any
	if errDecode := json.Unmarshal(resp.Auths[0].StorageJSON, &stored); errDecode != nil {
		t.Fatalf("StorageJSON decode error = %v", errDecode)
	}
	if values, exists := stored["api_keys"].([]any); !exists || len(values) != 2 {
		t.Fatalf("per-auth storage api_keys = %#v, want original two-key pool", stored["api_keys"])
	}
}

func TestCommandCodeAuthProviderIgnoresOtherTypes(t *testing.T) {
	p := NewAuthProvider(parseConfig(nil))
	resp, errParse := p.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		RawJSON: []byte(`{"type":"codex","api_key":"sk-test"}`),
	})
	if errParse != nil {
		t.Fatalf("ParseAuth() error = %v", errParse)
	}
	if resp.Handled {
		t.Fatalf("ParseAuth() handled unrelated type: %#v", resp)
	}
}

func TestCommandCodeAuthProviderAcceptsLegacyProviderAndKeyAliases(t *testing.T) {
	p := NewAuthProvider(parseConfig(nil))
	resp, errParse := p.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: "command-code",
		RawJSON:  []byte(`{"type":"command_code","api-key":"user_alias"}`),
	})
	if errParse != nil {
		t.Fatalf("ParseAuth() error = %v", errParse)
	}
	if !resp.Handled || len(resp.Auths) != 1 {
		t.Fatalf("ParseAuth() = %#v, want one handled auth", resp)
	}
	if got := resp.Auths[0].Attributes["api_key"]; got != "user_alias" {
		t.Fatalf("api_key = %q, want user_alias", got)
	}
}

func TestCommandCodeSharedRouterUsesCoreOnlyWhenAuthIsAvailable(t *testing.T) {
	cfg := parseConfig([]byte("shared_scheduling: true\nmodels:\n  - alias: flash\n    name: deepseek/deepseek-v4-flash\n"))
	router := NewRouter(cfg, NewModelProvider(cfg))
	bare, errBare := router.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "flash"})
	if errBare != nil {
		t.Fatalf("bare route error = %v", errBare)
	}
	if !bare.Handled || bare.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("bare route without commandcode auth = %#v, want direct self fallback", bare)
	}
	pinned, errPinned := router.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "commandcode/flash"})
	if errPinned != nil {
		t.Fatalf("pinned route error = %v", errPinned)
	}
	if !pinned.Handled || pinned.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("pinned route without commandcode auth = %#v, want self", pinned)
	}
	for _, model := range []string{"flash", "commandcode/flash"} {
		resp, errRoute := router.RouteModel(context.Background(), pluginapi.ModelRouteRequest{
			RequestedModel:     model,
			AvailableProviders: []string{"openai", Provider},
		})
		if errRoute != nil {
			t.Fatalf("route %q error = %v", model, errRoute)
		}
		if model == "flash" {
			if resp.Handled {
				t.Fatalf("bare route %q was handled despite commandcode auth: %#v", model, resp)
			}
			continue
		}
		if !resp.Handled || resp.TargetKind != pluginapi.ModelRouteTargetProvider || resp.Target != Provider || resp.TargetModel != "flash" {
			t.Fatalf("explicit route %q = %#v, want provider commandcode/flash", model, resp)
		}
	}
}

func TestCommandCodeStaticModelsExposeBareIDsForConfiguredKey(t *testing.T) {
	cfg := parseConfig([]byte("shared_scheduling: true\napi_key: user_test\nmodels:\n  - alias: flash\n    name: deepseek/deepseek-v4-flash\n"))
	p := NewModelProvider(cfg)
	resp, errModels := p.StaticModels(context.Background(), pluginapi.StaticModelRequest{})
	if errModels != nil {
		t.Fatalf("StaticModels() error = %v", errModels)
	}
	seen := map[string]bool{}
	for _, model := range resp.Models {
		seen[model.ID] = true
	}
	for _, want := range []string{"flash"} {
		if !seen[want] {
			t.Fatalf("StaticModels() missing %q: %v", want, seen)
		}
	}
}

func TestCommandCodeStaticModelsKeepOnePublicIDWithoutConfiguredKey(t *testing.T) {
	cfg := parseConfig([]byte("shared_scheduling: true\nmodels:\n  - alias: flash\n    name: deepseek/deepseek-v4-flash\n"))
	p := NewModelProvider(cfg)
	resp, errModels := p.StaticModels(context.Background(), pluginapi.StaticModelRequest{})
	if errModels != nil {
		t.Fatalf("StaticModels() error = %v", errModels)
	}
	if len(resp.Models) != 1 || resp.Models[0].ID != "flash" {
		t.Fatalf("StaticModels() = %#v, want one public alias", resp.Models)
	}
}

func TestCommandCodeModelsForAuthIncludesBareNames(t *testing.T) {
	cfg := parseConfig([]byte("shared_scheduling: true\nmodels:\n  - alias: flash\n    name: deepseek/deepseek-v4-flash\n"))
	p := NewModelProvider(cfg)
	resp, errModels := p.ModelsForAuth(context.Background(), pluginapi.AuthModelRequest{})
	if errModels != nil {
		t.Fatalf("ModelsForAuth() error = %v", errModels)
	}
	seen := map[string]bool{}
	for _, model := range resp.Models {
		seen[model.ID] = true
	}
	for _, want := range []string{"flash"} {
		if !seen[want] {
			t.Fatalf("ModelsForAuth() missing %q: %v", want, seen)
		}
	}
}

func TestCommandCodeExecutorPrefersSelectedAuthKey(t *testing.T) {
	cfg := parseConfig([]byte("api_key: config-key\nproxy_url: http://default-proxy\napi_keys:\n  - key: pool-key\n"))
	entries := cfg.members(pluginapi.ExecutorRequest{AuthAttributes: map[string]string{
		"api_key": "selected-key",
		"weight":  "9",
	}})
	if len(entries) != 1 || entries[0].Key != "selected-key" || entries[0].Weight != 9 || entries[0].ProxyURL != "http://default-proxy" {
		t.Fatalf("members() = %#v, want selected auth key", entries)
	}
}

func TestCommandCodeAuthProviderUsesPluginPriorityAsDefault(t *testing.T) {
	p := NewAuthProvider(parseConfig([]byte("priority: 12\n")))
	resp, errParse := p.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: Provider,
		FileName: "commandcode.json",
		RawJSON:  []byte(`{"type":"commandcode","api_key":"user_test"}`),
	})
	if errParse != nil || len(resp.Auths) != 1 {
		t.Fatalf("ParseAuth() = %#v, %v", resp, errParse)
	}
	if got := resp.Auths[0].Attributes["priority"]; got != "12" {
		t.Fatalf("priority = %q, want plugin default", got)
	}
}

func TestCommandCodeAuthProviderEntryPriorityOverridesFilePriority(t *testing.T) {
	p := NewAuthProvider(parseConfig([]byte("priority: 12\n")))
	resp, errParse := p.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: Provider,
		FileName: "commandcode.json",
		RawJSON:  []byte(`{"type":"commandcode","priority":7,"api_keys":[{"key":"user_test","priority":3}]}`),
	})
	if errParse != nil || len(resp.Auths) != 1 {
		t.Fatalf("ParseAuth() = %#v, %v", resp, errParse)
	}
	if got := resp.Auths[0].Attributes["priority"]; got != "3" {
		t.Fatalf("priority = %q, want per-entry priority", got)
	}
}

func TestCommandCodeModelsForAuthRepairsOAuthClassification(t *testing.T) {
	p := NewModelProvider(parseConfig([]byte("shared_scheduling: true\nmodels:\n  - alias: flash\n    name: deepseek/deepseek-v4-flash\n")))
	storage := []byte(`{"type":"commandcode","api_keys":[{"key":"user_test"}]}`)
	resp, errModels := p.ModelsForAuth(context.Background(), pluginapi.AuthModelRequest{
		AuthID:      "auth-1",
		StorageJSON: storage,
		Attributes:  map[string]string{"auth_kind": "oauth", "api_key": "user_test"},
		Metadata:    map[string]any{"auth_kind": "oauth"},
	})
	if errModels != nil {
		t.Fatalf("ModelsForAuth() error = %v", errModels)
	}
	if got := resp.AuthUpdate.Attributes["auth_kind"]; got != "apikey" {
		t.Fatalf("AuthUpdate auth_kind = %q, want apikey", got)
	}
	if string(resp.AuthUpdate.StorageJSON) != string(storage) {
		t.Fatalf("AuthUpdate StorageJSON = %s, want original source JSON", resp.AuthUpdate.StorageJSON)
	}
	if resp.AuthUpdate.Metadata != nil {
		t.Fatalf("AuthUpdate metadata = %#v, want nil so host can preserve source metadata", resp.AuthUpdate.Metadata)
	}
}
