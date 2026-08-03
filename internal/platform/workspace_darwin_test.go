//go:build darwin

package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinWorkspaceInspectorFactsAndBoundaries(t *testing.T) {
	root := darwinTestWorkspace(t)
	inspector := newDarwinWorkspaceInspector()
	facts, err := inspector.Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !facts.IsDirectory || facts.FileIdentity == "" || facts.CanonicalPath == "" || facts.FilesystemRoot == "" {
		t.Fatalf("incomplete workspace facts: %+v", facts)
	}
	if facts.IsFilesystemRoot || facts.IsHome || facts.IsSystem || facts.CrossesLinkBoundary {
		t.Fatalf("unexpected workspace classification: %+v", facts)
	}
	again, err := inspector.Inspect(context.Background(), root)
	if err != nil || again.FileIdentity != facts.FileIdentity || again.CanonicalPath != facts.CanonicalPath {
		t.Fatalf("unstable workspace identity: %+v, %v", again, err)
	}

	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	linked, err := inspector.Inspect(context.Background(), link)
	if err != nil || !linked.CrossesLinkBoundary || linked.CanonicalPath != target {
		t.Fatalf("symlink facts = %+v, error = %v", linked, err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	homeFacts, err := inspector.Inspect(context.Background(), home)
	if err != nil || !homeFacts.IsHome {
		t.Fatalf("home facts = %+v, error = %v", homeFacts, err)
	}
	rootFacts, err := inspector.Inspect(context.Background(), "/")
	if err != nil || !rootFacts.IsFilesystemRoot {
		t.Fatalf("root facts = %+v, error = %v", rootFacts, err)
	}
	systemFacts, err := inspector.Inspect(context.Background(), "/System")
	if err != nil || !systemFacts.IsSystem {
		t.Fatalf("system facts = %+v, error = %v", systemFacts, err)
	}
}

func TestDarwinWorkspaceInspectorFailsClosedAndRedactsPath(t *testing.T) {
	inspector := newDarwinWorkspaceInspector()
	const canary = "workspace-path-sensitive-canary"
	if _, err := inspector.Inspect(context.Background(), canary); !errors.Is(err, ErrInvalidArgument) || strings.Contains(err.Error(), canary) {
		t.Fatalf("relative path error = %v", err)
	}
	missing := filepath.Join(darwinTestWorkspace(t), canary)
	if _, err := inspector.Inspect(context.Background(), missing); !errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), canary) {
		t.Fatalf("missing path error = %v", err)
	}
}

func darwinTestWorkspace(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path, err := os.MkdirTemp(home, ".yuanshu-workspace-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}
