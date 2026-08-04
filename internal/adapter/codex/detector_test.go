package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	platformfake "github.com/yuanshu-ai/yuanshu/internal/platform/fake"
)

func TestDetectorReportsInstallationAndSafeProcessState(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manager := newScriptedProcessManager(func(_ platform.ProcessSpec, process *platformfake.Process) {
		_ = process.WriteStdout([]byte("codex-cli " + BaselineVersion + "\n"))
		_ = process.Complete(0)
	})
	inspector := platformfake.NewProcessInspector()
	inspector.Set(filepath.Base(executable), 1)
	detector, err := NewDetector(DetectorOptions{
		Config:    config.CodexAdapterConfig{Enabled: true, Binary: executable, RuntimeMode: "stdio"},
		Processes: manager, Inspector: inspector,
	})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	items, err := detector.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(items) != 1 || items[0].State != adapter.InstallationInstalled || items[0].Process != adapter.ProcessRunning || items[0].Installation.Compatibility != adapter.CompatibilityKnown {
		t.Fatalf("items = %#v", items)
	}
}

func TestDetectorDistinguishesMissingConfiguredCommand(t *testing.T) {
	detector, err := NewDetector(DetectorOptions{
		Config:    config.CodexAdapterConfig{Enabled: true, Binary: filepath.Join(t.TempDir(), "missing-codex"), RuntimeMode: "stdio"},
		Processes: platformfake.NewProcessManager(), Inspector: platformfake.NewProcessInspector(),
	})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	items, err := detector.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(items) != 1 || items[0].State != adapter.InstallationNotInstalled || items[0].Process != adapter.ProcessUnknown {
		t.Fatalf("items = %#v", items)
	}
}
