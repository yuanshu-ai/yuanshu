//go:build darwin

package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinLaunchAgentPlistIsAtomicAndRestricted(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	entry := AutostartEntry{ID: "yuanshu-node", Executable: executable, Args: []string{"node", "--background", "--config", "/tmp/config & <safe>"}}
	raw := darwinLaunchAgentPlist(entry)
	arguments, err := parseDarwinLaunchAgentArguments([]byte(raw))
	if err != nil || strings.Join(arguments, "\x00") != strings.Join(append([]string{entry.Executable}, entry.Args...), "\x00") {
		t.Fatalf("parsed arguments = %#v, error = %v", arguments, err)
	}
	for _, required := range []string{"<key>RunAtLoad</key><true/>", "<key>KeepAlive</key><true/>", "<key>ProcessType</key><string>Background</string>"} {
		if !strings.Contains(raw, required) {
			t.Fatalf("plist lacks %q", required)
		}
	}

	directory := t.TempDir()
	path := filepath.Join(directory, "com.yuanshu.node.plist")
	if err := writeDarwinLaunchAgent(path, raw); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("plist mode = %v, %v", info.Mode().Perm(), err)
	}
	if err := writeDarwinLaunchAgent(path, darwinLaunchAgentPlist(AutostartEntry{ID: entry.ID, Executable: executable, Args: []string{"replacement"}})); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range entries {
		if strings.HasPrefix(item.Name(), ".yuanshu-launch-agent-") {
			t.Fatalf("temporary plist was left behind: %s", item.Name())
		}
	}
}

func TestDarwinLaunchAgentValidationDoesNotWrite(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []AutostartEntry{
		{ID: "bad id", Executable: executable},
		{ID: "yuanshu-node", Executable: "relative"},
		{ID: "yuanshu-node", Executable: executable, Env: []string{"SECRET=canary"}},
		{ID: "yuanshu-node", Executable: executable, Directory: "/tmp"},
	} {
		if validDarwinAutostartEntry(entry) {
			t.Fatalf("entry unexpectedly valid: %+v", entry)
		}
	}
}

func TestDarwinLaunchAgentLive(t *testing.T) {
	if os.Getenv("YUANSHU_MACOS_LAUNCHAGENT_LIVE") != "1" {
		t.Skip("set YUANSHU_MACOS_LAUNCHAGENT_LIVE=1")
	}
	executable := os.Getenv("YUANSHU_MACOS_LAUNCHAGENT_EXECUTABLE")
	if executable == "" {
		t.Fatal("YUANSHU_MACOS_LAUNCHAGENT_EXECUTABLE is required for live LaunchAgent test")
	}
	manager := newDarwinAutostartManager()
	entry := AutostartEntry{ID: "yuanshu-node", Executable: executable, Args: []string{"node", "--background"}}
	_ = manager.Remove(context.Background(), entry.ID)
	if err := manager.Install(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Remove(context.Background(), entry.ID) })
	status, err := manager.Status(context.Background(), entry.ID)
	if err != nil || !status.Installed || status.Entry.Executable != executable {
		t.Fatalf("Status() = %+v, %v", status, err)
	}
	if err := manager.Remove(context.Background(), entry.ID); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status(context.Background(), entry.ID)
	if err != nil || status.Installed {
		t.Fatalf("Status() after Remove = %+v, %v", status, err)
	}
}
