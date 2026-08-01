package credential

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCredentialRedactText(t *testing.T) {
	t.Parallel()

	canary := credentialCanary()
	awsCanary := "AKIA" + strings.Repeat("A", 12)
	input := strings.Join([]string{
		"OPENAI_API_KEY=" + canary,
		"ANTHROPIC_API_KEY='" + canary + "'",
		"AWS_SECRET_ACCESS_KEY=\"" + canary + "\"",
		"Authorization: Bearer " + canary,
		"Proxy-Authorization: Basic " + canary,
		"Cookie: session=" + canary + "; preference=private",
		"Set-Cookie: session=" + canary + "; Secure",
		`auth.json={"access_token":"` + canary + `","refresh_token":"` + canary + `"}`,
		`{"access_token":"` + canary + `","provider-api-key":"` + canary + `"}`,
		"provider token sk-" + canary,
		"aws access id " + awsCanary,
		"tokenUsage=42 requestId=req-public",
	}, "\n")

	redacted := RedactText(input)
	if strings.Contains(redacted, canary) || strings.Contains(redacted, awsCanary) {
		t.Fatal("credential canary remained in redacted text")
	}
	if !strings.Contains(redacted, redactedSecret) {
		t.Fatal("redacted text did not contain the replacement marker")
	}
	for _, retained := range []string{"tokenUsage=42", "requestId=req-public"} {
		if !strings.Contains(redacted, retained) {
			t.Fatal("non-credential diagnostic field was modified")
		}
	}
	if RedactText(redacted) != redacted {
		t.Fatal("credential text redaction is not idempotent")
	}
}

func TestCredentialRedactFields(t *testing.T) {
	t.Parallel()

	canary := credentialCanary()
	input := map[string]any{
		"requestId":     "req-public",
		"tokenUsage":    7,
		"Authorization": canary,
		"nested": map[string]any{
			"provider.api-key": canary,
			"message":          "OPENAI_API_KEY=" + canary,
		},
		"items": []any{
			map[string]string{"cookie": canary, "result": "ok"},
			"Bearer " + canary,
		},
	}

	redacted := RedactFields(input)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal("marshal redacted credential fields")
	}
	if strings.Contains(string(encoded), canary) {
		t.Fatal("credential canary remained in structured log fields")
	}
	if input["Authorization"] != canary {
		t.Fatal("RedactFields mutated its input")
	}
	if redacted["requestId"] != "req-public" || redacted["tokenUsage"] != 7 {
		t.Fatal("RedactFields modified non-credential fields")
	}
}

func TestCredentialSensitiveKeyNormalization(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"OPENAI_API_KEY", "provider-api-key", "providerApiKey", "accessToken",
		"access.token", "refresh_token", "refreshToken", "clientSecret",
		"Authorization", "Set-Cookie", "auth.json", "AWS_SECRET_ACCESS_KEY",
	} {
		if !isSensitiveKey(key) {
			t.Fatalf("expected key category to be sensitive")
		}
	}
	for _, key := range []string{"tokenUsage", "token_count", "requestId", "latencyMs"} {
		if isSensitiveKey(key) {
			t.Fatalf("non-credential field was classified as sensitive")
		}
	}
}

func credentialCanary() string {
	return strings.Join([]string{"yuanshu", "credential", "canary", "value"}, "-")
}
