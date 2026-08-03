package codex

import (
	"strconv"
	"strings"
)

// compatibilityProfile describes an app-server contract accepted by Yuanshu.
// The upper bound is exclusive so a new minor release cannot be accepted
// accidentally.
type compatibilityProfile struct {
	ID       string
	Minimum  string
	Maximum  string
	Protocol string
}

var codexCompatibilityMatrix = []compatibilityProfile{
	{
		ID: "stable-v2-0.144",
		// 0.144.6 is the current schema and integration-test baseline. Later
		// patch releases in this line keep the same app-server contract.
		Minimum:  BaselineVersion,
		Maximum:  "0.145.0",
		Protocol: ProtocolVersion,
	},
	{
		ID: "stable-v2-0.146",
		// Codex 0.146.0-alpha.9.2 is the current macOS installation. Its
		// initialize user-agent and stdio app-server surface remain stable-v2;
		// the full live Thread/Turn probe remains a release verification gate.
		Minimum:  "0.146.0-alpha.9.2",
		Maximum:  "0.147.0",
		Protocol: ProtocolVersion,
	},
}

type codexVersion struct {
	major      int
	minor      int
	patch      int
	prerelease []string
}

func compatibilityForVersion(value string) (compatibilityProfile, bool) {
	parsed, ok := parseCodexVersion(value)
	if !ok {
		return compatibilityProfile{}, false
	}
	for _, profile := range codexCompatibilityMatrix {
		minimum, minimumOK := parseCodexVersion(profile.Minimum)
		maximum, maximumOK := parseCodexVersion(profile.Maximum)
		if minimumOK && maximumOK && compareCodexVersions(parsed, minimum) >= 0 && compareCodexVersions(parsed, maximum) < 0 {
			return profile, true
		}
	}
	return compatibilityProfile{}, false
}

// IsVersionCompatible reports whether a detected Codex CLI version is in a
// accepted compatibility profile. Unknown versions remain unsupported until
// their app-server behavior is tested and added to the matrix.
func IsVersionCompatible(value string) bool {
	_, ok := compatibilityForVersion(value)
	return ok
}

func parseCodexVersion(value string) (codexVersion, bool) {
	if value == "" || strings.Contains(value, "+") {
		return codexVersion{}, false
	}
	core, prereleaseText, hasPrerelease := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return codexVersion{}, false
	}
	major, majorOK := parseVersionNumber(parts[0])
	minor, minorOK := parseVersionNumber(parts[1])
	patch, patchOK := parseVersionNumber(parts[2])
	if !majorOK || !minorOK || !patchOK {
		return codexVersion{}, false
	}
	version := codexVersion{major: major, minor: minor, patch: patch}
	if !hasPrerelease {
		return version, true
	}
	if prereleaseText == "" {
		return codexVersion{}, false
	}
	for _, identifier := range strings.Split(prereleaseText, ".") {
		if identifier == "" || strings.IndexFunc(identifier, func(r rune) bool {
			return !(r >= '0' && r <= '9') && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && r != '-'
		}) >= 0 {
			return codexVersion{}, false
		}
		version.prerelease = append(version.prerelease, identifier)
	}
	return version, true
}

func parseVersionNumber(value string) (int, bool) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	number, err := strconv.Atoi(value)
	return number, err == nil && number >= 0
}

func compareCodexVersions(left, right codexVersion) int {
	for _, pair := range [][2]int{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) > 0 {
		return 1
	}
	if len(left.prerelease) > 0 && len(right.prerelease) == 0 {
		return -1
	}
	for index := 0; index < len(left.prerelease) && index < len(right.prerelease); index++ {
		leftIdentifier, rightIdentifier := left.prerelease[index], right.prerelease[index]
		leftNumber, leftNumeric := parseVersionNumber(leftIdentifier)
		rightNumber, rightNumeric := parseVersionNumber(rightIdentifier)
		if leftNumeric && rightNumeric {
			if leftNumber < rightNumber {
				return -1
			}
			if leftNumber > rightNumber {
				return 1
			}
			continue
		}
		if leftNumeric != rightNumeric {
			if leftNumeric {
				return -1
			}
			return 1
		}
		if leftIdentifier < rightIdentifier {
			return -1
		}
		if leftIdentifier > rightIdentifier {
			return 1
		}
	}
	if len(left.prerelease) < len(right.prerelease) {
		return -1
	}
	if len(left.prerelease) > len(right.prerelease) {
		return 1
	}
	return 0
}
