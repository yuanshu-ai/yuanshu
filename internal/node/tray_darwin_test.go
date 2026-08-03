//go:build darwin && cgo

package node

import (
	"testing"
)

func TestDarwinTrayUsesNativeImplementation(t *testing.T) {
	tray, ok := newPlatformTray(true).(*darwinTray)
	if !ok || tray == nil {
		t.Fatal("darwin tray is not native")
	}
	tray.Update(Status{State: "ready", Autostart: "enabled"})
	if err := tray.OpenURL(""); err == nil {
		t.Fatal("empty control center URL was accepted")
	}
}
