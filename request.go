package plugin

import (
	"strings"
	"time"
)

const (
	ccConfigPlatform = "win32"
	ccConfigArch     = "x64"
)

// buildCcRequest converts an OpenAI chat-completions payload into the Command
// Code CLI envelope used by /alpha/generate.
func buildCcRequest(payload map[string]any, cfg *pluginConfig) map[string]any {
	messages := asSlice(payload["messages"])
	maxTokens := asNumber(payload["max_tokens"])
	if maxTokens <= 0 {
		maxTokens = 64000
	}
	if maxTokens > 200000 {
		maxTokens = 200000
	}

	systemBlocks := systemBlocksFromMessages(messages)
	chatMessages := make([]any, 0, len(messages))
	toolNameMap := make(map[string]string)
	for _, msgValue := range messages {
		msg := asMap(msgValue)
		role := strings.TrimSpace(asString(msg["role"]))
		if role == "system" || role == "developer" {
			continue
		}
		if role == "assistant" {
			for _, tcValue := range asSlice(msg["tool_calls"]) {
				tc := asMap(tcValue)
				if id := asString(tc["id"]); id != "" {
					toolNameMap[id] = asString(asMap(tc["function"])["name"])
				}
			}
		}
		chatMessages = append(chatMessages, ccMessage(msg, toolNameMap))
	}

	hasCacheMarker := false
	for _, blockValue := range systemBlocks {
		if asMap(blockValue)["cache_control"] != nil {
			hasCacheMarker = true
		}
	}
	if !hasCacheMarker {
		for _, msgValue := range chatMessages {
			content := asSlice(asMap(msgValue)["content"])
			for _, partValue := range content {
				if asMap(partValue)["cache_control"] != nil {
					hasCacheMarker = true
					break
				}
			}
			if hasCacheMarker {
				break
			}
		}
	}
	promptCacheKey := asString(payload["prompt_cache_key"])
	if promptCacheKey != "" && !hasCacheMarker && len(systemBlocks) > 0 {
		last := asMap(systemBlocks[len(systemBlocks)-1])
		last["cache_control"] = map[string]any{"type": "ephemeral"}
		systemBlocks[len(systemBlocks)-1] = last
	}

	params := map[string]any{
		"model":      firstNonEmpty(asString(payload["model"]), "deepseek/deepseek-v4-flash"),
		"messages":   chatMessages,
		"max_tokens": int64(maxTokens),
		"stream":     true,
	}
	if len(systemBlocks) > 0 {
		params["system"] = systemBlocks
	} else if cfg.emptySystemPlaceholder() {
		params["system"] = []any{map[string]any{"type": "text", "text": " "}}
	}
	if temperature, exists := payload["temperature"]; exists {
		params["temperature"] = temperature
	}
	if reasoningEffort, exists := payload["reasoning_effort"]; exists {
		params["reasoning_effort"] = reasoningEffort
	}
	params["tools"] = wireTools(asSlice(payload["tools"]))
	if toolChoice, exists := payload["tool_choice"]; exists {
		params["tool_choice"] = wireToolChoice(toolChoice)
	}
	if parallel, exists := payload["parallel_tool_calls"]; exists {
		params["parallel_tool_calls"] = parallel
	}

	profile := defaultDeviceProfile(cfg)
	body := map[string]any{
		"config": map[string]any{
			"workingDir":    profile.projectDir,
			"date":          time.Now().UTC().Format("2006-01-02"),
			"environment":   profile.platform,
			"structure":     []any{},
			"isGitRepo":     false,
			"currentBranch": "",
			"mainBranch":    "",
			"gitStatus":     "",
			"recentCommits": []any{},
		},
		"memory":         nil,
		"taste":          nil,
		"skills":         nil,
		"permissionMode": "standard",
		"mode":           cfgCLIMode(cfg),
		"params":         params,
	}
	return body
}

func systemBlocksFromMessages(messages []any) []any {
	var blocks []any
	for _, msgValue := range messages {
		msg := asMap(msgValue)
		role := strings.TrimSpace(asString(msg["role"]))
		if role != "system" && role != "developer" {
			continue
		}
		switch content := msg["content"].(type) {
		case string:
			if content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": content})
			}
		case []any:
			for _, partValue := range content {
				part := asMap(partValue)
				text := asString(part["text"])
				if text == "" && part["content"] != nil {
					text = asString(part["content"])
				}
				if text == "" && part["cache_control"] == nil {
					continue
				}
				block := map[string]any{"type": "text", "text": text}
				if cc := part["cache_control"]; cc != nil {
					block["cache_control"] = cc
				}
				blocks = append(blocks, block)
			}
		default:
			if msg["content"] != nil {
				blocks = append(blocks, map[string]any{"type": "text", "text": asString(msg["content"])})
			}
		}
	}
	for i := 0; i < len(blocks)-1; i++ {
		block := asMap(blocks[i])
		block["text"] = asString(block["text"]) + "\n"
		blocks[i] = block
	}
	return blocks
}

