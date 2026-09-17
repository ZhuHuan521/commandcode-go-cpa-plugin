package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	missingKeyMessage   = "commandcode executor: missing api key (router path passes nil auth; set plugins.configs.commandcode.api_key or api_keys in config.yaml)"
	claudeMessagesPath  = "/v1/messages"
	timeoutReduceHint   = "Response timeout - try reducing context length (summarize earlier messages)"
	timeoutPlainMessage = "Response timeout - request timed out"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type streamFramingPolicy uint8

const (
	framingBare streamFramingPolicy = iota
	framingClaude
)

type statusError struct {
	statusCode int
	msg        string
	body       []byte
	retryAfter int
}

func (e statusError) Error() string {
	if strings.TrimSpace(e.msg) != "" {
		return e.msg
	}
	if len(e.body) > 0 {
		return upstreamErrorMessage(e.body)
	}
	return fmt.Sprintf("status %d", e.statusCode)
}

func (e statusError) StatusCode() int { return e.statusCode }

func statusErrorFromCC(err *ccUpstreamError) error {
	if err == nil {
		return statusError{statusCode: http.StatusBadGateway, msg: "commandcode upstream error"}
	}
	return statusError{statusCode: err.Status, msg: err.Message, retryAfter: err.RetryAfter}
}

// Executor forwards alpha/generate requests through the host transport and
// normalizes the upstream NDJSON stream back into OpenAI-compatible payloads.
type Executor struct {
	cfg        *pluginConfig
	translator *Translator
	pool       *pool
	sessions   *sessionManager
	inflight   atomic.Int64

	timeoutsMu sync.Mutex
	timeouts   int
}

func NewExecutor(cfg *pluginConfig, translator *Translator) *Executor {
	if translator == nil {
		translator = NewTranslator(cfg)
	}
	return &Executor{
		cfg:        cfg,
		translator: translator,
		pool:       newPool(),
		sessions:   newSessionManager(cfg),
	}
}

func (e *Executor) Identifier() string { return Provider }

func (e *Executor) acquireInflight() error {
	limit := e.cfg.MaxInflight
	if limit <= 0 {
		return nil
	}
	if e.inflight.Add(1) > limit {
		e.inflight.Add(-1)
		return statusError{statusCode: http.StatusServiceUnavailable, msg: fmt.Sprintf("Too many concurrent requests (limit %d), retry shortly", limit), retryAfter: 5}
	}
	return nil
}

func (e *Executor) releaseInflight() {
	if e.cfg.MaxInflight > 0 {
		e.inflight.Add(-1)
	}
}

func (e *Executor) timeoutMessage() string {
	e.timeoutsMu.Lock()
	e.timeouts++
	count := e.timeouts
	e.timeoutsMu.Unlock()
	if count >= 3 {
		return timeoutReduceHint
	}
	return timeoutPlainMessage
}

func (e *Executor) resetTimeouts() {
	e.timeoutsMu.Lock()
	e.timeouts = 0
	e.timeoutsMu.Unlock()
}

func (e *Executor) endpoint() string {
	return e.cfg.baseURL() + "/alpha/generate"
}

func (e *Executor) upstreamHeaders(apiKey, sessionID string) map[string]string {
	headers := map[string]string{
		"Content-Type":           "application/json",
		"User-Agent":             "cli",
		"x-command-code-version": e.cfg.protocolVersion(),
		"x-cli-environment":      "production",
		"x-project-slug":         e.cfg.projectSlug(),
		"x-taste-learning":       "false",
		"x-session-id":           sessionID,
		"Authorization":          "Bearer " + strings.TrimSpace(apiKey),
		"traceparent":            generateTraceparent(),
		"Accept":                 "text/event-stream",
	}
	if e.cfg.ZDR {
		headers["x-cmd-zdr"] = "1"
	}
	return headers
}

func generateTraceparent() string {
	return "00-" + randomHex(16) + "-" + randomHex(8) + "-01"
}

func (e *Executor) openAIRequest(req pluginapi.ExecutorRequest) (map[string]any, string) {
	payload := decodeObject(req.Payload)
	e.translator.normalizeRequestModelMap(req.Model, payload)
	return payload, asString(payload["prompt_cache_key"])
}

func (e *Executor) buildBody(openAI map[string]any, sessionID string) []byte {
	body := buildCcRequest(openAI, e.cfg)
	if isUUID(sessionID) {
		body["threadId"] = sessionID
	}
	return encodeJSON(body)
}

func isUUID(value string) bool {
	return uuidPattern.MatchString(value)
}

