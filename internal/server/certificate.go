package server

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

func certificateFingerprint(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func certificateExpiryWarning(now, notAfter time.Time) string {
	remaining := notAfter.Sub(now)
	switch {
	case remaining <= 0:
		return "expired"
	case remaining <= 7*24*time.Hour:
		return "expires_within_7_days"
	case remaining <= 14*24*time.Hour:
		return "expires_within_14_days"
	case remaining <= 30*24*time.Hour:
		return "expires_within_30_days"
	default:
		return ""
	}
}
