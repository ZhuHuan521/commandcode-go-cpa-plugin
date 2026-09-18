package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	plug "github.com/router-for-me/commandcode-go-cpa-plugin"
)

func TestRemoveManagedAuthFileOnlyRemovesMarkedFiles(t *testing.T) {
	dir := t.TempDir()

	managed := filepath.Join(dir, plug.ManagedAuthFileNamePrefix+"owned.json")
	if errWrite := os.WriteFile(managed, []byte(`{"managed_by":"`+plug.ManagedAuthMarker+`"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errRemove := removeManagedAuthFile(pluginapi.HostAuthFileEntry{Name: filepath.Base(managed), Path: managed}); errRemove != nil {
		t.Fatalf("remove managed file: %v", errRemove)
	}
	if _, errStat := os.Stat(managed); !os.IsNotExist(errStat) {
		t.Fatalf("managed file still exists: %v", errStat)
	}

	userOwned := filepath.Join(dir, plug.ManagedAuthFileNamePrefix+"user.json")
	if errWrite := os.WriteFile(userOwned, []byte(`{"type":"commandcode","api_key":"user_key"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errRemove := removeManagedAuthFile(pluginapi.HostAuthFileEntry{Name: filepath.Base(userOwned), Path: userOwned}); errRemove != nil {
		t.Fatalf("inspect user-owned file: %v", errRemove)
	}
	if _, errStat := os.Stat(userOwned); errStat != nil {
		t.Fatalf("user-owned file was removed: %v", errStat)
	}
}
