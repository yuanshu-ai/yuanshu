package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	secretPattern = regexp.MustCompile(`(?i)(?:bearer\s+|sk-|sess-)[a-z0-9_\-]{4,}`)
	emailPattern  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	windowsPath   = regexp.MustCompile(`(?i)[A-Z]:\\[^\s"']+`)
	unixPath      = regexp.MustCompile(`/(?:Users|home|tmp|var|etc)/[^\s"']+`)
)

// RedactText removes common credential, identity, and absolute-path shapes.
func RedactText(value string) string {
	value = secretPattern.ReplaceAllString(value, "<REDACTED_SECRET>")
	value = emailPattern.ReplaceAllString(value, "<REDACTED_EMAIL>")
	value = windowsPath.ReplaceAllString(value, "<REDACTED_PATH>")
	value = unixPath.ReplaceAllString(value, "<REDACTED_PATH>")
	return value
}

// Sanitizer converts captured protocol messages into deterministic public fixtures.
type Sanitizer struct {
	workspace string
}

// NewSanitizer creates a fixture sanitizer for one temporary probe workspace.
func NewSanitizer(workspace string) *Sanitizer {
	return &Sanitizer{workspace: workspace}
}

// SanitizeJSONLine redacts a single JSON object and returns canonical compact JSON.
func (s *Sanitizer) SanitizeJSONLine(line []byte) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(line)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode fixture JSON: %w", err)
	}
	value = s.sanitizeValue("", value, true)
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode sanitized fixture JSON: %w", err)
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func (s *Sanitizer) sanitizeValue(key string, value any, topLevel bool) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			preserveTopLevelID := topLevel && childKey == "id"
			if !preserveTopLevelID {
				if replacement, ok := fixtureReplacement(childKey); ok {
					result[childKey] = replacement
					continue
				}
			}
			result[childKey] = s.sanitizeValue(childKey, childValue, false)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = s.sanitizeValue(key, item, false)
		}
		return result
	case string:
		if s.workspace != "" {
			typed = strings.ReplaceAll(typed, s.workspace, "<WORKSPACE>")
			typed = strings.ReplaceAll(typed, strings.ReplaceAll(s.workspace, `\`, `/`), "<WORKSPACE>")
		}
		return RedactText(typed)
	default:
		return value
	}
}

func fixtureReplacement(key string) (string, bool) {
	switch key {
	case "id":
		return "<ID>", true
	case "threadId":
		return "<THREAD_ID>", true
	case "turnId", "expectedTurnId":
		return "<TURN_ID>", true
	case "itemId":
		return "<ITEM_ID>", true
	case "sessionId":
		return "<SESSION_ID>", true
	case "cwd", "path", "grantRoot":
		return "<WORKSPACE>", true
	case "text", "preview":
		return "<REDACTED_TEXT>", true
	case "command", "aggregatedOutput":
		return "<REDACTED_COMMAND>", true
	case "diff":
		return "<REDACTED_DIFF>", true
	case "email":
		return "<REDACTED_EMAIL>", true
	case "reason", "message", "additionalDetails":
		return "<REDACTED_TEXT>", true
	default:
		return "", false
	}
}
