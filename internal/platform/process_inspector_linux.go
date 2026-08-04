//go:build linux

package platform

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
)

type linuxProcessInspector struct{}

func newLinuxProcessInspector() ProcessInspector { return linuxProcessInspector{} }
func (linuxProcessInspector) Available() bool    { return true }

func (linuxProcessInspector) Inspect(ctx context.Context, query ProcessQuery) (ProcessSummary, error) {
	names, err := normalizeProcessQuery(ctx, query)
	if err != nil {
		return ProcessSummary{State: ProcessUnknown}, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ProcessSummary{State: ProcessUnknown}, ErrUnavailable
	}
	matches := 0
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		raw, readErr := os.ReadFile("/proc/" + entry.Name() + "/comm")
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) || errors.Is(readErr, os.ErrPermission) {
				continue
			}
			return ProcessSummary{State: ProcessUnknown}, ErrUnavailable
		}
		if _, ok := names[strings.ToLower(strings.TrimSpace(string(raw)))]; ok {
			matches++
		}
		if err := ctx.Err(); err != nil {
			return ProcessSummary{State: ProcessUnknown}, err
		}
	}
	return processSummary(matches), nil
}
