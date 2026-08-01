package probe

import (
	"strings"
	"testing"
)

func TestRedactText(t *testing.T) {
	t.Parallel()

	input := `Bearer secret-token-123 sk-testtoken123456 user@example.test C:\Users\person\project /home/person/project`
	got := RedactText(input)
	for _, forbidden := range []string{"secret-token", "sk-test", "user@example", `C:\Users`, "/home/person"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("RedactText() retained %q in %q", forbidden, got)
		}
	}
}

func TestSanitizeJSONLine(t *testing.T) {
	t.Parallel()

	workspace := `C:\Users\person\probe-workspace`
	line := `{"id":7,"method":"item/completed","params":{"threadId":"thr-secret","turnId":"turn-secret","item":{"id":"item-secret","type":"fileChange","cwd":"C:\\Users\\person\\probe-workspace","path":"/tmp/private/file","text":"private prompt sk-testtoken123","email":"user@example.test","diff":"private diff"}}}`
	sanitized, err := NewSanitizer(workspace).SanitizeJSONLine([]byte(line))
	if err != nil {
		t.Fatalf("SanitizeJSONLine() error = %v", err)
	}
	got := string(sanitized)
	for _, forbidden := range []string{"thr-secret", "turn-secret", "item-secret", "private prompt", "private diff", "sk-testtoken", "user@example", `C:\\Users`, "/tmp/private"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("SanitizeJSONLine() retained %q in %s", forbidden, got)
		}
	}
	for _, required := range []string{"<THREAD_ID>", "<TURN_ID>", "<WORKSPACE>", "<REDACTED_DIFF>"} {
		if !strings.Contains(got, required) {
			t.Fatalf("SanitizeJSONLine() missing %q in %s", required, got)
		}
	}
}
