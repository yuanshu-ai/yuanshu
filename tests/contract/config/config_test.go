package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	configpkg "github.com/yuanshu-ai/yuanshu/internal/config"
)

const schemaID = "https://yuanshu.ai/schemas/config/v1/node-config.schema.json"

func TestSharedConfigurationFixtures(t *testing.T) {
	schema := compileSchema(t)
	var valid []map[string]any
	readFixture(t, "valid-configs.json", &valid)
	for index, document := range valid {
		if err := schema.Validate(document); err != nil {
			t.Fatalf("valid fixture %d failed Schema validation: %v", index, err)
		}
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		var value configpkg.Config
		if err := json.Unmarshal(encoded, &value); err != nil {
			t.Fatal(err)
		}
		if err := configpkg.Validate(value); err != nil {
			t.Fatalf("valid fixture %d failed Go validation: %v", index, err)
		}

		path := filepath.Join(t.TempDir(), "node.toml")
		store, err := configpkg.NewFileStore(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(context.Background(), value); err != nil {
			t.Fatal(err)
		}
		loaded, err := store.Load(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(loaded.Config, value) {
			t.Fatalf("fixture %d changed in TOML round trip", index)
		}
	}

	var invalid []struct {
		Name   string         `json:"name"`
		Config map[string]any `json:"config"`
	}
	readFixture(t, "invalid-configs.json", &invalid)
	for _, testCase := range invalid {
		t.Run(testCase.Name, func(t *testing.T) {
			if err := schema.Validate(testCase.Config); err == nil {
				t.Fatal("invalid fixture passed Schema validation")
			}
		})
	}
}

func TestStrictTOMLAndSanitizedErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want error
	}{
		{"unknown secret field", "config_version=1\nsecret_token=\"SENSITIVE_TOKEN_CANARY\"\n", configpkg.ErrInvalid},
		{"duplicate version", "config_version=1\nconfig_version=1\n", configpkg.ErrInvalid},
		{"future version", "config_version=99\n", configpkg.ErrUnsupportedVersion},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "SENSITIVE_PATH_CANARY.toml")
			if err := os.WriteFile(path, []byte(testCase.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := configpkg.NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Load(context.Background())
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
			for _, canary := range []string{"SENSITIVE_TOKEN_CANARY", "SENSITIVE_PATH_CANARY"} {
				if strings.Contains(err.Error(), canary) {
					t.Fatal("configuration error exposed sensitive input")
				}
			}
		})
	}
}

func TestFileSizeAndParentBoundaries(t *testing.T) {
	t.Run("oversized primary", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "node.toml")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(configpkg.MaxFileBytes + 1); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		store, _ := configpkg.NewFileStore(path)
		if _, err := store.Load(context.Background()); !errors.Is(err, configpkg.ErrTooLarge) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("missing parent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing", "node.toml")
		store, _ := configpkg.NewFileStore(path)
		value := sharedValidConfig(t)
		if err := store.Save(context.Background(), value); !errors.Is(err, configpkg.ErrNotFound) {
			t.Fatalf("error=%v", err)
		}
		if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("Save created a missing parent directory")
		}
	})
}

func TestCanceledOperationsDoNotCreateConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.toml")
	store, _ := configpkg.NewFileStore(path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Save(ctx, sharedValidConfig(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save error=%v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled Save created a configuration")
	}
	if _, err := store.Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load error=%v", err)
	}
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schemaPath := filepath.Join(schemaDirectory(), "node-config.schema.json")
	file, err := os.Open(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource(schemaID, document); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(schemaID)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func sharedValidConfig(t *testing.T) configpkg.Config {
	t.Helper()
	var documents []configpkg.Config
	readFixture(t, "valid-configs.json", &documents)
	return documents[0]
}

func readFixture(t *testing.T, name string, target any) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(schemaDirectory(), "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, target); err != nil {
		t.Fatal(err)
	}
}

func schemaDirectory() string {
	return filepath.Join("..", "..", "..", "schemas", "config", "v1")
}
