package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := parseConfig([]byte(""))
	if cfg.baseURL() != defaultAPIBase {
		t.Fatalf("baseURL = %q, want %q", cfg.baseURL(), defaultAPIBase)
	}
	if cfg.protocolVersion() != defaultProtocolVersion {
		t.Fatalf("protocolVersion = %q", cfg.protocolVersion())
	}
	if len(cfg.effectiveModels()) == 0 {
		t.Fatal("default model catalog is empty")
	}
}

func TestConfiguredModelRewrite(t *testing.T) {
	cfg := parseConfig([]byte(`
models:
  - alias: flash
    name: deepseek/deepseek-v4-flash
`))
	if got := cfg.upstreamName("flash"); got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("upstreamName = %q", got)
	}
	if _, ok := cfg.modelSet()["flash"]; !ok {
		t.Fatal("alias not claimed")
	}
}

func TestCommandCodeBaseURLNormalizesVersionedEndpointForms(t *testing.T) {
	for _, raw := range []string{
		"https://cc.example/provider/v1",
		"https://cc.example/provider/v1/models",
		"https://cc.example/provider/v1/chat/completions",
		"https://cc.example/provider/v1/responses",
	} {
		cfg := parseConfig([]byte("base_url: " + raw + "\n"))
		if got := cfg.baseURL(); got != "https://cc.example" {
			t.Fatalf("baseURL(%q) = %q, want https://cc.example", raw, got)
		}
	}
}

func TestConfigFieldsSurviveRegistrationMetadata(t *testing.T) {
	desc, _ := Build(nil)
	raw, err := json.Marshal(desc.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ConfigFields") {
		t.Fatalf("registration metadata does not carry ConfigFields: %s", raw)
	}
	if len(desc.Metadata.ConfigFields) == 0 {
		t.Fatal("plugin metadata has no config fields")
	}
}
