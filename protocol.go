package plugin

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ccToolCall struct {
	ID    string
	Name  string
	Input any
}

type ccUpstreamError struct {
	Status         int
	Code           string
	Type           string
	Message        string
	ReportedStatus int
	RetryAfter     int
}

type ccEvent struct {
	Type         string
	Text         string
	ToolCall     *ccToolCall
	FinishReason string
	Usage        map[string]any
	Error        *ccUpstreamError
}

func parseCCEvent(line string) (*ccEvent, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed == "[DONE]" || strings.HasPrefix(trimmed, ":") {
		return nil, false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, false
	}
	eventType := strings.TrimSpace(asString(raw["type"]))
	if eventType == "" {
		return nil, false
	}
	event := &ccEvent{Type: eventType}
	switch eventType {
	case "text-delta":
		event.Text = firstNonEmpty(asString(raw["text"]), asString(raw["delta"]))
	case "reasoning-delta":
		event.Text = asString(raw["text"])
	case "tool-call":
		event.ToolCall = &ccToolCall{
			ID:    asString(raw["toolCallId"]),
			Name:  asString(raw["toolName"]),
			Input: raw["input"],
		}
	case "finish-step", "finish":
		event.FinishReason = asString(raw["finishReason"])
		if usage, ok := raw["totalUsage"].(map[string]any); ok {
			event.Usage = usage
		} else if usage, ok := raw["usage"].(map[string]any); ok {
			event.Usage = usage
		}
	case "error":
		event.Error = mapCCEventError(raw)
	}
	return event, true
}

type ccStreamState struct {
	cfg           *pluginConfig
	model         string
	completionID  string
	created       int64
	sawFinish     bool
	chunkIndex    int
	toolCallIndex int
	finishReason  string
	usage         map[string]any
	outputTokens  int64
	inputTokens   int64
	cachedTokens  int64
	upstreamError *ccUpstreamError
	lastCCEvent   string
}

func newCCStreamState(cfg *pluginConfig, model, completionID string) *ccStreamState {
	return &ccStreamState{
		cfg:          cfg,
		model:        model,
		completionID: completionID,
		created:      time.Now().Unix(),
	}
}

func (s *ccStreamState) parseLine(line string) []map[string]any {
	event, ok := parseCCEvent(line)
	if !ok || event == nil {
		return nil
	}
	s.lastCCEvent = event.Type
	var out []map[string]any
	if event.Error != nil {
		s.upstreamError = event.Error
		return nil
	}
	switch event.Type {
	case "text-delta", "reasoning-delta":
		if event.Text == "" {
			return nil
		}
		delta := map[string]any{}
		if s.chunkIndex == 0 {
			delta["role"] = "assistant"
		}
		if event.Type == "text-delta" {
			delta["content"] = event.Text
		} else {
			delta["reasoning_content"] = event.Text
		}
		out = append(out, s.makeChunk(delta, "", nil))
		s.chunkIndex++
	case "tool-call":
		call := event.ToolCall
		if call == nil {
			return nil
		}
		id := call.ID
		if id == "" {
			id = "call_" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "_" + itoa(int64(s.toolCallIndex))
		}
		args := "{}"
		if text, ok := call.Input.(string); ok {
			args = text
		} else if call.Input != nil {
			args = mustJSON(call.Input)
		}
		entry := map[string]any{
			"index": s.toolCallIndex,
			"id":    id,
			"type":  "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": args,
			},
		}
		delta := map[string]any{"tool_calls": []any{entry}}
		if s.chunkIndex == 0 {
			delta["role"] = "assistant"
			delta["content"] = nil
		}
		out = append(out, s.makeChunk(delta, "", nil))
		s.chunkIndex++
		s.toolCallIndex++
	case "finish-step", "finish":
		s.sawFinish = true
		if event.Type == "finish-step" {
			if event.FinishReason != "" {
				s.finishReason = mapFinishReason(event.FinishReason)
			}
			if event.Usage != nil {
				s.usage = event.Usage
			}
			return nil
		}
		finishReason := mapFinishReason(event.FinishReason)
		if s.finishReason != "" {
			finishReason = s.finishReason
		}
		if event.Usage != nil {
			s.usage = event.Usage
		}
		usage := normalizeUsage(s.usage)
		s.inputTokens = int64(asNumber(usage["inputTokens"]))
		s.outputTokens = int64(asNumber(usage["outputTokens"]))
		s.cachedTokens = int64(asNumber(usage["cachedInputTokens"]))
		out = append(out, s.makeChunk(map[string]any{}, toOpenAIFinishReason(finishReason), usage))
	default:
		return nil
	}
	return out
}