func ccMessage(msg map[string]any, toolNameMap map[string]string) map[string]any {
	role := strings.TrimSpace(asString(msg["role"]))
	switch role {
	case "user":
		return map[string]any{"role": "user", "content": ccUserContent(msg["content"])}
	case "assistant":
		return ccAssistantMessage(msg)
	case "tool":
		callID := asString(msg["tool_call_id"])
		name := toolNameMap[callID]
		if name == "" {
			name = asString(msg["name"])
		}
		return map[string]any{
			"role": "tool",
			"content": []any{map[string]any{
				"type":       "tool-result",
				"toolCallId": callID,
				"toolName":   name,
				"output":     map[string]any{"type": "text", "value": toWireToolOutput(msg["content"])},
			}},
		}
	default:
		return map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": asString(msg["content"])}}}
	}
}

func ccUserContent(content any) []any {
	if text, ok := content.(string); ok {
		return []any{map[string]any{"type": "text", "text": text}}
	}
	if parts, ok := content.([]any); ok {
		out := make([]any, 0, len(parts))
		for _, partValue := range parts {
			part := asMap(partValue)
			if part["type"] == "image_url" {
				url := asString(asMap(part["image_url"])["url"])
				imagePart := map[string]any{"type": "image", "image": url}
				if idx := strings.Index(url, ";"); idx > 0 && strings.HasPrefix(url, "data:") {
					mediaType := strings.TrimPrefix(url[5:idx], "")
					if mediaType != "" {
						imagePart["mimeType"] = mediaType
					}
				}
				out = append(out, imagePart)
				continue
			}
			out = append(out, part)
		}
		return out
	}
	return []any{map[string]any{"type": "text", "text": asString(content)}}
}

func ccAssistantMessage(msg map[string]any) map[string]any {
	var parts []any
	if reasoning := asString(msg["reasoning_content"]); reasoning != "" {
		parts = append(parts, map[string]any{"type": "reasoning", "text": reasoning})
	}
	switch content := msg["content"].(type) {
	case string:
		if content != "" {
			parts = append(parts, map[string]any{"type": "text", "text": content})
		}
	case []any:
		for _, partValue := range content {
			part := asMap(partValue)
			switch asString(part["type"]) {
			case "text":
				parts = append(parts, part)
			case "reasoning":
				if asString(msg["reasoning_content"]) == "" {
					parts = append(parts, part)
				}
			}
		}
	}
	for _, tcValue := range asSlice(msg["tool_calls"]) {
		tc := asMap(tcValue)
		fn := asMap(tc["function"])
		args := fn["arguments"]
		parsed := map[string]any{}
		if text, ok := args.(string); ok {
			_ = jsonUnmarshal([]byte(text), &parsed)
		} else if args != nil {
			if m, ok := args.(map[string]any); ok {
				parsed = m
			}
		}
		parts = append(parts, map[string]any{
			"type":       "tool-call",
			"toolCallId": asString(tc["id"]),
			"toolName":   asString(fn["name"]),
			"input":      parsed,
		})
	}
	return map[string]any{"role": "assistant", "content": parts}
}

func toWireToolOutput(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		var parts []string
		for _, partValue := range value {
			part := asMap(partValue)
			if part["type"] == "text" {
				parts = append(parts, asString(part["text"]))
			}
		}
		return strings.Join(parts, "\n")
	default:
		if content == nil {
			return ""
		}
		return asString(content)
	}
}

func wireTools(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, toolValue := range tools {
		tool := asMap(toolValue)
		fn := asMap(tool["function"])
		name := asString(fn["name"])
		if name == "" {
			name = asString(tool["name"])
		}
		inputSchema := fn["parameters"]
		if inputSchema == nil {
			inputSchema = tool["input_schema"]
		}
		if inputSchema == nil {
			inputSchema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"name":         toWireToolName(name),
			"description":  firstNonEmpty(asString(fn["description"]), asString(tool["description"])),
			"input_schema": inputSchema,
		})
	}
	return out
}

var toolNameAliases = map[string]string{
	"bash_output":         "shell_output",
	"task_output":         "shell_output",
	"tool_search":         "search_tools",
	"read_multiple_files": "read_file",
}

func toWireToolName(name string) string {
	if aliased := toolNameAliases[name]; aliased != "" {
		return aliased
	}
	return name
}

func wireToolChoice(value any) any {
	switch choice := value.(type) {
	case string:
		mapped := map[string]string{"auto": "auto", "none": "none", "required": "any"}
		if next, ok := mapped[choice]; ok {
			return map[string]any{"type": next}
		}
		return map[string]any{"type": "auto"}
	case map[string]any:
		if asString(choice["type"]) == "function" {
			return map[string]any{"type": "tool", "name": asString(asMap(choice["function"])["name"])}
		}
		return choice
	default:
		return value
	}
}

func cfgCLIMode(cfg *pluginConfig) string {
	if cfg != nil && strings.TrimSpace(cfg.CLIMode) != "" {
		return cfg.CLIMode
	}
	return "agent"
}

func slugifyProjectPath(path string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(path) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "root"
	}
	return out
}
