//go:build windows

package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func resolveConfiguredCommand(binary string) (string, []string, error) {
	if strings.TrimSpace(binary) == "" {
		return "", nil, errors.New("empty Codex binary")
	}
	extension := strings.ToLower(filepath.Ext(binary))
	if binary != "codex" && extension != ".cmd" {
		return binary, nil, nil
	}
	var shim string
	if extension == ".cmd" {
		if !strings.EqualFold(filepath.Base(binary), "codex.cmd") {
			return "", nil, errors.New("unsupported command shim")
		}
		shim = binary
	} else {
		for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
			candidate := filepath.Join(directory, "codex.cmd")
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				shim = candidate
				break
			}
		}
	}
	if shim == "" {
		return binary, nil, nil
	}
	root := filepath.Dir(shim)
	script := filepath.Join(root, "node_modules", "@openai", "codex", "bin", "codex.js")
	if info, err := os.Stat(script); err != nil || !info.Mode().IsRegular() {
		return "", nil, errors.New("unsupported Codex npm layout")
	}
	node := filepath.Join(root, "node.exe")
	if info, err := os.Stat(node); err != nil || !info.Mode().IsRegular() {
		node = "node"
	}
	return node, []string{script}, nil
}