func (s *ccStreamState) makeChunk(delta map[string]any, finishReason string, usage map[string]any) map[string]any {
	chunk := map[string]any{
		"id":      s.completionID,
		"object":  "chat.completion.chunk",
		"created": s.cratedUnix(),
		"model":   s.model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReasonOrNil(finishReason),
		}},
	}
	if usage != nil {
		chunk["usage"] = openAIUsage(usage)
	}
	return chunk
}

func (s *ccStreamState) cratedUnix() int64 {
	if s.created == 0 {
		return time.Now().Unix()
	}
	return s.created
}

func (s *ccStreamState) incompleteDetail() string {
	if !s.sawFinish {
		return "no finish event"
	}
	if s.finishReason == "upstream_error" {
		return "provider reported an upstream connection failure"
	}
	return ""
}

func finishReasonOrNil(finish string) any {
	if finish == "" {
		return nil
	}
	return finish
}

func normalizeUsage(usage map[string]any) map[string]any {
	if usage == nil {
		return map[string]any{}
	}
	output := asNumber(usage["outputTokens"])
	if output == 0 {
		usage["inputTokens"] = 0
		usage["cachedInputTokens"] = 0
	}
	return usage
}

func openAIUsage(usage map[string]any) map[string]any {
	input := int64(asNumber(usage["inputTokens"]))
	output := int64(asNumber(usage["outputTokens"]))
	cached := int64(asNumber(usage["cachedInputTokens"]))
	return map[string]any{
		"prompt_tokens":     input,
		"completion_tokens": output,
		"total_tokens":      input + output,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": cached,
		},
	}
}

// ccAccumulator buffers a non-streaming /alpha/generate response.
type ccAccumulator struct {
	fullText      string
	thinkingText  string
	finishReason  string
	sawFinish     bool
	usage         map[string]any
	toolCalls     []map[string]any
	upstreamError *ccUpstreamError
	lastCCEvent   string
}

func newCCAccumulator() *ccAccumulator {
	return &ccAccumulator{finishReason: "stop"}
}

func (a *ccAccumulator) feed(line string) {
	event, ok := parseCCEvent(line)
	if !ok || event == nil {
		return
	}
	a.lastCCEvent = event.Type
	if event.Error != nil {
		a.upstreamError = event.Error
		return
	}
	switch event.Type {
	case "text-delta":
		a.fullText += event.Text
	case "reasoning-delta":
		a.thinkingText += event.Text
	case "tool-call":
		call := event.ToolCall
		if call == nil {
			return
		}
		args := "{}"
		if text, ok := call.Input.(string); ok {
			args = text
		} else if call.Input != nil {
			args = mustJSON(call.Input)
		}
		a.toolCalls = append(a.toolCalls, map[string]any{
			"id":       firstNonEmpty(call.ID, "call_"+strconv.FormatInt(time.Now().UnixMilli(), 10)),
			"type":     "function",
			"function": map[string]any{"name": call.Name, "arguments": args},
		})
	case "finish-step", "finish":
		a.sawFinish = true
		if event.FinishReason != "" {
			a.finishReason = mapFinishReason(event.FinishReason)
		}
		if event.Usage != nil {
			a.usage = event.Usage
		}
	}
}

func (a *ccAccumulator) incompleteDetail() string {
	if !a.sawFinish {
		return "no finish event"
	}
	if a.finishReason == "upstream_error" {
		return "provider reported an upstream connection failure"
	}
	return ""
}

func (a *ccAccumulator) outputTokens() int64 {
	if a.usage == nil {
		return 0
	}
	return int64(asNumber(normalizeUsage(a.usage)["outputTokens"]))
}

