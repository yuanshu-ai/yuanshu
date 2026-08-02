//go:build !linux

package standalone

import (
	"io"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

func defaultStandalonePlatform(string, string) (platform.Platform, io.Closer, error) {
	return platform.Current(), nil, nil
}
