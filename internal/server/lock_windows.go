//go:build windows

package server

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type dataLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquireDataLock(path string) (*dataLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.New("server instance lock is unavailable")
	}
	lock := &dataLock{file: file}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &lock.overlapped); err != nil {
		_ = file.Close()
		return nil, errors.New("server instance is already running")
	}
	return lock, nil
}

func (l *dataLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	return l.file.Close()
}
