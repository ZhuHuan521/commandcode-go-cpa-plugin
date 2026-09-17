package plugin

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

func convertResponsesToChat(resp map[string]any) map[string]any {
	var messages []any
	if instructions, ok := resp["instructions"]; ok && instructions != nil {
		text := responsesTextOf(instructions)
		if text != "" {
			messages = append(messages, map[string]any{"role": "system", "content": text})
		}
	}

	var pending map[string]any
	ensurePending := func() map[string]any {
		if pending != nil {
			return pending
		}
		pending = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{}}
		return pending
	}
	flushPending := func() {
		if pending == nil {
			return
		}
		calls := asSlice(pending["tool_calls"])
		if len(calls) == 0 {
			delete(pending, "tool_calls")
		}
		if asString(pending["reasoning_content"]) == "" {
			delete(pending, "reasoning_content")
		}
		if pending["content"] == nil && pending["tool_calls"] == nil {
			pending = nil
			return
		}
		messages = append(messages, pending)
		pending = nil
	}

	input := resp["input"]
	if text, ok := input.(string); ok {
		messages = append(messages, map[string]any{"role": "user", "content": text})
	} else if items, ok := input.([]any); ok {
		for _, itemValue := range items {
			item := asMap(itemValue)
			itemType := asString(item["type"])
			if itemType == "" && item["role"] != nil {
				itemType = "message"
			}
			switch itemType {
			case "reasoning":
				if text := responsesReasoningOf(item); text != "" {
					ensurePending()["reasoning_content"] = text
				}
			case "message":
				text := responsesTextOf(item["content"])
				switch item["role"] {
				case "assistant":
					if text != "" {
						ensurePending()["content"] = text
					}
				case "system", "developer":
					flushPending()
					if text != "" {
						messages = append(messages, map[string]any{"role": "system", "content": text})
					}
				default:
					flushPending()
					if text != "" {
						messages = append(messages, map[string]any{"role": "user", "content": text})
					}
				}
			case "function_call":
				ensurePending()["tool_calls"] = append(asSlice(ensurePending()["tool_calls"]), map[string]any{
					"id":       firstNonEmpty(asString(item["call_id"]), asString(item["id"]), "call_"+randomHex(4)),
					"type":     "function",
					"function": map[string]any{"name": asString(item["name"]), "arguments": asString(item["arguments"])},
				})
			case "function_call_output":
				flushPending()
				output := item["output"]
				text := ""
				if value, ok := output.(string); ok {
					text = value
				} else if output == nil {
					text = ""
				} else {
					text = mustJSON(output)
				}
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": asString(item["call_id"]),
					"content":      text,
				})
			}
		}
	}
	flushPending()

	out := map[string]any{
		"model":    asString(resp["model"]),
		"messages": messages,
		"stream":   asBool(resp["stream"]),
	}
	if tools := asSlice(resp["tools"]); len(tools) > 0 {
		var mapped []any
		for _, toolValue := range tools {
			tool := asMap(toolValue)
			if asString(tool["type"]) != "function" && asString(tool["name"]) == "" {
				continue
			}
			mapped = append(mapped, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        asString(tool["name"]),
					"description": asString(tool["description"]),
					"parameters":  tool["parameters"],
				},
			})
		}
		if len(mapped) > 0 {
			out["tools"] = mapped
		}
	}
	if choice, exists := resp["tool_choice"]; exists {
		if text, ok := choice.(string); ok {
			out["tool_choice"] = text
		} else if obj := asMap(choice); asString(obj["name"]) != "" {
			out["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": asString(obj["name"])}}
		}
	}
	if tokens, exists := resp["max_output_tokens"]; exists {
		out["max_tokens"] = tokens
	}
	if temp, exists := resp["temperature"]; exists {
		out["temperature"] = temp
	}
	if topP, exists := resp["top_p"]; exists {
		out["top_p"] = topP
	}
	if parallel, exists := resp["parallel_tool_calls"]; exists {
		out["parallel_tool_calls"] = parallel
	}
	if reasoning := asMap(resp["reasoning"]); asString(reasoning["effort"]) != "" {
		out["reasoning_effort"] = asString(reasoning["effort"])
	}
	return out
}

func responsesTextOf(content any) string {
	if text, ok := content.(string); ok {
		return text
	}
	if parts, ok := content.([]any); ok {
		var texts []string
		for _, partValue := range parts {
			if part := asMap(partValue); part != nil {
				texts = append(texts, asString(part["text"]))
			}
		}
		return strings.Join(texts, "")
	}
	return ""
}

