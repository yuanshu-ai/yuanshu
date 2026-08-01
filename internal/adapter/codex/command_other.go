//go:build !windows

package codex

import (
	"errors"
	"strings"
)

func resolveConfiguredCommand(binary string) (string, []string, error) {
	if strings.TrimSpace(binary) == "" {
		return "", nil, errors.New("empty Codex binary")
	}
	return binary, nil, nil
}