func (e *Executor) Execute(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	if err := e.acquireInflight(); err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	defer e.releaseInflight()

	members := e.cfg.members(req)
	if len(members) == 0 {
		return pluginapi.ExecutorResponse{}, statusError{statusCode: http.StatusUnauthorized, msg: missingKeyMessage}
	}
	openAI, promptCacheKey := e.openAIRequest(req)
	model := firstNonEmpty(asString(openAI["model"]), "deepseek/deepseek-v4-flash")
	completionID := "chatcmpl-" + randomHex(6)
	created := nowUnix()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var lastErr error
	for _, idx := range e.pool.order(members) {
		member := members[idx]
		client, err := e.pool.clientFor(idx, member, req.HTTPClient)
		if err != nil {
			lastErr = err
			continue
		}
		apiKey := strings.TrimSpace(member.Key)
		e.sessions.ensureInitialized(ctx, client, apiKey)
		sessionID := e.sessions.sessionFor(req, apiKey, promptCacheKey)
		ccBody := e.buildBody(openAI, sessionID)
		status, _, chunks, err := client.doStream(ctx, e.endpoint(), e.upstreamHeaders(apiKey, sessionID), ccBody)
		if err != nil {
			lastErr = err
			if retryable(0, err) && ctx.Err() == nil {
				continue
			}
			return pluginapi.ExecutorResponse{}, err
		}
		if status < 200 || status >= 300 {
			lastErr = statusErrorFromCC(mapCCUpstreamError(status, readStreamErrorBody(ctx, chunks)))
			if retryable(status, nil) && ctx.Err() == nil {
				continue
			}
			return pluginapi.ExecutorResponse{}, lastErr
		}
		payload, errFinish := e.collectNonStream(ctx, chunks, model, completionID, created)
		if errFinish != nil {
			return pluginapi.ExecutorResponse{}, errFinish
		}
		e.resetTimeouts()
		return pluginapi.ExecutorResponse{Payload: payload}, nil
	}
	if lastErr != nil {
		return pluginapi.ExecutorResponse{}, lastErr
	}
	return pluginapi.ExecutorResponse{}, statusError{statusCode: http.StatusBadGateway, msg: "commandcode executor: all pool members failed"}
}

func (e *Executor) collectNonStream(ctx context.Context, chunks <-chan pluginapi.HTTPStreamChunk, model, completionID string, created int64) ([]byte, error) {
	acc := newCCAccumulator()
	var pending []byte
	timer := time.NewTimer(e.cfg.nonStreamIdle())
	defer timer.Stop()
	process := func() {
		lines := bytes.Split(pending, []byte{'\n'})
		pending = append([]byte(nil), lines[len(lines)-1]...)
		for _, line := range lines[:len(lines)-1] {
			acc.feed(string(line))
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, statusError{statusCode: http.StatusTooManyRequests, msg: e.timeoutMessage(), retryAfter: 5}
		case chunk, ok := <-chunks:
			if !ok {
				process()
				return e.finishNonStream(acc, model, completionID, created)
			}
			if chunk.Err != nil {
				return nil, statusError{statusCode: http.StatusBadGateway, msg: "Upstream stream error: " + chunk.Err.Error(), retryAfter: 10}
			}
			timer.Reset(e.cfg.nonStreamIdle())
			pending = append(pending, chunk.Payload...)
			if bytes.IndexByte(chunk.Payload, '\n') >= 0 {
				process()
			}
		}
	}
}

func (e *Executor) finishNonStream(acc *ccAccumulator, model, completionID string, created int64) ([]byte, error) {
	if acc.upstreamError != nil {
		return nil, statusErrorFromCC(acc.upstreamError)
	}
	if detail := acc.incompleteDetail(); detail != "" {
		return nil, statusErrorFromCC(incompleteUpstreamError(detail))
	}
	if acc.outputTokens() == 0 {
		return nil, statusError{statusCode: http.StatusTooManyRequests, msg: "Empty response from upstream (zero output tokens)", retryAfter: 10}
	}
	return encodeJSON(acc.build(model, completionID, created)), nil
}

