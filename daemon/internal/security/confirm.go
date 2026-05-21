package security

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const confirmTTL = 60 * time.Second

// validConfirmOps is the exhaustive set of operations that accept a confirmation token.
// Any name not in this set is rejected at the issue step.
var validConfirmOps = map[string]bool{
	"pool_destroy":  true,
	"pool_export":   true,
	"docker_remove": true,
	"docker_prune":  true,
	"docker_rmi":    true,
	"zvol_destroy":  true,
}

// ValidConfirmOp returns true if op is a known destructive operation name.
func ValidConfirmOp(op string) bool { return validConfirmOps[op] }

type confirmEntry struct {
	Operation string
	Target    string
	UserID    int
	ExpiresAt time.Time
}

var (
	confirmMu    sync.Mutex
	confirmStore = make(map[string]confirmEntry)
)

// IssueConfirmToken mints a 48-hex-char single-use token scoped to
// operation + target + userID. Expired tokens are purged inline on
// each issue call (bounded by the number of concurrent in-flight
// destructive requests, which is tiny in practice).
func IssueConfirmToken(operation, target string, userID int) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)

	confirmMu.Lock()
	now := time.Now()
	for k, v := range confirmStore {
		if v.ExpiresAt.Before(now) {
			delete(confirmStore, k)
		}
	}
	confirmStore[token] = confirmEntry{
		Operation: operation,
		Target:    target,
		UserID:    userID,
		ExpiresAt: now.Add(confirmTTL),
	}
	confirmMu.Unlock()

	return token, nil
}

// ConsumeConfirmToken validates and atomically removes a confirmation token.
// The token is always deleted on first call - even if validation fails -
// to prevent retries with the same token.
// Returns false if the token is absent, expired, or does not match
// operation/target/userID.
func ConsumeConfirmToken(token, operation, target string, userID int) bool {
	confirmMu.Lock()
	defer confirmMu.Unlock()

	entry, ok := confirmStore[token]
	delete(confirmStore, token)
	if !ok {
		return false
	}
	if time.Now().After(entry.ExpiresAt) {
		return false
	}
	return entry.Operation == operation && entry.Target == target && entry.UserID == userID
}
