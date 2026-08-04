package statuscatalog

import "testing"

func TestRequiredMachineStatusesAreComplete(t *testing.T) {
	required := []string{"online", "offline", "reconnecting", "runtime_unavailable", "runtime_unverified", "history_gap", "lease_occupied", "lease_lost", "lease_expired", "approval_expired", "approval_ambiguous", "operation_unknown", "config_pending", "config_rejected", "config_failed", "certificate_expiring_30d", "certificate_expiring_14d", "certificate_expiring_7d", "backup_unavailable", "backup_invalid", "setup_required"}
	for _, code := range required {
		value, ok := Lookup(code)
		if !ok || value.Title == "" || value.Description == "" || value.Action == "" || value.Severity == "" {
			t.Fatalf("status %q is incomplete: %#v", code, value)
		}
	}
}
