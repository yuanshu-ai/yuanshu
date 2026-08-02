//go:build windows

package platform

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestWindowsAutostartValidatesWithoutWritingRegistry(t *testing.T) {
	manager := newWindowsAutostartManager()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []AutostartEntry{
		{ID: "bad id", Executable: executable},
		{ID: "valid", Executable: "relative.exe"},
		{ID: "valid", Executable: executable, Env: []string{"SECRET=canary"}},
		{ID: "valid", Executable: executable, Directory: `C:\canary`},
	} {
		if err := manager.Install(context.Background(), entry); !errors.Is(err, ErrInvalidArgument) || strings.Contains(err.Error(), "canary") {
			t.Fatalf("Install error = %v", err)
		}
	}
}

func TestWindowsAutostartLive(t *testing.T) {
	if os.Getenv("YUANSHU_WINDOWS_PACKAGING_LIVE") != "1" {
		t.Skip("set YUANSHU_WINDOWS_PACKAGING_LIVE=1")
	}
	manager := newWindowsAutostartManager()
	id := "test-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	_ = manager.Remove(context.Background(), id)
	t.Cleanup(func() {
		if err := manager.Remove(context.Background(), id); err != nil && !errors.Is(err, ErrNotFound) {
			t.Errorf("autostart cleanup: %v", err)
		}
	})
	executable, _ := os.Executable()
	want := AutostartEntry{ID: id, Executable: executable, Args: []string{"node", "--background", "--config", `C:\synthetic path\config.toml`}}
	if err := manager.Install(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background(), id)
	if err != nil || !status.Installed || status.Entry.Executable != want.Executable || strings.Join(status.Entry.Args, "\x00") != strings.Join(want.Args, "\x00") {
		t.Fatalf("Status = %+v, error = %v", status, err)
	}
	if err := manager.Remove(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}
