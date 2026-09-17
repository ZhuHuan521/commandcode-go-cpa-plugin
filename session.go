package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	sessionDuration = 12 * time.Hour
	sessionJitter   = time.Hour
	initRefresh     = 8 * time.Hour
	initJitter      = 2 * time.Hour
)

type sessionEntry struct {
	id        string
	expiresAt time.Time
}

type keyInitState struct {
	fingerprint map[string]any
	nextInitAt  time.Time
}

// sessionManager owns per-key sessions and fingerprint/lifecycle throttles.
type sessionManager struct {
	cfg *pluginConfig

	mu       sync.Mutex
	sessions map[string]sessionEntry
	keyState map[string]keyInitState
	rnd      *big.Int
}

func newSessionManager(cfg *pluginConfig) *sessionManager {
	return &sessionManager{
		cfg:      cfg,
		sessions: make(map[string]sessionEntry),
		keyState: make(map[string]keyInitState),
	}
}

func (m *sessionManager) sessionFor(req pluginapi.ExecutorRequest, apiKey, promptCacheKey string) string {
	candidates := []string{
		req.Headers.Get("x-session-id"),
		req.Headers.Get("x-claude-code-session-id"),
		req.Headers.Get("session_id"),
		promptCacheKey,
	}
	for _, key := range []string{
		coreexecutor.CanonicalSessionIDMetadataKey,
		coreexecutor.ExecutionSessionMetadataKey,
		coreexecutor.DerivedSessionIDMetadataKey,
	} {
		if v, ok := req.Metadata[key].(string); ok {
			candidates = append(candidates, v)
		}
	}
	for _, candidate := range candidates {
		if len(candidate) >= 8 {
			return candidate
		}
	}
	return m.ensureSession(apiKey)
}

func (m *sessionManager) ensureSession(apiKey string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if len(m.sessions) > 1024 {
		for key, entry := range m.sessions {
			if now.After(entry.expiresAt) {
				delete(m.sessions, key)
				delete(m.keyState, key)
			}
		}
	}
	if entry, ok := m.sessions[apiKey]; ok && now.Before(entry.expiresAt) {
		return entry.id
	}
	id, err := randomUUID()
	if err != nil {
		id = "sess-" + randomHex(8)
	}
	jitterN, _ := rand.Int(rand.Reader, big.NewInt(int64(sessionJitter/time.Millisecond)))
	m.sessions[apiKey] = sessionEntry{
		id:        id,
		expiresAt: now.Add(sessionDuration + time.Duration(jitterN.Int64())*time.Millisecond),
	}
	return id
}

func (m *sessionManager) stateForKey(apiKey string) keyInitState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state, ok := m.keyState[apiKey]; ok {
		return state
	}
	state := keyInitState{
		fingerprint: generateFingerprint(apiKey, m.cfg),
		nextInitAt:  time.Time{},
	}
	m.keyState[apiKey] = state
	return state
}

func (m *sessionManager) markInitialized(apiKey string) {
	jitterN, _ := rand.Int(rand.Reader, big.NewInt(int64(initJitter/time.Millisecond)))
	next := time.Now().Add(initRefresh + time.Duration(jitterN.Int64())*time.Millisecond)
	m.mu.Lock()
	state := m.keyState[apiKey]
	state.nextInitAt = next
	m.keyState[apiKey] = state
	m.mu.Unlock()
}

func (m *sessionManager) ensureInitialized(ctx context.Context, client doer, apiKey string) {
	state := m.stateForKey(apiKey)
	if time.Now().Before(state.nextInitAt) {
		return
	}
	headers := m.initHeaders(apiKey)
	fingerprintBody := encodeJSON(state.fingerprint)
	components := asMap(state.fingerprint["components"])
	lifecycleBody := encodeJSON(map[string]any{
		"eventType": "cli_session_exists",
		"metadata": map[string]any{
			"sessionId":  "sess_" + randomHex(8),
			"cliVersion": m.cfg.protocolVersion(),
			"mode":       m.cfg.CLISessionMode,
			"os":         asString(components["platform"]) + "-" + asString(components["arch"]),
		},
	})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, _, _ = client.do(ctx, m.cfg.baseURL()+"/alpha/fingerprint/record", headers, fingerprintBody)
	}()
	go func() {
		defer wg.Done()
		_, _, _, _ = client.do(ctx, m.cfg.baseURL()+"/alpha/lifecycle-events", headers, lifecycleBody)
	}()
	wg.Wait()
	if ctx.Err() == nil {
		m.markInitialized(apiKey)
	}
}

func (m *sessionManager) initHeaders(apiKey string) map[string]string {
	headers := map[string]string{
		"Content-Type":           "application/json",
		"x-cli-environment":      "production",
		"Authorization":          "Bearer " + apiKey,
		"x-command-code-version": m.cfg.protocolVersion(),
	}
	if m.cfg.ZDR {
		headers["x-cmd-zdr"] = "1"
	}
	return headers
}

func randomUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func randomHex(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(buf)
}
