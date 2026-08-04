//go:build darwin

package platform

import (
	"context"
	"strings"

	"golang.org/x/sys/unix"
)

type darwinProcessInspector struct{}

func newDarwinProcessInspector() ProcessInspector { return darwinProcessInspector{} }
func (darwinProcessInspector) Available() bool    { return true }

func (darwinProcessInspector) Inspect(ctx context.Context, query ProcessQuery) (ProcessSummary, error) {
	names, err := normalizeProcessQuery(ctx, query)
	if err != nil {
		return ProcessSummary{State: ProcessUnknown}, err
	}
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return ProcessSummary{State: ProcessUnknown}, ErrUnavailable
	}
	matches := 0
	for _, process := range processes {
		end := 0
		for end < len(process.Proc.P_comm) && process.Proc.P_comm[end] != 0 {
			end++
		}
		name := strings.ToLower(string(process.Proc.P_comm[:end]))
		if _, ok := names[name]; ok {
			matches++
		}
		if err := ctx.Err(); err != nil {
			return ProcessSummary{State: ProcessUnknown}, err
		}
	}
	return processSummary(matches), nil
}
