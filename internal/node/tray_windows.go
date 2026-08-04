//go:build windows

package node

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmClose        = 0x0010
	wmDestroy      = 0x0002
	wmRButtonUp    = 0x0205
	wmLButtonUp    = 0x0202
	wmTray         = 0x8001
	wmTrayUpdate   = 0x8002
	nimAdd         = 0x00000000
	nimModify      = 0x00000001
	nimDelete      = 0x00000002
	nimSetVersion  = 0x00000004
	nifMessage     = 0x00000001
	nifIcon        = 0x00000002
	nifTip         = 0x00000004
	nifInfo        = 0x00000010
	niifWarning    = 0x00000002
	mfString       = 0x00000000
	mfGray         = 0x00000001
	mfChecked      = 0x00000008
	mfSeparator    = 0x00000800
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	swHide         = 0
	swShowNormal   = 1
	cfUnicodeText  = 13
	gmemMoveable   = 0x0002
	trayIconID     = 1
	menuOpen       = 101
	menuReload     = 102
	menuCopy       = 103
	menuAutostart  = 104
	menuReview     = 105
	menuExit       = 106
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	shell32                 = windows.NewLazySystemDLL("shell32.dll")
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procUnregisterClassW    = user32.NewProc("UnregisterClassW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procRegisterWindowMsgW  = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procShowWindow          = user32.NewProc("ShowWindow")
	procCreateIcon          = user32.NewProc("CreateIcon")
	procDestroyIcon         = user32.NewProc("DestroyIcon")
	procOpenClipboard       = user32.NewProc("OpenClipboard")
	procCloseClipboard      = user32.NewProc("CloseClipboard")
	procEmptyClipboard      = user32.NewProc("EmptyClipboard")
	procSetClipboardData    = user32.NewProc("SetClipboardData")
	procShellNotifyIconW    = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW       = shell32.NewProc("ShellExecuteW")
	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	procGetConsoleWindow    = kernel32.NewProc("GetConsoleWindow")
	procGlobalAlloc         = kernel32.NewProc("GlobalAlloc")
	procGlobalLock          = kernel32.NewProc("GlobalLock")
	procGlobalUnlock        = kernel32.NewProc("GlobalUnlock")
	procGlobalFree          = kernel32.NewProc("GlobalFree")
	procRtlMoveMemory       = kernel32.NewProc("RtlMoveMemory")
)

type trayPoint struct{ X, Y int32 }
type trayMessage struct {
	Window, Message, WParam, LParam uintptr
	Time                            uint32
	Point                           trayPoint
	Private                         uint32
}
type windowClassEx struct {
	Size, Style  uint32
	WindowProc   uintptr
	ClassExtra   int32
	WindowExtra  int32
	Instance     uintptr
	Icon, Cursor uintptr
	Background   uintptr
	MenuName     *uint16
	ClassName    *uint16
	SmallIcon    uintptr
}
type notifyIconData struct {
	Size                       uint32
	Window                     uintptr
	ID, Flags, CallbackMessage uint32
	Icon                       uintptr
	Tip                        [128]uint16
	State, StateMask           uint32
	Info                       [256]uint16
	TimeoutOrVersion           uint32
	InfoTitle                  [64]uint16
	InfoFlags                  uint32
	GUID                       windows.GUID
	BalloonIcon                uintptr
}

type windowsTray struct {
	background bool
	mu         sync.RWMutex
	window     uintptr
	icon       uintptr
	status     Status
	callbacks  trayCallbacks
	taskbarMsg uint32
}

func newPlatformTray(background bool) tray { return &windowsTray{background: background} }

func (t *windowsTray) Update(status Status) {
	t.mu.Lock()
	t.status = status
	window := t.window
	t.mu.Unlock()
	if window != 0 {
		procPostMessageW.Call(window, wmTrayUpdate, 0, 0)
	}
}

