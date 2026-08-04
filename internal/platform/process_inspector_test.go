package platform_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

func TestCurrentProcessInspectorFindsOnlyBasename(t *testing.T) {
	inspector := platform.Current().ProcessInspector()
	if inspector == nil || !inspector.Available() {
		t.Skip("process inspection is unavailable on this platform")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	summary, err := inspector.Inspect(context.Background(), platform.ProcessQuery{ExecutableNames: []string{filepath.Base(executable)}})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if summary.State != platform.ProcessRunning || summary.Matches < 1 || summary.Matches > 255 {
		t.Fatalf("summary = %#v", summary)
	}
	if _, err := inspector.Inspect(context.Background(), platform.ProcessQuery{ExecutableNames: []string{executable}}); err == nil {
		t.Fatal("absolute process path was accepted")
	}
}
