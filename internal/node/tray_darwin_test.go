//go:build darwin

package node

import (
	"context"
	"errors"
	"testing"
)

func TestDarwinTrayIsHeadlessAndContextBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- newPlatformTray(true).Run(ctx, trayCallbacks{}) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("headless tray error = %v", err)
	}
}
