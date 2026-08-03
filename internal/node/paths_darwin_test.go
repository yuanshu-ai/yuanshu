//go:build darwin

package node

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

func TestDarwinDefaultPathsUsePrivateApplicationSupport(t *testing.T) {
	locations, err := defaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(locations.root) || !strings.HasSuffix(filepath.ToSlash(locations.root), "/Library/Application Support/Yuanshu") {
		t.Fatalf("root = %q", locations.root)
	}
	for _, path := range []string{locations.config, locations.database, locations.log} {
		if filepath.Dir(path) != locations.root || !filepath.IsAbs(path) {
			t.Fatalf("path %q is outside root %q", path, locations.root)
		}
	}
}

func TestDarwinNodeRootIsUserOwnedPrivateAndRejectsSymlinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	if err := prepareDarwinNodeRoot(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("data root mode = %v", info.Mode().Perm())
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareDarwinNodeRoot(link); !errors.Is(err, platform.ErrUnavailable) {
		t.Fatalf("symlink data root error = %v", err)
	}
}
