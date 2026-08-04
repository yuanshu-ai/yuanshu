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
		return "certificate_expired"
	case remaining <= 7*24*time.Hour:
		return "certificate_expiring_7d"
	case remaining <= 14*24*time.Hour:
		return "certificate_expiring_14d"
	case remaining <= 30*24*time.Hour:
		return "certificate_expiring_30d"
	default:
		return ""
	}
}

func certificateExpiryWarningForProvider(provider string, now, notAfter time.Time) string {
	if provider != "acme-ip" {
		return certificateExpiryWarning(now, notAfter)
	}
	remaining := notAfter.Sub(now)
	switch {
	case remaining <= 0:
		return "certificate_expired"
	case remaining <= 12*time.Hour:
		return "certificate_expiring_12h"
	case remaining <= 24*time.Hour:
		return "certificate_expiring_24h"
	case remaining <= 48*time.Hour:
		return "certificate_expiring_48h"
	default:
		return ""
	}
}