func (e *Executor) ExecuteStream(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	if err := e.acquireInflight(); err != nil {
		return pluginapi.ExecutorStreamResponse{}, err
	}
	releaseOnReturn := true
	defer func() {
		if releaseOnReturn {
			e.releaseInflight()
		}
	}()

	members := e.cfg.members(req)
	if len(members) == 0 {
		return pluginapi.ExecutorStreamResponse{}, statusError{statusCode: http.StatusUnauthorized, msg: missingKeyMessage}
	}
	openAI, promptCacheKey := e.openAIRequest(req)
	model := firstNonEmpty(asString(openAI["model"]), "deepseek/deepseek-v4-flash")
	completionID := "chatcmpl-" + randomHex(6)

	ctx, cancel := context.WithCancel(ctx)
	streamStarted := false
	defer func() {
		if !streamStarted {
			cancel()
		}
	}()
	var lastErr error
	for _, idx := range e.pool.order(members) {
		member := members[idx]
		client, err := e.pool.clientFor(idx, member, req.HTTPClient)
		if err != nil {
			lastErr = err
			continue
		}
		apiKey := strings.TrimSpace(member.Key)
		e.sessions.ensureInitialized(ctx, client, apiKey)
		sessionID := e.sessions.sessionFor(req, apiKey, promptCacheKey)
		ccBody := e.buildBody(openAI, sessionID)
		status, headers, chunks, err := client.doStream(ctx, e.endpoint(), e.upstreamHeaders(apiKey, sessionID), ccBody)
		if err != nil {
			lastErr = err
			if retryable(0, err) && ctx.Err() == nil {
				continue
			}
			return pluginapi.ExecutorStreamResponse{}, err
		}
		if status < 200 || status >= 300 {
			lastErr = statusErrorFromCC(mapCCUpstreamError(status, readStreamErrorBody(ctx, chunks)))
			if retryable(status, nil) && ctx.Err() == nil {
				continue
			}
			return pluginapi.ExecutorStreamResponse{}, lastErr
		}
		out := e.streamLoop(ctx, cancel, chunks, model, completionID, streamFramingForRequest(req))
		streamStarted = true
		releaseOnReturn = false
		return pluginapi.ExecutorStreamResponse{Headers: headers, Chunks: out}, nil
	}
	if lastErr != nil {
		return pluginapi.ExecutorStreamResponse{}, lastErr
	}
	return pluginapi.ExecutorStreamResponse{}, statusError{statusCode: http.StatusBadGateway, msg: "commandcode executor: all pool members failed"}
}

func streamFramingForRequest(req pluginapi.ExecutorRequest) streamFramingPolicy {
	path, ok := req.Metadata[coreexecutor.RequestPathMetadataKey].(string)
	if ok && path == claudeMessagesPath {
		return framingClaude
	}
	return framingBare
}

func (policy streamFramingPolicy) apply(payload []byte) []byte {
	if policy != framingClaude {
		return payload
	}
	out := make([]byte, 0, len(payload)+len("data: "))
	out = append(out, "data: "...)
	out = append(out, payload...)
	return out
}

func (e *Executor) streamLoop(ctx context.Context, cancel context.CancelFunc, chunks <-chan pluginapi.HTTPStreamChunk, model, completionID string, framing streamFramingPolicy) <-chan pluginapi.ExecutorStreamChunk {
	out := make(chan pluginapi.ExecutorStreamChunk, 1)
	go func() {
		defer close(out)
		defer e.releaseInflight()
		defer cancel()
		state := newCCStreamState(e.cfg, model, completionID)
		var pending []byte
		timer := time.NewTimer(e.cfg.streamIdle())
		defer timer.Stop()
		emitErr := func(err error) {
			select {
			case <-ctx.Done():
			case out <- pluginapi.ExecutorStreamChunk{Err: err}:
			}
		}
		emit := func(payload []byte) bool {
			if len(bytes.TrimSpace(payload)) == 0 {
				return true
			}
			framed := framing.apply(payload)
			select {
			case <-ctx.Done():
				return false
			case out <- pluginapi.ExecutorStreamChunk{Payload: framed}:
				return true
			}
		}
		process := func() bool {
			lines := bytes.Split(pending, []byte{'\n'})
			pending = append([]byte(nil), lines[len(lines)-1]...)
			for _, line := range lines[:len(lines)-1] {
				for _, chunkMap := range state.parseLine(string(line)) {
					if !emit(encodeJSON(chunkMap)) {
						return false
					}
				}
				if state.upstreamError != nil {
					emitErr(statusErrorFromCC(state.upstreamError))
					return false
				}
			}
			return true
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				emitErr(statusError{statusCode: http.StatusTooManyRequests, msg: e.timeoutMessage(), retryAfter: 5})
				return
			case chunk, ok := <-chunks:
				if !ok {
					for _, chunkMap := range state.parseLine(string(pending)) {
						if !emit(encodeJSON(chunkMap)) {
							return
						}
					}
					if state.upstreamError != nil {
						emitErr(statusErrorFromCC(state.upstreamError))
						return
					}
					if detail := state.incompleteDetail(); detail != "" {
						emitErr(statusErrorFromCC(incompleteUpstreamError(detail)))
						return
					}
					if state.outputTokens == 0 {
						emitErr(statusError{statusCode: http.StatusTooManyRequests, msg: "Empty response from upstream (zero output tokens)", retryAfter: 10})
						return
					}
					e.resetTimeouts()
					return
				}
				if chunk.Err != nil {
					emitErr(statusError{statusCode: http.StatusBadGateway, msg: "Upstream stream error: " + chunk.Err.Error(), retryAfter: 10})
					return
				}
				timer.Reset(e.cfg.streamIdle())
				pending = append(pending, chunk.Payload...)
				if bytes.IndexByte(chunk.Payload, '\n') >= 0 {
					if !process() {
						return
					}
				}
				if len(pending) > 4<<20 {
					if !process() {
						return
					}
				}
			}
		}
	}()
	return out
}

