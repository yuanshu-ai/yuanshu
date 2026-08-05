package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestApprovalClaimIsSingleUseAndExpires(t *testing.T) {
	local, _ := openTestStore(t)
	now := fixedNow
	record := ApprovalRecord{
		ApprovalID: "approval", WorkspaceID: "workspace", ThreadID: "thread", TurnID: "turn", ItemID: "item",
		Status: ApprovalPending, OperationDigest: strings.Repeat("A", 43), Payload: []byte(`{"approvalId":"approval"}`),
		ExpiresAt: now.Add(time.Minute), UpdatedAt: now,
	}
	if err := local.SaveApproval(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	claim := ApprovalClaim{ApprovalID: "approval", WorkspaceID: "workspace", ThreadID: "thread", TurnID: "turn", ItemID: "item", OperationDigest: record.OperationDigest, Decision: "accept", Now: now}
	claimed, err := local.ClaimApproval(context.Background(), claim)
	if err != nil || claimed.Status != ApprovalProcessing {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if _, err := local.ClaimApproval(context.Background(), claim); !errors.Is(err, ErrConflict) {
		t.Fatalf("second claim error=%v", err)
	}
	if err := local.MarkApprovalAmbiguous(context.Background(), "approval"); err != nil {
		t.Fatal(err)
	}
	ambiguous, err := local.Approval(context.Background(), "approval")
	if err != nil || ambiguous.Status != ApprovalAmbiguous {
		t.Fatalf("ambiguous=%+v err=%v", ambiguous, err)
	}

	expired := record
	expired.ApprovalID = "expired"
	expired.ExpiresAt = now.Add(-time.Second)
	if err := local.SaveApproval(context.Background(), expired); err != nil {
		t.Fatal(err)
	}
	claim.ApprovalID = "expired"
	claim.Now = now
	if _, err := local.ClaimApproval(context.Background(), claim); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired claim error=%v", err)
	}
	stored, err := local.Approval(context.Background(), "expired")
	if err != nil || stored.Status != ApprovalExpired {
		t.Fatalf("expired status=%+v err=%v", stored, err)
	}

	interaction := record
	interaction.ApprovalID = "interaction"
	if err := local.SaveApproval(context.Background(), interaction); err != nil {
		t.Fatal(err)
	}
	claim = ApprovalClaim{ApprovalID: "interaction", WorkspaceID: "workspace", ThreadID: "thread", TurnID: "turn", ItemID: "item", OperationDigest: record.OperationDigest, Decision: "answer", Now: now}
	if claimed, err := local.ClaimApproval(context.Background(), claim); err != nil || claimed.Status != ApprovalProcessing {
		t.Fatalf("interaction claim=%+v err=%v", claimed, err)
	}
}
