package plugin

import (
	"encoding/json"
	"testing"
)

func TestStreamParseLine(t *testing.T) {
	cfg := parseConfig([]byte(""))
	state := newCCStreamState(cfg, "model", "chatcmpl-test")
	chunks := state.parseLine(`{"type":"text-delta","text":"hello"}`)
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	chunks = state.parseLine(`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":10,"outputTokens":20,"cachedInputTokens":5}}`)
	if len(chunks) != 1 {
		t.Fatalf("finish chunks = %d", len(chunks))
	}
	if state.outputTokens != 20 || state.cachedTokens != 5 {
		t.Fatalf("usage = %d/%d", state.outputTokens, state.cachedTokens)
	}
	if state.incompleteDetail() != "" {
		t.Fatalf("incomplete = %q", state.incompleteDetail())
	}
}

func TestAccumulatorBuild(t *testing.T) {
	acc := newCCAccumulator()
	acc.feed(`{"type":"reasoning-delta","text":"think"}`)
	acc.feed(`{"type":"text-delta","text":"answer"}`)
	acc.feed(`{"type":"finish","finishReason":"tool-calls","totalUsage":{"inputTokens":10,"outputTokens":2,"cachedInputTokens":0}}`)
	raw := acc.build("m", "id", 1)
	msg := asMap(asMap(asSlice(raw["choices"])[0])["message"])
	if asString(msg["content"]) != "answer" || asString(msg["reasoning_content"]) != "think" {
		t.Fatalf("message = %#v", msg)
	}
	usage := asMap(raw["usage"])
	if asNumber(usage["total_tokens"]) != 12 {
		t.Fatalf("usage = %#v", usage)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encodeJSON(raw), &decoded); err != nil {
		t.Fatal(err)
	}
}

func TestMapFinishReason(t *testing.T) {
	cases := map[string]string{
		"max_output_tokens":             "length",
		"model_context_window_exceeded": "length",
		"tool-calls":                    "tool_calls",
		"upstream_error":                "upstream_error",
		"pause_turn":                    "pause_turn",
	}
	for input, want := range cases {
		if got := mapFinishReason(input); got != want {
			t.Fatalf("mapFinishReason(%q) = %q, want %q", input, got, want)
		}
	}
}
