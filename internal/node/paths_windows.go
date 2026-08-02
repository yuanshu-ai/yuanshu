//go:build windows

package node

import (
	"path/filepath"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
	"golang.org/x/sys/windows"
)

type paths struct {
	root, config, database, log string
}

func defaultPaths() (paths, error) {
	root, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return paths{}, platform.ErrUnavailable
	}
	root = filepath.Join(root, "Yuanshu")
	return paths{
		root: root, config: filepath.Join(root, "config.toml"),
		database: filepath.Join(root, "node.db"), log: filepath.Join(root, "node.log"),
	}, nil
}
