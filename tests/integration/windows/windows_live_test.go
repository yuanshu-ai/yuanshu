//go:build windows

package windows_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

func TestWindowsPackagingLive(t *testing.T) {
	if os.Getenv("YUANSHU_WINDOWS_PACKAGING_LIVE") != "1" {
		t.Skip("set YUANSHU_WINDOWS_PACKAGING_LIVE=1")
	}
	current := platform.Current()
	if current.Family() != platform.FamilyWindows || !current.IPC().Available() || !current.Autostart().Available() || !current.Processes().Available() || !current.SecureStore().Available() || !current.Workspaces().Available() {
		t.Fatal("Windows user capabilities are incomplete")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := current.IPC().Dial(ctx, platform.IPCName("live-canceled")); err != context.Canceled {
		t.Fatalf("canceled IPC error = %v", err)
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "-v", "-count=1", "./internal/node", "./internal/platform", "-run", "WindowsTrayLive|WindowsAutostartLive")
	command.Dir = root
	command.Env = append(os.Environ(), "YUANSHU_WINDOWS_PACKAGING_LIVE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native packaging checks failed: %v", err)
	}
	if strings.Contains(strings.ToLower(string(output)), "secret=") {
		t.Fatal("native packaging output exposed a credential canary")
	}
}
