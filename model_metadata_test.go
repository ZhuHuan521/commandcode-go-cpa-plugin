package plugin

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type modelDiscoveryHTTPClient struct {
	url string
}

func (c *modelDiscoveryHTTPClient) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	c.url = req.URL
	return pluginapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"data":[{"id":"vendor/discovered"}]}`),
	}, nil
}

func (*modelDiscoveryHTTPClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, nil
}

func TestCommandCodeModelsForAuthMergesAuthMetadataModels(t *testing.T) {
	cfg := parseConfig([]byte("shared_scheduling: true\nmodels:\n  - alias: configured\n    name: vendor/configured\n"))
	provider := NewModelProvider(cfg)
	response, errModels := provider.ModelsForAuth(context.Background(), pluginapi.AuthModelRequest{
		Metadata: map[string]any{
			"models": []any{
				map[string]any{
					"alias":              "private",
					"name":               "vendor/private",
					"display_name":       "Private model",
					"max_context_length": 123456,
					"thinking": map[string]any{
						"levels": []any{"none", "high", "auto"},
					},
				},
			},
		},
	})
	if errModels != nil {
		t.Fatalf("ModelsForAuth() error = %v", errModels)
	}
	seen := make(map[string]bool, len(response.Models))
	for _, model := range response.Models {
		seen[model.ID] = true
	}
	for _, want := range []string{
		"private",
		"configured",
	} {
		if !seen[want] {
			t.Fatalf("ModelsForAuth() missing %q: %v", want, seen)
		}
	}
	for _, model := range response.Models {
		if model.ID == "private" {
			if model.DisplayName != "Private model" || strings.Contains(model.DisplayName, "via CommandCode") {
				t.Fatalf("private display name = %q, want Private model without provider suffix", model.DisplayName)
			}
			if model.ContextLength != 123456 {
				t.Fatalf("private context length = %d, want 123456", model.ContextLength)
			}
			if model.Thinking == nil || len(model.Thinking.Levels) != 3 {
				t.Fatalf("private thinking = %#v, want three levels", model.Thinking)
			}
		}
	}
}

func TestCommandCodeModelsForAuthUsesPerAuthBaseURL(t *testing.T) {
	cfg := parseConfig([]byte("base_url: https://config.example\napi_key: config-key\n"))
	provider := NewModelProvider(cfg)
	client := &modelDiscoveryHTTPClient{}
	_, errModels := provider.ModelsForAuth(context.Background(), pluginapi.AuthModelRequest{
		Attributes: map[string]string{
			"api_key":  "auth-key",
			"base_url": "https://auth.example/provider/v1",
		},
		HTTPClient: client,
	})
	if errModels != nil {
		t.Fatalf("ModelsForAuth() error = %v", errModels)
	}
	if client.url != "https://auth.example/provider/v1/models" {
		t.Fatalf("model discovery URL = %q, want per-auth endpoint", client.url)
	}
}