func (a *ccAccumulator) build(model, completionID string, created int64) map[string]any {
	usage := normalizeUsage(a.usage)
	message := map[string]any{
		"role":    "assistant",
		"content": a.fullText,
	}
	if len(a.toolCalls) > 0 {
		message["tool_calls"] = a.toolCalls
	}
	if a.thinkingText != "" {
		message["reasoning_content"] = a.thinkingText
	}
	if a.fullText == "" {
		message["content"] = nil
	}
	return map[string]any{
		"id":      completionID,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": toOpenAIFinishReason(a.finishReason),
		}},
		"usage": openAIUsage(usage),
	}
}

var statusMessagePattern = regexp.MustCompile(`^<(\d{3})>`)

var ccStatusMap = map[int]struct {
	Status int
	Type   string
}{
	400: {400, "invalid_request_error"},
	401: {401, "authentication_error"},
	402: {429, "rate_limit_error"},
	403: {401, "authentication_error"},
	404: {404, "not_found"},
	422: {400, "invalid_request_error"},
	429: {429, "rate_limit_error"},
	500: {502, "upstream_error"},
	502: {502, "upstream_error"},
	503: {503, "temporarily_unavailable"},
}

func mappedCCStatus(status int) (int, string) {
	if entry, ok := ccStatusMap[status]; ok {
		return entry.Status, entry.Type
	}
	return 502, "upstream_error"
}

func mapCCEventError(raw map[string]any) *ccUpstreamError {
	errObj := asMap(raw["error"])
	message := firstNonEmpty(asString(errObj["message"]), asString(raw["message"]), "Unknown CC error")
	code := firstNonEmpty(asString(errObj["code"]), asString(raw["code"]))
	reported := 0
	if match := statusMessagePattern.FindStringSubmatch(message); match != nil {
		reported, _ = strconv.Atoi(match[1])
	}
	if reported == 0 && errObj["statusCode"] != nil {
		reported = int(asNumber(errObj["statusCode"]))
	}
	status, errType := mappedCCStatus(reported)
	retryAfter := 0
	if status == 429 {
		retryAfter = 30
	}
	return &ccUpstreamError{
		Status:         status,
		Code:           code,
		Type:           errType,
		Message:        message,
		ReportedStatus: reported,
		RetryAfter:     retryAfter,
	}
}

func mapCCUpstreamError(status int, body []byte) *ccUpstreamError {
	mappedStatus, errType := mappedCCStatus(status)
	message := "CC API error (" + itoa(int64(status)) + ")"
	code := ""
	if len(strings.TrimSpace(string(body))) > 0 {
		var parsed struct {
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
			Code string `json:"code"`
		}
		if err := json.Unmarshal(body, &parsed); err == nil {
			message = firstNonEmpty(parsed.Error.Message, parsed.Message, message)
			code = firstNonEmpty(parsed.Error.Code, parsed.Code)
		} else {
			text := strings.TrimSpace(string(body))
			if len(text) > 200 {
				text = text[:200]
			}
			message = text
		}
	}
	retryAfter := 0
	if status == 429 {
		retryAfter = 30
	}
	return &ccUpstreamError{
		Status:         mappedStatus,
		Code:           code,
		Type:           errType,
		Message:        message,
		ReportedStatus: status,
		RetryAfter:     retryAfter,
	}
}

func mapFinishReason(reason string) string {
	r := strings.ToLower(strings.TrimSpace(reason))
	if r == "" {
		return "stop"
	}
	switch r {
	case "tool-calls", "tool_calls", "tool_use":
		return "tool_calls"
	case "length", "max_tokens", "max_output_tokens", "model_context_window_exceeded":
		return "length"
	}
	if m, _ := regexp.MatchString(`^(?:network|connection|upstream)[-_\s]?error$`, r); m {
		return "upstream_error"
	}
	return r
}

func toOpenAIFinishReason(reason string) string {
	if reason == "pause_turn" {
		return "length"
	}
	return reason
}

func incompleteUpstreamError(detail string) *ccUpstreamError {
	return &ccUpstreamError{
		Status:     502,
		Type:       "upstream_error",
		Message:    "Upstream stream ended without a completion finish (" + detail + ") - response was truncated",
		RetryAfter: 10,
	}
}
