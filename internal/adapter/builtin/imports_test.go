package builtin_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestFormalCompositionDoesNotImportCodexDirectly(t *testing.T) {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path is unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	for _, relative := range []string{
		filepath.Join("internal", "node", "host.go"),
		filepath.Join("internal", "node", "doctor.go"),
		filepath.Join("internal", "standalone", "standalone.go"),
	} {
		path := filepath.Join(root, relative)
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", relative, err)
		}
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", relative, err)
			}
			if value == "github.com/yuanshu-ai/yuanshu/internal/adapter/codex" {
				t.Fatalf("%s imports Codex directly", relative)
			}
		}
	}
}
