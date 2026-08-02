//go:build windows

package node

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestWindowsTrayStateLabels(t *testing.T) {
	tests := map[string]string{
		"ready": "Ready", "unpaired": "Unpaired", "recovering": "Recovering", "starting": "Recovering", "failed": "Needs attention",
	}
	for state, want := range tests {
		if got := trayStateLabel(state); got != want {
			t.Fatalf("trayStateLabel(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestWindowsTrayLive(t *testing.T) {
	if os.Getenv("YUANSHU_WINDOWS_PACKAGING_LIVE") != "1" {
		t.Skip("set YUANSHU_WINDOWS_PACKAGING_LIVE=1")
	}
	ctx, cancel := context.WithCancel(context.Background())
	tray := newPlatformTray(false)
	status := Status{Version: 1, State: "unpaired", Platform: "windows", Autostart: "disabled"}
	done := make(chan error, 1)
	go func() {
		done <- tray.Run(ctx, trayCallbacks{
			Status:       func() Status { return status },
			Reload:       func(context.Context) error { return nil },
			Diagnostics:  func() ([]byte, error) { return marshalStatus(status, true) },
			OpenConfig:   func() error { return nil },
			SetAutostart: func(context.Context, bool) error { return nil },
			Stop:         cancel,
		})
	}()
	tray.Update(status)
	time.Sleep(750 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tray did not remove its icon")
	}
}
