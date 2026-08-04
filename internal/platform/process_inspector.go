package platform

import (
	"context"
	"path/filepath"
	"strings"
)

const maxProcessMatches = 255

func normalizeProcessQuery(ctx context.Context, query ProcessQuery) (map[string]struct{}, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(query.ExecutableNames) == 0 || len(query.ExecutableNames) > 32 {
		return nil, ErrInvalidArgument
	}
	names := make(map[string]struct{}, len(query.ExecutableNames))
	for _, value := range query.ExecutableNames {
		if value == "" || filepath.Base(value) != value || strings.ContainsAny(value, "\x00/\\\r\n") || len(value) > 255 {
			return nil, ErrInvalidArgument
		}
		names[strings.ToLower(value)] = struct{}{}
	}
	return names, nil
}

func processSummary(matches int) ProcessSummary {
	if matches > maxProcessMatches {
		matches = maxProcessMatches
	}
	if matches > 0 {
		return ProcessSummary{State: ProcessRunning, Matches: matches}
	}
	return ProcessSummary{State: ProcessStopped}
}
