package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildCCRequestSystemAndTools(t *testing.T) {
	cfg := parseConfig([]byte(""))
	payload := decodeObject([]byte(`{
		"model":"deepseek-v4-flash",
		"messages":[
			{"role":"system","content":"Be helpful"},
			{"role":"user","content":"hi"}
		],
		"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}],
		"reasoning_effort":"high"
	}`))
	cc := buildCcRequest(payload, cfg)
	params := asMap(cc["params"])
	if asString(params["model"]) != "deepseek-v4-flash" {
		t.Fatalf("model = %q", asString(params["model"]))
	}
	if asBool(params["stream"]) != true {
		t.Fatal("CC envelope must always stream")
	}
	system := asSlice(params["system"])
	if len(system) == 0 || asString(asMap(system[0])["text"]) != "Be helpful" {
		t.Fatalf("system blocks = %#v", system)
	}
	tools := asSlice(params["tools"])
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	tool := asMap(tools[0])
	if asString(tool["name"]) != "get_weather" {
		t.Fatalf("tool name = %#v", tool)
	}
}

func TestBuildCCRequestMultimodal(t *testing.T) {
	cfg := parseConfig([]byte(""))
	payload := decodeObject([]byte(`{
		"model":"xiaomi/mimo-v2.5",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"what is this"},
			{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,AAAA"}}
		]}]
	}`))
	cc := buildCcRequest(payload, cfg)
	content := asSlice(asMap(asSlice(asMap(cc["params"])["messages"])[0])["content"])
	if len(content) != 2 {
		t.Fatalf("content = %#v", content)
	}
	image := asMap(content[1])
	if asString(image["type"]) != "image" || asString(image["mimeType"]) != "image/jpeg" {
		t.Fatalf("image part = %#v", image)
	}
}

func TestBuildCCRequestPreservesCachePlaceholder(t *testing.T) {
	cfg := parseConfig([]byte("empty_system_placeholder: false"))
	payload := decodeObject([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	cc := buildCcRequest(payload, cfg)
	params := asMap(cc["params"])
	if _, ok := params["system"]; ok {
		t.Fatalf("system should be absent when placeholder disabled: %#v", params)
	}
	raw, _ := json.Marshal(cc)
	if !json.Valid(raw) {
		t.Fatal("CC envelope is not valid JSON")
	}
}

func TestBuildBodyThreadID(t *testing.T) {
	cfg := parseConfig([]byte(""))
	executor := NewExecutor(cfg, nil)
	body := executor.buildBody(map[string]any{"model": "m", "messages": []any{}}, "c5d69e42-3f2f-4b62-a1a2-8bbff0c7e5b7")
	if !strings.Contains(string(body), `"threadId":"c5d69e42-3f2f-4b62-a1a2-8bbff0c7e5b7"`) {
		t.Fatalf("threadId missing: %s", body)
	}
	nonUUID := executor.buildBody(map[string]any{"model": "m", "messages": []any{}}, "client-session")
	if strings.Contains(string(nonUUID), "threadId") {
		t.Fatalf("non-UUID session must not enter threadId: %s", nonUUID)
	}
}
