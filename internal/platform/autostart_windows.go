//go:build windows

package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const windowsRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

type windowsAutostartManager struct{}

func newWindowsAutostartManager() AutostartManager { return windowsAutostartManager{} }

func (windowsAutostartManager) Available() bool { return true }

func (windowsAutostartManager) Install(ctx context.Context, entry AutostartEntry) error {
	if err := platformContext(ctx); err != nil {
		return err
	}
	if !validAutostartEntry(entry) {
		return ErrInvalidArgument
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, windowsRunKey, registry.SET_VALUE)
	if err != nil {
		return ErrUnavailable
	}
	defer key.Close()
	command := windows.ComposeCommandLine(append([]string{entry.Executable}, entry.Args...))
	if err := key.SetStringValue(windowsAutostartValue(entry.ID), command); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (windowsAutostartManager) Remove(ctx context.Context, id string) error {
	if err := platformContext(ctx); err != nil {
		return err
	}
	if !validAutostartID(id) {
		return ErrInvalidArgument
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, windowsRunKey, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return ErrNotFound
		}
		return ErrUnavailable
	}
	defer key.Close()
	if err := key.DeleteValue(windowsAutostartValue(id)); err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return ErrNotFound
		}
		return ErrUnavailable
	}
	return nil
}

func (windowsAutostartManager) Status(ctx context.Context, id string) (AutostartStatus, error) {
	if err := platformContext(ctx); err != nil {
		return AutostartStatus{}, err
	}
	if !validAutostartID(id) {
		return AutostartStatus{}, ErrInvalidArgument
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, windowsRunKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return AutostartStatus{Installed: false}, nil
		}
		return AutostartStatus{}, ErrUnavailable
	}
	defer key.Close()
	command, _, err := key.GetStringValue(windowsAutostartValue(id))
	if errors.Is(err, registry.ErrNotExist) {
		return AutostartStatus{Installed: false}, nil
	}
	if err != nil {
		return AutostartStatus{}, ErrUnavailable
	}
	arguments, err := windows.DecomposeCommandLine(command)
	if err != nil || len(arguments) == 0 {
		return AutostartStatus{}, ErrUnavailable
	}
	return AutostartStatus{Installed: true, Entry: AutostartEntry{
		ID: id, Executable: arguments[0], Args: append([]string(nil), arguments[1:]...),
	}}, nil
}

func validAutostartEntry(entry AutostartEntry) bool {
	if !validAutostartID(entry.ID) || entry.Env != nil || entry.Directory != "" || !filepath.IsAbs(entry.Executable) {
		return false
	}
	info, err := os.Stat(entry.Executable)
	if err != nil || !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Executable), ".exe") {
		return false
	}
	if len(entry.Args) > 32 {
		return false
	}
	for _, argument := range entry.Args {
		if len(argument) > 4096 || strings.IndexFunc(argument, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

func validAutostartID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, character := range id {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func windowsAutostartValue(id string) string { return "Yuanshu " + id }
