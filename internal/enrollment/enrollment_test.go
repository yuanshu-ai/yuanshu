package enrollment

import (
	"bytes"
	"testing"
	"time"
)

func TestNodeEnrollmentAndRevocationBindingsCoverOwnership(t *testing.T) {
	expires := time.Date(2026, 8, 2, 10, 5, 0, 0, time.UTC).Format(time.RFC3339Nano)
	value := NodeEnrollmentDecision{Version: "1", EnrollmentID: "enrollment", OwnerID: "owner", IssuerNodeID: "issuer", CandidateNodeID: "candidate", CandidatePublicKey: "public", CredentialHash: "digest", Name: "Home PC", OS: "windows", NodeVersion: "dev", Decision: "accept", ExpiresAt: expires}
	input, err := NodeEnrollmentDecisionSigningInput(value)
	if err != nil {
		t.Fatal(err)
	}
	changed := value
	changed.CandidateNodeID = "other"
	other, err := NodeEnrollmentDecisionSigningInput(changed)
	if err != nil || bytes.Equal(input, other) {
		t.Fatal("candidate ownership was not signed")
	}
	revocation := NodeRevocation{Version: "1", OwnerID: "owner", IssuerNodeID: "issuer", TargetNodeID: "candidate", IssuedAt: expires}
	if _, err := NodeRevocationSigningInput(revocation); err != nil {
		t.Fatal(err)
	}
	revocation.TargetNodeID = "issuer"
	if _, err := NodeRevocationSigningInput(revocation); err == nil {
		t.Fatal("self revocation binding accepted")
	}
}
