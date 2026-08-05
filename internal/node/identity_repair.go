package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

func identityRepairPaths(defaults paths, configPath string) paths {
	configPath = filepath.Clean(configPath)
	if configPath == defaults.config {
		return defaults
	}
	root := filepath.Dir(configPath)
	return paths{
		root:     root,
		config:   configPath,
		database: filepath.Join(root, "node.db"),
		log:      filepath.Join(root, "node.log"),
	}
}

func prepareNodeIdentityRepair(ctx context.Context, current platform.Platform, locations paths, configPath string) (string, error) {
	if ctx == nil {
		return "", context.Canceled
	}
	if current != nil && current.IPC() != nil {
		if response, err := callLocal(ctx, current.IPC(), "status"); err == nil && response.OK {
			return "", errors.New("stop Node before identity repair")
		}
	}
	if info, err := os.Lstat(configPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("legacy Node configuration was not found")
		}
		return "", errors.New("legacy Node configuration is unavailable")
	} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("legacy Node configuration is unsafe")
	}
	configurationStore, err := config.NewFileStore(configPath)
	if err != nil {
		return "", errors.New("legacy Node configuration is unavailable")
	}
	loaded, err := configurationStore.Load(ctx)
	if err != nil || loaded.Config.Identity.PrivateKeyRef == "" {
		return "", errors.New("identity repair is not required")
	}
	if err := os.MkdirAll(locations.root, 0o700); err != nil {
		return "", errors.New("Node data directory is unavailable")
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	backup := filepath.Join(locations.root, "identity-repair-"+stamp)
	if err := os.Mkdir(backup, 0o700); err != nil {
		return "", errors.New("identity repair backup could not be created")
	}
	files := []string{configPath, configPath + ".bak", locations.database, locations.log, filepath.Join(locations.root, "relay-ca.pem"), filepath.Join(locations.root, "identity.key")}
	type movedFile struct{ source, target string }
	var moved []movedFile
	rollback := func() {
		for index := len(moved) - 1; index >= 0; index-- {
			_ = os.Rename(moved[index].target, moved[index].source)
		}
		_ = os.Remove(backup)
	}
	for _, source := range files {
		info, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			rollback()
			return "", errors.New("identity repair source is unsafe")
		}
		target := filepath.Join(backup, filepath.Base(source))
		if err := os.Rename(source, target); err != nil {
			rollback()
			return "", errors.New("identity repair backup failed")
		}
		moved = append(moved, movedFile{source: source, target: target})
	}
	return backup, nil
}
