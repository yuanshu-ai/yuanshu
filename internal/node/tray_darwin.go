//go:build darwin && cgo

package node

/*
#cgo darwin CFLAGS: -x objective-c
#cgo darwin LDFLAGS: -framework AppKit -framework Foundation
#include <stdlib.h>

void YuanshuTrayRun(void);
void YuanshuTrayStop(void);
void YuanshuTrayUpdate(const char *state, int autostartEnabled);
void YuanshuTrayOpenURL(const char *target);
void YuanshuTrayCopy(const char *value);
void YuanshuTrayShowError(const char *message);
int YuanshuTrayConfirmConfig(const char *identifier, const char *fields);
*/
import "C"

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const (
	darwinTrayOpen = iota + 1
	darwinTrayPair
	darwinTrayReload
	darwinTrayCopyDiagnostics
	darwinTrayAutostart
	darwinTrayReviewConfig
	darwinTrayQuit
)

var darwinTrayState struct {
	sync.RWMutex
	current *darwinTray
}

type darwinTray struct {
	mu        sync.RWMutex
	status    Status
	callbacks trayCallbacks
	running   bool
}

func newPlatformTray(bool) tray { return &darwinTray{} }

func (t *darwinTray) Run(ctx context.Context, callbacks trayCallbacks) error {
	if ctx == nil || ctx.Err() != nil {
		return context.Canceled
	}
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return errors.New("darwin tray is already running")
	}
	t.callbacks = callbacks
	t.running = true
	t.mu.Unlock()
	darwinTrayState.Lock()
	if darwinTrayState.current != nil {
		darwinTrayState.Unlock()
		t.mu.Lock()
		t.running = false
		t.mu.Unlock()
		return errors.New("darwin tray is already active")
	}
	darwinTrayState.current = t
	darwinTrayState.Unlock()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			C.YuanshuTrayStop()
		case <-done:
		}
	}()
	t.pushStatus()
	C.YuanshuTrayRun()
	close(done)

	darwinTrayState.Lock()
	if darwinTrayState.current == t {
		darwinTrayState.current = nil
	}
	darwinTrayState.Unlock()
	t.mu.Lock()
	t.running = false
	t.mu.Unlock()
	return nil
}

func (t *darwinTray) Update(status Status) {
	t.mu.Lock()
	t.status = status
	running := t.running
	t.mu.Unlock()
	if running {
		t.pushStatus()
	}
}

func (t *darwinTray) pushStatus() {
	t.mu.RLock()
	status := t.status
	t.mu.RUnlock()
	state := C.CString(trayStateLabel(status.State))
	defer C.free(unsafe.Pointer(state))
	autostart := C.int(0)
	if status.Autostart == "enabled" {
		autostart = 1
	}
	C.YuanshuTrayUpdate(state, autostart)
}

func (t *darwinTray) OpenURL(target string) error {
	if target == "" {
		return errors.New("control center URL is empty")
	}
	value := C.CString(target)
	defer C.free(unsafe.Pointer(value))
	C.YuanshuTrayOpenURL(value)
	return nil
}

//export yuanshuTrayAction
func yuanshuTrayAction(action C.int) {
	darwinTrayState.RLock()
	tray := darwinTrayState.current
	darwinTrayState.RUnlock()
	if tray == nil {
		return
	}
	go tray.handleAction(int(action))
}

func (t *darwinTray) handleAction(action int) {
	t.mu.RLock()
	callbacks := t.callbacks
	t.mu.RUnlock()
	var err error
	switch action {
	case darwinTrayOpen, darwinTrayPair:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = callbacks.OpenControlCenter(ctx)
		cancel()
	case darwinTrayReload:
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = callbacks.Reload(ctx)
		cancel()
	case darwinTrayCopyDiagnostics:
		var diagnostics []byte
		diagnostics, err = callbacks.Diagnostics()
		if err == nil {
			value := C.CString(string(diagnostics))
			C.YuanshuTrayCopy(value)
			C.free(unsafe.Pointer(value))
		}
	case darwinTrayAutostart:
		enabled := callbacks.Status().Autostart != "enabled"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = callbacks.SetAutostart(ctx, enabled)
		cancel()
	case darwinTrayReviewConfig:
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var changes []ConfigChangeSummary
		changes, err = callbacks.PendingConfig(ctx)
		for _, change := range changes {
			identifier := C.CString(change.ID)
			fields := C.CString(strings.Join(change.Fields, ", "))
			decision := int(C.YuanshuTrayConfirmConfig(identifier, fields))
			C.free(unsafe.Pointer(identifier))
			C.free(unsafe.Pointer(fields))
			if decision == 0 {
				break
			}
			if decision == 1 {
				err = callbacks.DecideConfig(ctx, change.ID, true)
			} else if decision == 2 {
				err = callbacks.DecideConfig(ctx, change.ID, false)
			}
			if err != nil {
				break
			}
		}
	case darwinTrayQuit:
		callbacks.Stop()
		return
	}
	if err != nil {
		message := C.CString("The requested local action could not be completed.")
		C.YuanshuTrayShowError(message)
		C.free(unsafe.Pointer(message))
	}
	t.Update(callbacks.Status())
}
