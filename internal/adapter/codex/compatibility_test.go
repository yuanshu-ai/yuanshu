package codex

import "testing"

func TestCodexCompatibilityMatrix(t *testing.T) {
	tests := []struct {
		version string
		want    bool
		profile string
	}{
		{version: "0.144.6", want: true, profile: "stable-v2-0.144"},
		{version: "0.144.99", want: true, profile: "stable-v2-0.144"},
		{version: "0.145.0", want: false},
		{version: "0.146.0-alpha.9.1", want: false},
		{version: "0.146.0-alpha.9.2", want: true, profile: "stable-v2-0.146"},
		{version: "0.146.0-alpha.10", want: true, profile: "stable-v2-0.146"},
		{version: "0.146.99", want: true, profile: "stable-v2-0.146"},
		{version: "0.147.0", want: false},
		{version: "1.0.0", want: false},
		{version: "codex-cli 0.144.6", want: false},
		{version: "0.146", want: false},
	}
	for _, test := range tests {
		profile, got := compatibilityForVersion(test.version)
		if got != test.want {
			t.Fatalf("compatibilityForVersion(%q) = %v, want %v", test.version, got, test.want)
		}
		if got && profile.ID != test.profile {
			t.Fatalf("compatibilityForVersion(%q) profile = %q, want %q", test.version, profile.ID, test.profile)
		}
		if IsVersionCompatible(test.version) != test.want {
			t.Fatalf("IsVersionCompatible(%q) mismatch", test.version)
		}
	}
}

func TestCodexVersionOrderingHandlesPrereleases(t *testing.T) {
	ordered := []string{"0.146.0-alpha.9.2", "0.146.0-alpha.10", "0.146.0", "0.147.0"}
	for index := 1; index < len(ordered); index++ {
		left, leftOK := parseCodexVersion(ordered[index-1])
		right, rightOK := parseCodexVersion(ordered[index])
		if !leftOK || !rightOK || compareCodexVersions(left, right) >= 0 {
			t.Fatalf("version order is incorrect: %q before %q", ordered[index-1], ordered[index])
		}
	}
}