func (e *Executor) CountTokens(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	count := int64(len(req.Payload) / 4)
	if count < 1 && len(req.Payload) > 0 {
		count = 1
	}
	raw, _ := json.Marshal(map[string]any{
		"id":      "commandcode-count",
		"object":  "chat.completion",
		"created": 0,
		"model":   req.Model,
		"choices": []any{},
		"usage": map[string]any{
			"prompt_tokens":     count,
			"completion_tokens": 0,
			"total_tokens":      count,
		},
	})
	return pluginapi.ExecutorResponse{Payload: raw}, nil
}

func (e *Executor) HttpRequest(ctx context.Context, req pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	if strings.TrimSpace(req.URL) == "" {
		return pluginapi.ExecutorHTTPResponse{}, fmt.Errorf("commandcode executor: request URL is required")
	}
	headers := httpHeaderToMap(req.Headers)
	members := e.cfg.members(poolRequestFromHTTP(req))
	if len(members) == 0 && req.Headers.Get("Authorization") == "" {
		return pluginapi.ExecutorHTTPResponse{}, statusError{statusCode: http.StatusUnauthorized, msg: missingKeyMessage}
	}
	if req.Headers.Get("Authorization") == "" && len(members) > 0 {
		headers["Authorization"] = "Bearer " + strings.TrimSpace(members[0].Key)
	}
	var client doer
	var errClient error
	if len(members) > 0 {
		client, errClient = e.pool.clientFor(0, members[0], req.HTTPClient)
	} else if req.HTTPClient != nil {
		client = hostDoer{client: req.HTTPClient}
	} else {
		return pluginapi.ExecutorHTTPResponse{}, fmt.Errorf("commandcode executor: host HTTP client is required")
	}
	if errClient != nil {
		return pluginapi.ExecutorHTTPResponse{}, errClient
	}
	status, respHeaders, respBody, err := client.do(ctx, strings.TrimSpace(req.URL), headers, req.Body)
	if err != nil {
		return pluginapi.ExecutorHTTPResponse{}, err
	}
	return pluginapi.ExecutorHTTPResponse{StatusCode: status, Headers: respHeaders, Body: respBody}, nil
}

func upstreamErrorMessage(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	var decoded struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		if len(decoded.Error) > 0 {
			var obj struct {
				Message string `json:"message"`
			}
			if errObj := json.Unmarshal(decoded.Error, &obj); errObj == nil && strings.TrimSpace(obj.Message) != "" {
				return strings.TrimSpace(obj.Message)
			}
			var text string
			if errString := json.Unmarshal(decoded.Error, &text); errString == nil && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
		if strings.TrimSpace(decoded.Message) != "" {
			return strings.TrimSpace(decoded.Message)
		}
	}
	if len(trimmed) > 500 {
		return trimmed[:500]
	}
	return trimmed
}

func readStreamErrorBody(ctx context.Context, chunks <-chan pluginapi.HTTPStreamChunk) []byte {
	const maxBytes = 1 << 20
	body := make([]byte, 0, 512)
	if chunks == nil {
		return body
	}
	for len(body) < maxBytes {
		select {
		case <-ctx.Done():
			return body
		case chunk, ok := <-chunks:
			if !ok {
				return body
			}
			if len(chunk.Payload) > 0 {
				remaining := maxBytes - len(body)
				if len(chunk.Payload) > remaining {
					return append(body, chunk.Payload[:remaining]...)
				}
				body = append(body, chunk.Payload...)
			}
			if chunk.Err != nil {
				return body
			}
		}
	}
	return body
}

func httpHeaderToMap(headers http.Header) map[string]string {
	out := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) > 0 {
			out[key] = values[0]
		}
	}
	return out
}
