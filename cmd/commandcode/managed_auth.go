package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	plug "github.com/router-for-me/commandcode-go-cpa-plugin"
)

type abiHostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type abiHostLogRequest struct {
	Level   string         `json:"level,omitempty"`
	Message string         `json:"message,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

func syncManagedSchedulingAuths(p *plug.CommandCodePlugin) error {
	if p == nil {
		return nil
	}
	files := p.ManagedAuthFiles()
	desired := make(map[string]struct{}, len(files))
	for _, file := range files {
		desired[file.Name] = struct{}{}
	}

	var syncErrors []error
	listResp, errList := callHost[abiHostAuthListResponse](pluginabi.MethodHostAuthList, map[string]any{})
	if errList != nil {
		syncErrors = append(syncErrors, fmt.Errorf("list host auth files: %w", errList))
	}

	for _, file := range files {
		_, errSave := callHost[pluginapi.HostAuthSaveResponse](pluginabi.MethodHostAuthSave, pluginapi.HostAuthSaveRequest{
			Name: file.Name,
			JSON: file.JSON,
		})
		if errSave != nil {
			syncErrors = append(syncErrors, fmt.Errorf("save managed auth %s: %w", file.Name, errSave))
		}
	}

	if errList == nil {
		for _, entry := range listResp.Files {
			if !strings.HasPrefix(strings.TrimSpace(entry.Name), plug.ManagedAuthFileNamePrefix) {
				continue
			}
			if _, keep := desired[entry.Name]; keep {
				continue
			}
			if errRemove := removeManagedAuthFile(entry); errRemove != nil {
				syncErrors = append(syncErrors, errRemove)
			}
		}
	}
	return errors.Join(syncErrors...)
}

func removeManagedAuthFile(entry pluginapi.HostAuthFileEntry) error {
	path := strings.TrimSpace(entry.Path)
	if path == "" {
		return nil
	}
	raw, errRead := os.ReadFile(path)
	if os.IsNotExist(errRead) {
		return nil
	}
	if errRead != nil {
		return fmt.Errorf("read stale managed auth %s: %w", entry.Name, errRead)
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(raw, &metadata); errUnmarshal != nil {
		return nil
	}
	if strings.TrimSpace(asStringValue(metadata["managed_by"])) != plug.ManagedAuthMarker {
		return nil
	}
	if errRemove := os.Remove(path); errRemove != nil && !os.IsNotExist(errRemove) {
		return fmt.Errorf("remove stale managed auth %s: %w", entry.Name, errRemove)
	}
	return nil
}

func logManagedSchedulingAuthError(err error) {
	if err == nil {
		return
	}
	_, _ = callHost[abiEmptyResponse](pluginabi.MethodHostLog, abiHostLogRequest{
		Level:   "warn",
		Message: "commandcode: failed to synchronize configured keys with the host scheduler",
		Fields:  map[string]any{"error": err.Error()},
	})
}

func asStringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
