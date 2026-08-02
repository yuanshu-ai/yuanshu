package linuxintegration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLinuxPackagingContract(t *testing.T) {
	root := repositoryRoot(t)
	dockerfile := read(t, filepath.Join(root, "Dockerfile"))
	for _, required := range []string{
		"golang:1.26.5@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647",
		"node:24.18.1-bookworm-slim@sha256:235600a8101ab264e117b1768e925532262668dc9b581ef1dd7d96ced463b8e7",
		"gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35",
		"CGO_ENABLED=0", "USER 1000:1000", "HEALTHCHECK", "AS server", "AS standalone",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("Dockerfile is missing required packaging contract %q", required)
		}
	}
	for _, relative := range []string{"deploy/docker-compose/server.yml", "deploy/docker-compose/standalone.yml"} {
		compose := read(t, filepath.Join(root, filepath.FromSlash(relative)))
		for _, required := range []string{"read_only: true", "cap_drop: [ALL]", "no-new-privileges:true", "tmpfs:", "stop_grace_period:"} {
			if !strings.Contains(compose, required) {
				t.Fatalf("%s is missing %q", relative, required)
			}
		}
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		}
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(root, "deploy", "container", "codex", "package-lock.json"))), &lock); err != nil {
		t.Fatal(err)
	}
	if item := lock.Packages["node_modules/@openai/codex"]; item.Version != "0.144.6" {
		t.Fatalf("Codex lock version = %q", item.Version)
	}
}

func TestLinuxPackagingLive(t *testing.T) {
	if os.Getenv("YUANSHU_LINUX_PACKAGING_LIVE") != "1" {
		t.Skip("set YUANSHU_LINUX_PACKAGING_LIVE=1 after building both images")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, image := range []string{"yuanshu-server:dev", "yuanshu-standalone:dev"} {
		output, err := exec.CommandContext(ctx, "docker", "image", "inspect", image, "--format", "{{.Config.User}}").CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != "1000:1000" {
			t.Fatalf("image user contract failed for %s", image)
		}
	}
	output, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=16m", "--entrypoint", "/opt/codex/node_modules/.bin/codex", "yuanshu-standalone:dev", "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(output), "codex-cli 0.144.6") {
		t.Fatal("Standalone Codex version check failed")
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("repository location is unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", ".."))
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
