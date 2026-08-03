//go:build !darwin

package node

import "github.com/yuanshu-ai/yuanshu/internal/platform"

func prepareDarwinNodeRoot(string) error { return platform.ErrUnavailable }
