package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// computeRowHash computes HMAC-SHA256(key, prevHash|ts|user|action|resource|details|ipAddress|success).
// Returns "" when key is nil (chain disabled — backwards compatible with pre-Phase-1.5 rows).
//
// This formula is also replicated in handlers/audit_verify.go (verifyComputeHash).
// If you change it here, update it there too.
func computeRowHash(key []byte, prevHash string, e AuditEvent) string {
	if len(key) == 0 {
		return ""
	}
	// AuditEvent.Timestamp is int64 (Unix seconds).
	// We format it as a decimal string for stable byte representation.
	msg := fmt.Sprintf("%s|%d|%s|%s|%s|%s|%s|%v",
		prevHash,
		e.Timestamp,
		e.User,
		e.Action,
		e.Resource,
		e.Details,
		e.IPAddress,
		e.Success,
	)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}
