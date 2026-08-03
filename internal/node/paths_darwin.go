//go:build darwin

package node

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

type paths struct {
	root, config, database, log string
}

func defaultPaths() (paths, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return paths{}, platform.ErrUnavailable
	}
	root := filepath.Join(home, "Library", "Application Support", "Yuanshu")
	return paths{root: root, config: filepath.Join(root, "config.toml"), database: filepath.Join(root, "node.db"), log: filepath.Join(root, "node.log")}, nil
}

func prepareDarwinNodeRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) {
		return platform.ErrInvalidArgument
	}
	if info, err := os.Lstat(root); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return platform.ErrUnavailable
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return platform.ErrUnavailable
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return platform.ErrUnavailable
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) || err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return platform.ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return platform.ErrUnavailable
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return platform.ErrUnavailable
	}
	return nil
}
