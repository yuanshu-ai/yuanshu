// Package credential defines the credential filtering boundary shared by
// diagnostics and future structured application logging.
package credential

import (
	"regexp"
	"strings"
)

const redactedSecret = "<REDACTED_SECRET>"

var (
	authorizationHeaderPattern = regexp.MustCompile(`(?im)\b(authorization|proxy-authorization)(\s*:\s*)[^\r\n]+`)
	cookieHeaderPattern        = regexp.MustCompile(`(?im)\b(cookie|set-cookie)(\s*:\s*)[^\r\n]+`)
	authJSONPattern            = regexp.MustCompile(`(?im)\b(auth\.json)(\s*[:=]\s*)[^\r\n]+`)
	credentialSchemePattern    = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=:-]{4,}`)
	assignmentPattern          = regexp.MustCompile(`(?i)(["']?)([A-Za-z][A-Za-z0-9_.-]*)(["']?)(\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}\]\r\n]+)`)
	tokenShapePattern          = regexp.MustCompile(`(?i)\b(?:sk|sess)-[a-z0-9][a-z0-9_-]{3,}|\bgithub_pat_[a-z0-9_]{8,}|\bgh[pousr]_[a-z0-9]{8,}|\bxox[baprs]-[a-z0-9-]{8,}|\bAIza[a-z0-9_-]{12,}|\b(?:AKIA|ASIA)[A-Z0-9]{12,}|\beyJ[a-z0-9_-]{8,}\.[a-z0-9_-]{8,}\.[a-z0-9_-]{8,}`)
)

// RedactText replaces credential-shaped headers, assignments, and tokens.
// It never reads process environment variables or credential files.
func RedactText(value string) string {
	value = authorizationHeaderPattern.ReplaceAllString(value, `${1}${2}`+redactedSecret)
	value = cookieHeaderPattern.ReplaceAllString(value, `${1}${2}`+redactedSecret)
	value = authJSONPattern.ReplaceAllString(value, `${1}${2}`+redactedSecret)
	value = credentialSchemePattern.ReplaceAllString(value, redactedSecret)
	value = redactAssignments(value)
	return tokenShapePattern.ReplaceAllString(value, redactedSecret)
}

// RedactFields returns a deep copy suitable for structured logging. Values of
// credential-bearing keys are discarded, while other strings are filtered.
func RedactFields(fields map[string]any) map[string]any {
	if fields == nil {
		return nil
	}
	result := make(map[string]any, len(fields))
	for key, value := range fields {
		result[key] = redactValue(key, value)
	}
	return result
}

func redactAssignments(value string) string {
	matches := assignmentPattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value
	}

	var output strings.Builder
	output.Grow(len(value))
	last := 0
	for _, match := range matches {
		fullEnd := match[1]
		keyStart, keyEnd := match[4], match[5]
		separatorEnd := match[9]
		if !isSensitiveKey(value[keyStart:keyEnd]) {
			continue
		}
		output.WriteString(value[last:separatorEnd])
		if separatorEnd < fullEnd && (value[separatorEnd] == '"' || value[separatorEnd] == '\'') {
			output.WriteByte(value[separatorEnd])
			output.WriteString(redactedSecret)
			output.WriteByte(value[separatorEnd])
		} else {
			output.WriteString(redactedSecret)
		}
		last = fullEnd
	}
	if last == 0 {
		return value
	}
	output.WriteString(value[last:])
	return output.String()
}

func redactValue(key string, value any) any {
	if isSensitiveKey(key) {
		return redactedSecret
	}
	switch typed := value.(type) {
	case string:
		return RedactText(typed)
	case []byte:
		return RedactText(string(typed))
	case map[string]any:
		return RedactFields(typed)
	case map[string]string:
		result := make(map[string]string, len(typed))
		for childKey, childValue := range typed {
			if isSensitiveKey(childKey) {
				result[childKey] = redactedSecret
			} else {
				result[childKey] = RedactText(childValue)
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = redactValue("", item)
		}
		return result
	case []string:
		result := make([]string, len(typed))
		for i, item := range typed {
			result[i] = RedactText(item)
		}
		return result
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	normalized := normalizeKey(key)
	switch normalized {
	case "apikey", "api_key", "authorization", "proxy_authorization",
		"cookie", "set_cookie", "accesstoken", "access_token",
		"refreshtoken", "refresh_token", "idtoken", "id_token",
		"password", "secret", "clientsecret", "client_secret",
		"credential", "credentials", "auth_json",
		"awssecretaccesskey", "aws_secret_access_key",
		"awssessiontoken", "aws_session_token":
		return true
	}
	for _, suffix := range []string{
		"_api_key", "_access_token", "_refresh_token", "_id_token",
		"_secret", "_password", "_credential", "_credentials",
		"_cookie", "_authorization", "_secret_access_key", "_session_token",
	} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	for _, suffix := range []string{"apikey", "accesstoken", "refreshtoken", "clientsecret", "sessiontoken"} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func normalizeKey(key string) string {
	key = strings.Trim(strings.ToLower(strings.TrimSpace(key)), `"'`)
	replacer := strings.NewReplacer("-", "_", ".", "_", " ", "_")
	key = replacer.Replace(key)
	for strings.Contains(key, "__") {
		key = strings.ReplaceAll(key, "__", "_")
	}
	return key
}
