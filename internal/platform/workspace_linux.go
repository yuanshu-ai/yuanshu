//go:build linux

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type linuxWorkspaceInspector struct{}

func newLinuxWorkspaceInspector() WorkspaceInspector { return linuxWorkspaceInspector{} }
func (linuxWorkspaceInspector) Available() bool      { return true }

func (linuxWorkspaceInspector) Inspect(ctx context.Context, path string) (WorkspaceFacts, error) {
	if ctx == nil {
		return WorkspaceFacts{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceFacts{}, err
	}
	if path == "" || !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		return WorkspaceFacts{}, ErrInvalidArgument
	}
	clean := filepath.Clean(path)
	crossesLink, err := linuxPathCrossesLink(ctx, clean)
	if err != nil {
		return WorkspaceFacts{}, err
	}
	canonical, err := filepath.EvalSymlinks(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WorkspaceFacts{}, ErrNotFound
		}
		return WorkspaceFacts{}, errors.New("platform workspace canonical path is unavailable")
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return WorkspaceFacts{}, ErrInvalidArgument
	}
	info, err := os.Stat(canonical)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WorkspaceFacts{}, ErrNotFound
		}
		return WorkspaceFacts{}, errors.New("platform workspace metadata is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return WorkspaceFacts{}, errors.New("platform workspace metadata is unavailable")
	}
	home, _ := os.UserHomeDir()
	return WorkspaceFacts{
		CanonicalPath: canonical, FilesystemRoot: "/", FileIdentity: fmt.Sprintf("%x:%x", uint64(stat.Dev), stat.Ino),
		IsDirectory: info.IsDir(), IsFilesystemRoot: canonical == "/", IsHome: home != "" && sameLinuxPath(canonical, home),
		IsSystem: isLinuxSystemPath(canonical), CrossesLinkBoundary: crossesLink,
	}, nil
}

func linuxPathCrossesLink(ctx context.Context, path string) (bool, error) {
	relative := strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator))
	current := string(filepath.Separator)
	if relative == "" {
		return false, nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, ErrNotFound
			}
			return false, errors.New("platform workspace inspection failed")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
	}
	return false, nil
}

func isLinuxSystemPath(path string) bool {
	for _, root := range []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var"} {
		if linuxPathWithin(path, root) {
			return true
		}
	}
	return false
}

func linuxPathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func sameLinuxPath(left, right string) bool { return filepath.Clean(left) == filepath.Clean(right) }
