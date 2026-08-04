//go:build windows

package platform

import (
	"context"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessInspector struct{}

func newWindowsProcessInspector() ProcessInspector { return windowsProcessInspector{} }
func (windowsProcessInspector) Available() bool    { return true }

func (windowsProcessInspector) Inspect(ctx context.Context, query ProcessQuery) (ProcessSummary, error) {
	names, err := normalizeProcessQuery(ctx, query)
	if err != nil {
		return ProcessSummary{State: ProcessUnknown}, err
	}
	handle, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return ProcessSummary{State: ProcessUnknown}, ErrUnavailable
	}
	defer windows.CloseHandle(handle)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(handle, &entry); err != nil {
		return ProcessSummary{State: ProcessUnknown}, ErrUnavailable
	}
	matches := 0
	for {
		if _, ok := names[strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))]; ok {
			matches++
		}
		if err := ctx.Err(); err != nil {
			return ProcessSummary{State: ProcessUnknown}, err
		}
		if err := windows.Process32Next(handle, &entry); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				break
			}
			return ProcessSummary{State: ProcessUnknown}, ErrUnavailable
		}
	}
	return processSummary(matches), nil
}
