//go:build !windows

package server

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type dataLock struct {
	file *os.File
}

func acquireDataLock(path string) (*dataLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.New("server instance lock is unavailable")
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("server instance is already running")
	}
	return &dataLock{file: file}, nil
}

func (l *dataLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	return l.file.Close()
}
