package codex_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex"
)

const schemaVersion = "0.144.6"

func requireCompatibleCodexVersion(t *testing.T, raw []byte) string {
	t.Helper()
	output := strings.TrimSpace(string(raw))
	version, ok := strings.CutPrefix(output, "codex-cli ")
	if !ok || !codex.IsVersionCompatible(version) {
		t.Fatalf("Codex version = %q, no compatible Yuanshu profile", output)
	}
	return version
}

func TestSchemaSnapshotAndFixtures(t *testing.T) {
	t.Parallel()

	directory := schemaDirectory(t)
	metadataBytes, err := os.ReadFile(filepath.Join(directory, "metadata.json"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var metadata struct {
		CodexVersion string `json:"codexVersion"`
		SchemaFiles  []struct {
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
		} `json:"schemaFiles"`
		GenerationCommand string `json:"generationCommand"`
		Experimental      bool   `json:"experimental"`
	}
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata.CodexVersion != schemaVersion {
		t.Fatalf("codexVersion = %q, want %q", metadata.CodexVersion, schemaVersion)
	}
	if metadata.Experimental {
		t.Fatal("schema snapshot must not enable experimental generation")
	}
	if !strings.Contains(metadata.GenerationCommand, "generate-json-schema") || strings.Contains(metadata.GenerationCommand, "--experimental") {
		t.Fatalf("unexpected generation command %q", metadata.GenerationCommand)
	}

	if len(metadata.SchemaFiles) != 2 {
		t.Fatalf("schemaFiles length = %d, want 2", len(metadata.SchemaFiles))
	}
	var combinedSchemas strings.Builder
	for _, schemaFile := range metadata.SchemaFiles {
		schemaBytes, err := os.ReadFile(filepath.Join(directory, schemaFile.File))
		if err != nil {
			t.Fatalf("read schema %s: %v", schemaFile.File, err)
		}
		digest := sha256.Sum256(schemaBytes)
		if got := hex.EncodeToString(digest[:]); got != schemaFile.SHA256 {
			t.Fatalf("schema %s SHA-256 = %s, want %s", schemaFile.File, got, schemaFile.SHA256)
		}
		var schema any
		if err := json.Unmarshal(schemaBytes, &schema); err != nil {
			t.Fatalf("schema %s is not valid JSON: %v", schemaFile.File, err)
		}
		combinedSchemas.Write(schemaBytes)
	}
	schemaText := combinedSchemas.String()
	for _, method := range []string{
		"initialize", "thread/start", "thread/list", "thread/read", "thread/resume",
		"turn/start", "turn/steer", "turn/interrupt",
		"item/commandExecution/requestApproval", "item/fileChange/requestApproval",
	} {
		if !strings.Contains(schemaText, `"`+method+`"`) {
			t.Errorf("schema does not contain stable method %q", method)
		}
	}

	requiredFixtureMethods := map[string]bool{
		"thread/started":                        false,
		"turn/started":                          false,
		"turn/completed":                        false,
		"item/started":                          false,
		"item/completed":                        false,
		"item/agentMessage/delta":               false,
		"turn/diff/updated":                     false,
		"item/commandExecution/requestApproval": false,
		"item/fileChange/requestApproval":       false,
	}
	for _, name := range []string{"stable-flow.jsonl", "approvals.jsonl"} {
		validateFixture(t, filepath.Join(directory, "fixtures", name), requiredFixtureMethods)
	}
	for method, found := range requiredFixtureMethods {
		if !found {
			t.Errorf("fixtures do not contain %q", method)
		}
	}
}

func validateFixture(t *testing.T, path string, methods map[string]bool) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture %s: %v", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		content := scanner.Text()
		var message struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(content), &message); err != nil {
			t.Fatalf("fixture %s line %d is invalid JSON: %v", path, line, err)
		}
		if _, ok := methods[message.Method]; ok {
			methods[message.Method] = true
		}
		lower := strings.ToLower(content)
		for _, forbidden := range []string{"sk-", "bearer ", "@example", `c:\\users\\`, "/home/", "/users/", "/tmp/"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("fixture %s line %d contains forbidden pattern %q", path, line, forbidden)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fixture %s: %v", path, err)
	}
	if line == 0 {
		t.Fatalf("fixture %s is empty", path)
	}
}

func schemaDirectory(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
	return filepath.Join(repositoryRoot, "schemas", "codex", schemaVersion)
}
