package plugin

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Translator keeps the commandcode-proxy-master protocol conversions available
// on every canonical edge the host may ask for.
type Translator struct {
	cfg *pluginConfig
}

func NewTranslator(cfg *pluginConfig) *Translator { return &Translator{cfg: cfg} }

func (t *Translator) TranslateRequest(_ context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
	from := strings.ToLower(strings.TrimSpace(req.FromFormat))
	to := strings.ToLower(strings.TrimSpace(req.ToFormat))
	if to != "" && to != "commandcode" && to != "openai" {
		return pluginapi.PayloadResponse{}, fmt.Errorf("unsupported request translation %s -> %s", req.FromFormat, req.ToFormat)
	}
	var openAI map[string]any
	switch from {
	case "", "openai", "commandcode":
		openAI = decodeObject(req.Body)
	case "claude", "anthropic":
		openAI = convertAnthropicToOpenAI(decodeObject(req.Body))
	case "openai-response", "responses":
		openAI = convertResponsesToChat(decodeObject(req.Body))
	default:
		return pluginapi.PayloadResponse{Body: append([]byte(nil), req.Body...)}, nil
	}
	t.normalizeRequestModelMap(req.Model, openAI)
	ccBody := buildCcRequest(openAI, t.cfg)
	return pluginapi.PayloadResponse{Body: encodeJSON(ccBody)}, nil
}

func (t *Translator) TranslateResponse(_ context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
	from := strings.ToLower(strings.TrimSpace(req.FromFormat))
	to := strings.ToLower(strings.TrimSpace(req.ToFormat))
	if from != "" && from != "commandcode" && from != "openai" {
		return pluginapi.PayloadResponse{}, fmt.Errorf("unsupported response translation %s -> %s", req.FromFormat, req.ToFormat)
	}
	if to != "" && to != "openai" && to != "claude" && to != "openai-response" && to != "commandcode" {
		return pluginapi.PayloadResponse{}, fmt.Errorf("unsupported response translation %s -> %s", req.FromFormat, req.ToFormat)
	}
	fixed, _ := mapReasoningBody(req.Body)
	return pluginapi.PayloadResponse{Body: fixed}, nil
}

func (t *Translator) normalizeRequestModelMap(model string, openAI map[string]any) {
	if len(openAI) == 0 {
		return
	}
	name := t.cfg.upstreamName(model)
	if name == "" {
		return
	}
	if current := asString(openAI["model"]); current == name {
		return
	}
	openAI["model"] = name
}

// mapReasoningBody backfills reasoning_content from commandcode's
// reasoning / reasoning_details fields for translated edges.
func mapReasoningBody(body []byte) ([]byte, bool) {
	if len(body) == 0 || !gjson.ValidBytes(body) || !strings.Contains(string(body), "reasoning") {
		return body, false
	}
	choices := gjson.GetBytes(body, "choices")
	if !choices.IsArray() {
		return body, false
	}
	out := body
	changed := false
	choiceIdx := -1
	choices.ForEach(func(_, choice gjson.Result) bool {
		choiceIdx++
		for _, field := range []string{"delta", "message"} {
			msg := choice.Get(field)
			if !msg.Exists() || !msg.IsObject() {
				continue
			}
			text, ok := reasoningText(msg)
			if !ok || text == "" {
				continue
			}
			if existing := msg.Get("reasoning_content"); existing.Exists() && strings.TrimSpace(existing.String()) != "" {
				continue
			}
			updated, err := sjson.SetBytes(out, "choices."+itoa(int64(choiceIdx))+"."+field+".reasoning_content", text)
			if err == nil {
				out = updated
				changed = true
			}
		}
		return true
	})
	return out, changed
}

func reasoningText(msg gjson.Result) (string, bool) {
	if details := msg.Get("reasoning_details"); details.IsArray() {
		var parts []string
		for _, detail := range details.Array() {
			if text := detail.Get("text").String(); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, ""), true
		}
	}
	if r := msg.Get("reasoning"); r.Type == gjson.String {
		if text := strings.TrimSpace(r.String()); text != "" {
			return r.String(), true
		}
	}
	return "", false
}
