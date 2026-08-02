//go:build windows

package platform

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsWorkspaceInspectorFactsAndStableIdentity(t *testing.T) {
	inspector := newWindowsWorkspaceInspector()
	root := t.TempDir()
	facts, err := inspector.Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !facts.IsDirectory || facts.FileIdentity == "" || facts.CanonicalPath == "" || facts.FilesystemRoot == "" {
		t.Fatalf("incomplete workspace facts: %+v", facts)
	}
	if facts.IsFilesystemRoot || facts.IsHome || facts.IsSystem || facts.CrossesReparseBoundary {
		t.Fatalf("unexpected workspace classification: %+v", facts)
	}
	again, err := inspector.Inspect(context.Background(), root)
	if err != nil || again.FileIdentity != facts.FileIdentity || !equalWindowsPath(again.CanonicalPath, facts.CanonicalPath) {
		t.Fatalf("unstable workspace identity: %+v, %v", again, err)
	}

	filePath := filepath.Join(root, "synthetic.txt")
	if err := os.WriteFile(filePath, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileFacts, err := inspector.Inspect(context.Background(), filePath)
	if err != nil {
		t.Fatal(err)
	}
	if fileFacts.IsDirectory || fileFacts.FileIdentity == "" {
		t.Fatalf("file facts = %+v", fileFacts)
	}
}

func TestWindowsWorkspaceInspectorDetectsReplacement(t *testing.T) {
	inspector := newWindowsWorkspaceInspector()
	parent := t.TempDir()
	path := filepath.Join(parent, "workspace")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := inspector.Inspect(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(parent, "workspace-old")
	if err := os.Rename(path, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := inspector.Inspect(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if before.FileIdentity == after.FileIdentity {
		t.Fatal("replacement directory retained the same file identity")
	}
}

func TestWindowsWorkspaceInspectorDetectsJunctionBoundary(t *testing.T) {
	inspector := newWindowsWorkspaceInspector()
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	link := filepath.Join(parent, "junction")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("cmd.exe", "/d", "/c", "mklink /J "+link+" "+target)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("synthetic junction setup failed: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	facts, err := inspector.Inspect(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	if !facts.CrossesReparseBoundary || !facts.CrossesLinkBoundary {
		t.Fatalf("junction facts = %+v", facts)
	}
}

func TestWindowsWorkspaceInspectorClassifiesProtectedLocations(t *testing.T) {
	inspector := newWindowsWorkspaceInspector()
	home, err := windows.KnownFolderPath(windows.FOLDERID_Profile, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatal(err)
	}
	homeFacts, err := inspector.Inspect(context.Background(), home)
	if err != nil || !homeFacts.IsHome {
		t.Fatalf("home facts = %+v, error = %v", homeFacts, err)
	}
	windowsDir, err := windows.KnownFolderPath(windows.FOLDERID_Windows, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatal(err)
	}
	systemFacts, err := inspector.Inspect(context.Background(), windowsDir)
	if err != nil || !systemFacts.IsSystem {
		t.Fatalf("system facts = %+v, error = %v", systemFacts, err)
	}
	root := filepath.VolumeName(t.TempDir()) + `\`
	rootFacts, err := inspector.Inspect(context.Background(), root)
	if err != nil || !rootFacts.IsFilesystemRoot {
		t.Fatalf("root facts = %+v, error = %v", rootFacts, err)
	}
}

func TestWindowsWorkspaceInspectorFailsClosedAndRedactsPath(t *testing.T) {
	inspector := newWindowsWorkspaceInspector()
	const canary = "workspace-path-sensitive-canary"
	if _, err := inspector.Inspect(context.Background(), canary); !errors.Is(err, ErrInvalidArgument) || strings.Contains(err.Error(), canary) {
		t.Fatalf("relative path error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), canary)
	if _, err := inspector.Inspect(context.Background(), missing); !errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), canary) {
		t.Fatalf("missing path error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inspector.Inspect(ctx, missing); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Inspect error = %v", err)
	}
}
