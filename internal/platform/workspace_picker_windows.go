//go:build windows

package platform

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	coinitApartmentThreaded = 0x2
	coinitDisableOLE1DDE    = 0x4
	clsctxInprocServer      = 0x1
	fosPickFolders          = 0x20
	fosForceFilesystem      = 0x40
	fosPathMustExist        = 0x800
	fosDontAddToRecent      = 0x02000000
	sigdnFileSysPath        = 0x80058000
	hresultCancelled        = 0x800704c7
)

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")
	clsidFileOpenDialog  = windows.GUID{Data1: 0xdc1c5a9c, Data2: 0xe88a, Data3: 0x4dde, Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7}}
	iidIFileOpenDialog   = windows.GUID{Data1: 0xd57c7288, Data2: 0xd4ad, Data3: 0x4768, Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}
)

type windowsDirectoryPicker struct{}

type fileOpenDialog struct{ vtable *fileOpenDialogVTable }
type fileOpenDialogVTable struct {
	queryInterface, addRef, release, show                       uintptr
	setFileTypes, setFileTypeIndex, getFileTypeIndex            uintptr
	advise, unadvise, setOptions, getOptions                    uintptr
	setDefaultFolder, setFolder, getFolder, getCurrentSelection uintptr
	setFileName, getFileName, setTitle, setOKButtonLabel        uintptr
	setFileNameLabel, getResult                                 uintptr
}
type shellItem struct{ vtable *shellItemVTable }
type shellItemVTable struct {
	queryInterface, addRef, release, bindToHandler, getParent, getDisplayName uintptr
}

func newWindowsDirectoryPicker() DirectoryPicker { return windowsDirectoryPicker{} }

func (windowsDirectoryPicker) Available() bool { return true }

func (windowsDirectoryPicker) PickDirectory(ctx context.Context) (DirectorySelection, error) {
	if ctx == nil || ctx.Err() != nil {
		return DirectorySelection{}, context.Canceled
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	result, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded|coinitDisableOLE1DDE)
	if failedHRESULT(result) {
		return DirectorySelection{}, ErrUnavailable
	}
	defer procCoUninitialize.Call()
	var dialog *fileOpenDialog
	result, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dialog)),
	)
	if failedHRESULT(result) || dialog == nil {
		return DirectorySelection{}, errors.New("Windows directory picker failed")
	}
	defer syscall.SyscallN(dialog.vtable.release, uintptr(unsafe.Pointer(dialog)))
	var options uint32
	result, _, _ = syscall.SyscallN(dialog.vtable.getOptions, uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(&options)))
	if failedHRESULT(result) {
		return DirectorySelection{}, errors.New("Windows directory picker failed")
	}
	options |= fosPickFolders | fosForceFilesystem | fosPathMustExist | fosDontAddToRecent
	result, _, _ = syscall.SyscallN(dialog.vtable.setOptions, uintptr(unsafe.Pointer(dialog)), uintptr(options))
	if failedHRESULT(result) {
		return DirectorySelection{}, errors.New("Windows directory picker failed")
	}
	title, _ := windows.UTF16PtrFromString("Choose a Yuanshu workspace")
	result, _, _ = syscall.SyscallN(dialog.vtable.setTitle, uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(title)))
	if failedHRESULT(result) {
		return DirectorySelection{}, errors.New("Windows directory picker failed")
	}
	result, _, _ = syscall.SyscallN(dialog.vtable.show, uintptr(unsafe.Pointer(dialog)), 0)
	if uint32(result) == hresultCancelled {
		return DirectorySelection{}, context.Canceled
	}
	if failedHRESULT(result) {
		return DirectorySelection{}, errors.New("Windows directory picker failed")
	}
	var item *shellItem
	result, _, _ = syscall.SyscallN(dialog.vtable.getResult, uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(&item)))
	if failedHRESULT(result) || item == nil {
		return DirectorySelection{}, errors.New("Windows directory picker failed")
	}
	defer syscall.SyscallN(item.vtable.release, uintptr(unsafe.Pointer(item)))
	var rawPath *uint16
	result, _, _ = syscall.SyscallN(item.vtable.getDisplayName, uintptr(unsafe.Pointer(item)), sigdnFileSysPath, uintptr(unsafe.Pointer(&rawPath)))
	if failedHRESULT(result) || rawPath == nil {
		return DirectorySelection{}, errors.New("Windows directory picker failed")
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(rawPath)))
	path := windows.UTF16PtrToString(rawPath)
	if err := ctx.Err(); err != nil {
		return DirectorySelection{}, err
	}
	if path == "" || !filepath.IsAbs(path) {
		return DirectorySelection{}, ErrUnavailable
	}
	return DirectorySelection{Path: path, DisplayName: filepath.Base(path)}, nil
}

func failedHRESULT(value uintptr) bool { return int32(uint32(value)) < 0 }
