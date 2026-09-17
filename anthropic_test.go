package plugin

import "testing"

func TestConvertAnthropicToOpenAI(t *testing.T) {
	anthropic := decodeObject([]byte(`{
		"model":"claude-sonnet-4-6",
		"max_tokens":1000,
		"system":"You are helpful.",
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[{"type":"thinking","thinking":"reason"},{"type":"text","text":"hi"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}
		]
	}`))
	openAI := convertAnthropicToOpenAI(anthropic)
	messages := asSlice(openAI["messages"])
	if len(messages) != 4 {
		t.Fatalf("messages = %d", len(messages))
	}
	assistant := asMap(messages[2])
	if asString(assistant["reasoning_content"]) != "reason" {
		t.Fatalf("assistant = %#v", assistant)
	}
	tool := asMap(messages[3])
	if asString(tool["role"]) != "tool" || asString(tool["tool_call_id"]) != "toolu_1" {
		t.Fatalf("tool = %#v", tool)
	}
}
