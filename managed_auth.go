package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	// ManagedAuthFileNamePrefix identifies auth files owned by this plugin.
	ManagedAuthFileNamePrefix = "commandcode-managed-"
	// ManagedAuthMarker is persisted in every managed auth file so stale-file
	// cleanup can distinguish plugin-owned files from user-managed credentials.
	ManagedAuthMarker = "commandcode-go-cpa-plugin"
)

// ManagedAuthFile is one host-visible auth file synthesized from plugin config.
type ManagedAuthFile struct {
	Name string
	JSON []byte
}

type managedAuthDocument struct {
	Type     string `json:"type"`
	AuthKind string `json:"auth_kind"`
	APIKey   string `json:"api_key"`
	Priority int    `json:"priority,omitempty"`
	Weight   int    `json:"weight,omitempty"`
	ProxyURL string `json:"proxy_url,omitempty"`
	// DisableCooling is a tri-state override for the host scheduler: omitted
	// means "inherit", true keeps the credential out of cooldown bookkeeping,
	// false forces cooldowns back on even when config.yaml disables them.
	DisableCooling *bool  `json:"disable_cooling,omitempty"`
	ManagedBy      string `json:"managed_by"`
	Note           string `json:"note,omitempty"`
}

// ManagedAuthFiles materializes configured API keys as host auth records. This
// is required because CLIProxyAPI's mixed scheduler only selects providers that
// have an auth candidate, even when the provider executor itself needs no OAuth
// login.
func (p *CommandCodePlugin) ManagedAuthFiles() []ManagedAuthFile {
	if p == nil || p.cfg == nil || !p.cfg.sharedScheduling() {
		return nil
	}
	members := p.cfg.members(pluginapi.ExecutorRequest{})
	if len(members) == 0 {
		return nil
	}

	baseURL := p.cfg.baseURL()
	files := make([]ManagedAuthFile, 0, len(members))
	seen := make(map[string]struct{}, len(members))
	for _, member := range members {
		key := strings.TrimSpace(member.Key)
		if key == "" {
			continue
		}
		signature := baseURL + "\x00" + key + "\x00" + strings.TrimSpace(member.ProxyURL)
		sum := sha256.Sum256([]byte(signature))
		name := ManagedAuthFileNamePrefix + hex.EncodeToString(sum[:12]) + ".json"
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}

		priority := member.Priority
		if priority == 0 {
			priority = p.cfg.Priority
		}
		doc := managedAuthDocument{
			Type:           Provider,
			AuthKind:       "apikey",
			APIKey:         key,
			Priority:       priority,
			Weight:         member.normalizedWeight(),
			ProxyURL:       strings.TrimSpace(member.ProxyURL),
			DisableCooling: p.cfg.disableCoolingFor(member),
			ManagedBy:      ManagedAuthMarker,
			Note:           "Managed from plugins.configs.commandcode",
		}
		raw, errMarshal := json.Marshal(doc)
		if errMarshal != nil {
			continue
		}
		files = append(files, ManagedAuthFile{Name: name, JSON: raw})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files
}
