//go:build linux

package standalone

import (
	"io"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

func defaultStandalonePlatform(dataDir, masterKeyFile string) (platform.Platform, io.Closer, error) {
	return platform.NewLinuxStandalone(platform.LinuxStandaloneOptions{DataDir: dataDir, MasterKeyFile: masterKeyFile})
}