func (t *windowsTray) Run(ctx context.Context, callbacks trayCallbacks) error {
	if ctx == nil {
		return context.Canceled
	}
	if t.background {
		if console, _, _ := procGetConsoleWindow.Call(); console != 0 {
			procShowWindow.Call(console, swHide)
		}
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	t.callbacks = callbacks
	instance, _, _ := procGetModuleHandleW.Call(0)
	className, _ := windows.UTF16PtrFromString("YuanshuNodeTrayWindowV1")
	callback := windows.NewCallback(t.windowProc)
	class := windowClassEx{Size: uint32(unsafe.Sizeof(windowClassEx{})), WindowProc: callback, Instance: instance, ClassName: className}
	if atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class))); atom == 0 && !errors.Is(callErr, windows.ERROR_CLASS_ALREADY_EXISTS) {
		return platformFailure()
	}
	defer procUnregisterClassW.Call(uintptr(unsafe.Pointer(className)), instance)
	window, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if window == 0 {
		return platformFailure()
	}
	t.mu.Lock()
	t.window = window
	t.icon = createYuanshuIcon(instance)
	t.mu.Unlock()
	if t.icon == 0 || !t.notify(nimAdd, false) {
		procDestroyWindow.Call(window)
		return platformFailure()
	}
	t.notify(nimSetVersion, false)
	taskbarName, _ := windows.UTF16PtrFromString("TaskbarCreated")
	registered, _, _ := procRegisterWindowMsgW.Call(uintptr(unsafe.Pointer(taskbarName)))
	t.taskbarMsg = uint32(registered)
	go func() {
		<-ctx.Done()
		procPostMessageW.Call(window, wmClose, 0, 0)
	}()
	var message trayMessage
	for {
		result, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
	t.mu.Lock()
	icon := t.icon
	t.icon, t.window = 0, 0
	t.mu.Unlock()
	if icon != 0 {
		procDestroyIcon.Call(icon)
	}
	return nil
}

func (t *windowsTray) windowProc(window uintptr, message uint32, wparam, lparam uintptr) uintptr {
	if message == t.taskbarMsg && message != 0 {
		t.notify(nimAdd, false)
		return 0
	}
	switch message {
	case wmTray:
		if uint32(lparam) == wmRButtonUp || uint32(lparam) == wmLButtonUp {
			t.showMenu(window)
		}
		return 0
	case wmTrayUpdate:
		t.notify(nimModify, false)
		return 0
	case wmClose:
		procDestroyWindow.Call(window)
		return 0
	case wmDestroy:
		t.notify(nimDelete, false)
		procPostQuitMessage.Call(0)
		return 0
	default:
		result, _, _ := procDefWindowProcW.Call(window, uintptr(message), wparam, lparam)
		return result
	}
}

func (t *windowsTray) notify(operation uint32, failure bool) bool {
	t.mu.RLock()
	window, icon, status := t.window, t.icon, t.status
	t.mu.RUnlock()
	data := notifyIconData{Size: uint32(unsafe.Sizeof(notifyIconData{})), Window: window, ID: trayIconID, Flags: nifMessage | nifIcon | nifTip, CallbackMessage: wmTray, Icon: icon}
	copy(data.Tip[:], windows.StringToUTF16("Yuanshu Node · "+trayStateLabel(status.State)))
	if operation == nimSetVersion {
		data.TimeoutOrVersion = 4
	}
	if failure {
		data.Flags |= nifInfo
		data.InfoFlags = niifWarning
		copy(data.InfoTitle[:], windows.StringToUTF16("Yuanshu Node"))
		copy(data.Info[:], windows.StringToUTF16("The requested action could not be completed."))
	}
	result, _, _ := procShellNotifyIconW.Call(uintptr(operation), uintptr(unsafe.Pointer(&data)))
	return result != 0
}