func responsesReasoningOf(item map[string]any) string {
	if summary := asSlice(item["summary"]); len(summary) > 0 {
		return responsesTextOf(summary)
	}
	if content := asSlice(item["content"]); len(content) > 0 {
		return responsesTextOf(content)
	}
	return asString(item["text"])
}

func buildResponsesUsage(usage map[string]any, fallbackOutput int64) map[string]any {
	normalized := normalizeUsage(usage)
	input := int64(asNumber(normalized["inputTokens"]))
	output := int64(asNumber(normalized["outputTokens"]))
	if output == 0 {
		output = fallbackOutput
	}
	cached := int64(asNumber(normalized["cachedInputTokens"]))
	cacheWrite := int64(0)
	if details := asMap(normalized["inputTokenDetails"]); details != nil {
		cacheWrite = int64(asNumber(details["cacheWriteTokens"]))
	}
	return map[string]any{
		"input_tokens": input,
		"input_tokens_details": map[string]any{
			"cached_tokens":      cached,
			"cache_write_tokens": cacheWrite,
		},
		"output_tokens": output,
		"output_tokens_details": map[string]any{
			"reasoning_tokens": 0,
		},
		"total_tokens": input + output,
	}
}

func buildResponsesOutput(fullText, thinkingText string, toolCalls []map[string]any) []any {
	var output []any
	if thinkingText != "" {
		output = append(output, map[string]any{
			"type":    "reasoning",
			"id":      newResponsesID("rs_"),
			"summary": []any{map[string]any{"type": "summary_text", "text": thinkingText}},
		})
	}
	if fullText != "" {
		output = append(output, map[string]any{
			"type":    "message",
			"id":      newResponsesID("msg_"),
			"status":  "completed",
			"role":    "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": fullText, "annotations": []any{}}},
		})
	}
	for _, call := range toolCalls {
		rawArgs := "{}"
		if args, ok := call["arguments"].(string); ok {
			rawArgs = args
		} else if call["arguments"] != nil {
			rawArgs = mustJSON(call["arguments"])
		}
		output = append(output, map[string]any{
			"type":      "function_call",
			"id":        newResponsesID("fc_"),
			"call_id":   asString(call["id"]),
			"name":      asString(asMap(call["function"])["name"]),
			"arguments": rawArgs,
			"status":    "completed",
		})
	}
	return output
}

func buildResponsesObject(responseID, model string, created int64, fullText, thinkingText string, toolCalls []map[string]any, usage map[string]any, finishReason string) map[string]any {
	truncated := finishReason == "length"
	paused := finishReason == "pause_turn"
	status := "completed"
	if truncated || paused {
		status = "incomplete"
	}
	incompleteDetails := any(nil)
	if truncated {
		incompleteDetails = map[string]any{"reason": "max_output_tokens"}
	} else if paused {
		incompleteDetails = map[string]any{"reason": "pause_turn"}
	}
	return map[string]any{
		"id":                   responseID,
		"object":               "response",
		"created_at":           created,
		"status":               status,
		"completed_at":         nowUnix(),
		"error":                nil,
		"incomplete_details":   incompleteDetails,
		"input":                []any{},
		"instructions":         nil,
		"max_output_tokens":    nil,
		"model":                model,
		"output":               buildResponsesOutput(fullText, thinkingText, toolCalls),
		"output_text":          fullText,
		"parallel_tool_calls":  true,
		"previous_response_id": nil,
		"reasoning":            nil,
		"store":                false,
		"temperature":          1,
		"text":                 map[string]any{"format": map[string]any{"type": "text"}},
		"tool_choice":          "auto",
		"tools":                []any{},
		"top_p":                1,
		"truncation":           "disabled",
		"usage":                buildResponsesUsage(usage, 0),
		"user":                 nil,
		"metadata":             map[string]any{},
	}
}

func newResponsesID(prefix string) string {
	return prefix + strings.ReplaceAll(randomUUIDValue(), "-", "")[:24]
}

func randomUUIDValue() string {
	id, _ := randomUUID()
	if id == "" {
		return randomHex(16)
	}
	return id
}

func nowUnix() int64 {
	return timeNowUnix()
}

func sha256Sum(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	out := make([]byte, 0, len(sum))
	out = append(out, sum[:]...)
	return out
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
