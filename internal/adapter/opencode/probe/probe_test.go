package probe_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuanshu-ai/yuanshu/internal/adapter/opencode/probe"
)

func TestPinnedFixtureCoversSessionEventAndApprovalShell(t *testing.T) {
	metadataBytes, err := os.ReadFile(filepath.Join("testdata", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Version   string `json:"version"`
		Tag       string `json:"tag"`
		Commit    string `json:"commit"`
		License   string `json:"license"`
		Integrity string `json:"npm_integrity"`
	}
	if json.Unmarshal(metadataBytes, &metadata) != nil || metadata.Version != "1.18.13" || metadata.Tag != "v1.18.13" || len(metadata.Commit) != 40 || metadata.License != "MIT" || !strings.HasPrefix(metadata.Integrity, "sha512-") {
		t.Fatal("OpenCode fixture metadata is invalid")
	}

	file, err := os.Open(filepath.Join("testdata", "managed-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	counts := map[probe.Kind]int{}
	content := map[probe.ContentKind]int{}
	terminal, requiresUser := false, false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), probe.MaxMessageBytes)
	for scanner.Scan() {
		event, err := probe.ParseLine(scanner.Bytes())
		if err != nil {
			t.Fatalf("ParseLine: %v", err)
		}
		counts[event.Kind]++
		content[event.Content]++
		terminal = terminal || event.Terminal
		requiresUser = requiresUser || event.RequiresUser
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []probe.Kind{probe.KindSession, probe.KindMessage, probe.KindApproval, probe.KindQuestion} {
		if counts[kind] == 0 {
			t.Fatalf("fixture missing %s", kind)
		}
	}
	for _, kind := range []probe.ContentKind{probe.ContentText, probe.ContentReasoning, probe.ContentTool} {
		if content[kind] == 0 {
			t.Fatalf("fixture missing %s", kind)
		}
	}
	if !terminal || !requiresUser {
		t.Fatalf("terminal=%t requiresUser=%t", terminal, requiresUser)
	}
}

func TestParserDoesNotLeakRejectedInput(t *testing.T) {
	canary := `C:\Users\canary\secret sk-secret-canary`
	_, err := probe.ParseLine([]byte(`{"payload":{"type":"` + canary + `","properties":{}}}`))
	if !errors.Is(err, probe.ErrInvalidMessage) {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "sk-") {
		t.Fatalf("error leaked input: %v", err)
	}
}

func TestParserRejectsOversizedEvents(t *testing.T) {
	_, err := probe.ParseLine(make([]byte, probe.MaxMessageBytes+1))
	if !errors.Is(err, probe.ErrMessageTooLarge) {
		t.Fatalf("error=%v", err)
	}
}
