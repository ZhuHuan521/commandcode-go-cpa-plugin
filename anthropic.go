package plugin

import "strings"

// convertAnthropicToOpenAI ports proxy.mjs convertAnthropicToOpenAI.
func convertAnthropicToOpenAI(anthropic map[string]any) map[string]any {
	systemPrompt := ""
	var systemBlocks []any
	if system, ok := anthropic["system"]; ok && system != nil {
		switch value := system.(type) {
		case string:
			systemPrompt = value
		case []any:
			for _, blockValue := range value {
				block := asMap(blockValue)
				if block["type"] != "text" {
					continue
				}
				out := map[string]any{"type": "text", "text": asString(block["text"])}
				if cc := block["cache_control"]; cc != nil {
					out["cache_control"] = cc
				}
				systemBlocks = append(systemBlocks, out)
			}
			parts := make([]string, 0, len(systemBlocks))
			for _, blockValue := range systemBlocks {
				parts = append(parts, asString(asMap(blockValue)["text"]))
			}
			systemPrompt = strings.Join(parts, "\n")
		}
	}

	toolNameFromID := make(map[string]string)
	var openAIMessages []any
	if systemPrompt != "" {
		if len(systemBlocks) > 0 {
			openAIMessages = append(openAIMessages, map[string]any{"role": "system", "content": systemBlocks})
		} else {
			openAIMessages = append(openAIMessages, map[string]any{"role": "system", "content": systemPrompt})
		}
	}

	for _, msgValue := range asSlice(anthropic["messages"]) {
		msg := asMap(msgValue)
		role := strings.TrimSpace(asString(msg["role"]))
		if role == "assistant" {
			textContent := ""
			thinkingContent := ""
			var textParts []any
			textHasCache := false
			var toolCalls []any
			content := asSlice(msg["content"])
			if len(content) == 0 && msg["content"] != nil {
				content = []any{map[string]any{"type": "text", "text": asString(msg["content"])}}
			}
			for _, blockValue := range content {
				block := asMap(blockValue)
				switch block["type"] {
				case "text":
					text := asString(block["text"])
					textContent += text
					part := map[string]any{"type": "text", "text": text}
					if cc := block["cache_control"]; cc != nil {
						part["cache_control"] = cc
						textHasCache = true
					}
					textParts = append(textParts, part)
				case "thinking":
					thinkingContent += asString(block["thinking"])
				case "tool_use":
					id := asString(block["id"])
					name := asString(block["name"])
					toolNameFromID[id] = name
					toolCalls = append(toolCalls, map[string]any{
						"id":       id,
						"type":     "function",
						"function": map[string]any{"name": name, "arguments": mustJSON(block["input"])},
					})
				}
			}
			assistant := map[string]any{"role": "assistant"}
			if len(textParts) > 1 || textHasCache {
				assistant["content"] = textParts
			} else if textContent != "" {
				assistant["content"] = textContent
			} else {
				assistant["content"] = nil
			}
			if thinkingContent != "" {
				assistant["reasoning_content"] = thinkingContent
			}
			if len(toolCalls) > 0 {
				assistant["tool_calls"] = toolCalls
			}
			openAIMessages = append(openAIMessages, assistant)
			continue
		}

		if role == "user" {
			textContent := ""
			var parts []any
			textHasCache := false
			var toolResults []map[string]any
			content := asSlice(msg["content"])
			if len(content) == 0 && msg["content"] != nil {
				if text, ok := msg["content"].(string); ok {
					textContent = text
				}
			}
			for _, blockValue := range content {
				block := asMap(blockValue)
				switch block["type"] {
				case "text":
					text := asString(block["text"])
					textContent += text
					part := map[string]any{"type": "text", "text": text}
					if cc := block["cache_control"]; cc != nil {
						part["cache_control"] = cc
						textHasCache = true
					}
					parts = append(parts, part)
				case "image":
					source := asMap(block["source"])
					url := ""
					if source["type"] == "base64" && asString(source["data"]) != "" {
						url = "data:" + firstNonEmpty(asString(source["media_type"]), "image/png") + ";base64," + asString(source["data"])
					} else {
						url = asString(source["url"])
					}
					if url != "" {
						parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
					}
				case "tool_result":
					toolResults = append(toolResults, block)
				}
			}
			for _, result := range toolResults {
				toolContent := ""
				switch value := result["content"].(type) {
				case string:
					toolContent = value
				case []any:
					var texts []string
					for _, partValue := range value {
						part := asMap(partValue)
						texts = append(texts, asString(part["text"]))
					}
					toolContent = strings.Join(texts, "\n")
				default:
					if result["content"] != nil {
						toolContent = asString(result["content"])
					}
				}
				toolMsg := map[string]any{
					"role":         "tool",
					"tool_call_id": asString(result["tool_use_id"]),
					"content":      toolContent,
				}
				if name := toolNameFromID[asString(result["tool_use_id"])]; name != "" {
					toolMsg["name"] = name
				}
				openAIMessages = append(openAIMessages, toolMsg)
			}
			if len(parts) > 0 || textContent != "" {
				singleText := len(parts) <= 1 && (len(parts) == 0 || asString(asMap(parts[0])["type"]) == "text") && !textHasCache
				if singleText {
					openAIMessages = append(openAIMessages, map[string]any{"role": "user", "content": textContent})
				} else {
					openAIMessages = append(openAIMessages, map[string]any{"role": "user", "content": parts})
				}
			}
		}
	}

	out := map[string]any{
		"model":      firstNonEmpty(asString(anthropic["model"]), "deepseek/deepseek-v4-flash"),
		"messages":   openAIMessages,
		"max_tokens": int64(nonZeroInt(asNumber(anthropic["max_tokens"]), 64000)),
		"stream":     asBool(anthropic["stream"]),
	}
	if tools := asSlice(anthropic["tools"]); len(tools) > 0 {
		var mapped []any
		for _, toolValue := range tools {
			tool := asMap(toolValue)
			mapped = append(mapped, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        asString(tool["name"]),
					"description": asString(tool["description"]),
					"parameters":  tool["input_schema"],
				},
			})
		}
		out["tools"] = mapped
	}
	if choice, exists := anthropic["tool_choice"]; exists && choice != nil {
		tc := asMap(choice)
		switch tc["type"] {
		case "auto":
			out["tool_choice"] = "auto"
		case "any":
			out["tool_choice"] = "required"
		case "tool":
			out["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": asString(tc["name"])}}
		case "none":
			out["tool_choice"] = "none"
		default:
			out["tool_choice"] = "auto"
		}
	}
	if temperature, exists := anthropic["temperature"]; exists {
		out["temperature"] = temperature
	}
	if topP, exists := anthropic["top_p"]; exists {
		out["top_p"] = topP
	}
	if stops := asSlice(anthropic["stop_sequences"]); len(stops) > 0 {
		out["stop"] = stops
	}
	if metadata := asMap(anthropic["metadata"]); asString(metadata["user_id"]) != "" {
		out["user"] = asString(metadata["user_id"])
	}
	if thinking, exists := anthropic["thinking"]; exists && thinking != nil {
		config := asMap(thinking)
		switch config["type"] {
		case "disabled", "none":
		case "adaptive":
			out["reasoning_effort"] = firstNonEmpty(asString(config["effort"]), "medium")
		default:
			if budget := int(asNumber(config["budget_tokens"])); budget > 0 {
				switch {
				case budget >= 10000:
					out["reasoning_effort"] = "high"
				case budget >= 5000:
					out["reasoning_effort"] = "medium"
				default:
					out["reasoning_effort"] = "low"
				}
			}
		}
	}
	return out
}

func nonZeroInt(value, fallback float64) float64 {
	if value <= 0 {
		return fallback
	}
	return value
}

func mapAnthropicStopReason(reason string) string {
	switch reason {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "stop":
		return "end_turn"
	case "pause_turn":
		return "pause_turn"
	case "refusal":
		return "refusal"
	default:
		return "end_turn"
	}
}

func fakeThinkingSignature(thinkingText string) string {
	seed := sha256Sum(thinkingText)
	payload := append([]byte{0x12, byte(len(seed))}, seed...)
	return base64Encode(payload)
}
