//go:build !windows

package node

import "github.com/yuanshu-ai/yuanshu/internal/platform"

type paths struct {
	root, config, database, log string
}

func defaultPaths() (paths, error) { return paths{}, platform.ErrUnavailable }