func (t *windowsTray) showMenu(window uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	status := t.callbacks.Status()
	appendTrayMenu(menu, mfString|mfGray, 0, "Yuanshu Node · "+trayStateLabel(status.State))
	appendTrayMenu(menu, mfSeparator, 0, "")
	appendTrayMenu(menu, mfString, menuOpen, "Open Node Control Center")
	appendTrayMenu(menu, mfString, menuReload, "Reload and reconnect")
	appendTrayMenu(menu, mfString, menuCopy, "Copy diagnostics")
	autostartFlags := uint32(mfString)
	if status.Autostart == "enabled" {
		autostartFlags |= mfChecked
	}
	appendTrayMenu(menu, autostartFlags, menuAutostart, "Start at login")
	appendTrayMenu(menu, mfString, menuReview, "Review pending changes...")
	appendTrayMenu(menu, mfSeparator, 0, "")
	appendTrayMenu(menu, mfString, menuExit, "Exit Yuanshu Node")
	var point trayPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&point)))
	procSetForegroundWindow.Call(window)
	selected, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmReturnCmd, uintptr(point.X), uintptr(point.Y), 0, window, 0)
	t.handleMenu(uint32(selected))
}

func (t *windowsTray) handleMenu(command uint32) {
	switch command {
	case menuOpen:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if t.callbacks.OpenControlCenter(ctx) != nil {
			t.notify(nimModify, true)
		}
	case menuReload:
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if t.callbacks.Reload(ctx) != nil {
				t.notify(nimModify, true)
			}
		}()
	case menuCopy:
		value, err := t.callbacks.Diagnostics()
		if err != nil || copyWindowsClipboard(string(value)) != nil {
			t.notify(nimModify, true)
		}
	case menuAutostart:
		enabled := t.callbacks.Status().Autostart != "enabled"
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if t.callbacks.SetAutostart(ctx, enabled) != nil {
				t.notify(nimModify, true)
			}
			t.Update(t.callbacks.Status())
		}()
	case menuReview:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if t.callbacks.OpenControlCenter(ctx) != nil {
			t.notify(nimModify, true)
		}
	case menuExit:
		t.callbacks.Stop()
	}
}

func appendTrayMenu(menu uintptr, flags uint32, id uint32, label string) {
	value, _ := windows.UTF16PtrFromString(label)
	procAppendMenuW.Call(menu, uintptr(flags), uintptr(id), uintptr(unsafe.Pointer(value)))
}

func createYuanshuIcon(instance uintptr) uintptr {
	andMask := make([]byte, 128)
	xorMask := make([]byte, 128)
	for index := range andMask {
		andMask[index] = 0xff
	}
	set := func(x, y int) {
		index, bit := y*4+x/8, byte(0x80>>uint(x%8))
		xorMask[index] |= bit
	}
	for y := 4; y <= 14; y++ {
		offset := y - 4
		for width := 0; width < 2; width++ {
			set(5+offset+width, y)
			set(26-offset-width, y)
		}
	}
	for y := 14; y <= 27; y++ {
		set(15, y)
		set(16, y)
	}
	icon, _, _ := procCreateIcon.Call(instance, 32, 32, 1, 1, uintptr(unsafe.Pointer(&andMask[0])), uintptr(unsafe.Pointer(&xorMask[0])))
	return icon
}

func (t *windowsTray) OpenURL(target string) error {
	operation, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(target)
	result, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(operation)), uintptr(unsafe.Pointer(file)), 0, 0, swShowNormal)
	if result <= 32 {
		return platformFailure()
	}
	return nil
}

func copyWindowsClipboard(value string) error {
	encoded, err := windows.UTF16FromString(value)
	if err != nil || len(encoded) > localMaxBytes {
		return platformFailure()
	}
	if opened, _, _ := procOpenClipboard.Call(0); opened == 0 {
		return platformFailure()
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	handle, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(encoded)*2))
	if handle == 0 {
		return platformFailure()
	}
	pointer, _, _ := procGlobalLock.Call(handle)
	if pointer == 0 {
		procGlobalFree.Call(handle)
		return platformFailure()
	}
	procRtlMoveMemory.Call(pointer, uintptr(unsafe.Pointer(&encoded[0])), uintptr(len(encoded)*2))
	procGlobalUnlock.Call(handle)
	if result, _, _ := procSetClipboardData.Call(cfUnicodeText, handle); result == 0 {
		procGlobalFree.Call(handle)
		return platformFailure()
	}
	return nil
}

func platformFailure() error { return errors.New("windows user interface is unavailable") }
