package handlers

// audit_verify.go — Audit HMAC chain verification endpoint.
// Kept in its own file to isolate crypto imports from enterprise_hardening.go.

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
)

const auditKeyPath = "/var/lib/dplaneos/audit.key"
const auditDBPath  = "/var/lib/dplaneos/dplaneos.db"

// verifyComputeHash re-implements the HMAC formula from buffered_logger.go.
// Must stay in sync with computeRowHash() in the audit package.
// Formula: HMAC-SHA256(key, prevHash|ts|user|action|resource|details|ipAddress|success)
// verifyComputeHash replicates the formula in audit/chain.go computeRowHash.
// MUST stay in sync: if chain.go changes, update here too.
func verifyComputeHash(key []byte, prevHash string, ts int64, user, action, resource, details, ipAddress string, success bool) string {
	msg := fmt.Sprintf("%s|%d|%s|%s|%s|%s|%s|%v",
		prevHash, ts, user, action, resource, details, ipAddress, success,
	)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyAuditChain re-computes every row_hash in audit_logs and confirms the chain
// is intact. Rows with an empty row_hash (written before Phase 1.5 migration)
// are counted but skipped — verification starts from the first chained row.
//
// GET /api/system/audit/verify-chain
func (h *AuditRotationHandler) VerifyAuditChain(w http.ResponseWriter, r *http.Request) {
	// Load the HMAC key — must be the same key the daemon uses for writing
	keyBytes, err := os.ReadFile(auditKeyPath)
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"valid":   false,
			"error":   "Audit key not available. The daemon must have written at least one audit row.",
		})
		return
	}
	if len(keyBytes) != 32 {
		respondOK(w, map[string]interface{}{
			"success": false,
			"valid":   false,
			"error":   fmt.Sprintf("Audit key has unexpected length %d (want 32). Key file may be corrupt.", len(keyBytes)),
		})
		return
	}

	db, err := sql.Open("sqlite3", auditDBPath+"?_journal_mode=WAL&_busy_timeout=30000&cache=shared&_synchronous=FULL")
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to open database", err)
		return
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT id, timestamp, user, action, resource, details, ip_address, success, prev_hash, row_hash
		FROM audit_logs
		ORDER BY id ASC
	`)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to query audit_logs", err)
		return
	}
	defer rows.Close()

	var (
		total          int
		skipped        int  // rows pre-dating the chain (empty row_hash)
		checked        int
		valid          = true
		firstBrokenID  int64
	)

	// prevHashSeen tracks the row_hash of the previous chained row.
	// On the very first chained row, storedPrevHash should be "" (genesis).
	prevHashSeen := ""
	chainStarted := false

	for rows.Next() {
		var (
			id             int64
			ts             int64  // stored as Unix epoch integer
			user           string
			action         string
			resource       string
			details        string
			ipAddress      string
			successInt     int
			storedPrevHash string
			storedRowHash  string
		)
		if err := rows.Scan(&id, &ts, &user, &action, &resource, &details,
			&ipAddress, &successInt, &storedPrevHash, &storedRowHash); err != nil {
			continue
		}
		total++

		// Skip legacy rows (pre-migration)
		if storedRowHash == "" {
			skipped++
			continue
		}

		// On the first chained row, seed prevHashSeen from what the row claims as its prev_hash.
		// This handles the transition from legacy to chained rows correctly:
		// the first chained row always has prev_hash = "" (no predecessor in chain).
		if !chainStarted {
			chainStarted = true
			prevHashSeen = storedPrevHash
		}

		// Re-compute the hash
		successBool := successInt != 0
		computed := verifyComputeHash(keyBytes, prevHashSeen, ts, user, action, resource, details, ipAddress, successBool)

		if computed != storedRowHash {
			valid = false
			if firstBrokenID == 0 {
				firstBrokenID = id
			}
		}
		// Advance chain regardless — report all broken rows, not just the first
		prevHashSeen = storedRowHash
		checked++
	}

	result := map[string]interface{}{
		"success":       true,
		"valid":         valid,
		"total_rows":    total,
		"checked_rows":  checked,
		"skipped_rows":  skipped, // pre-migration rows without a hash
	}
	if !valid && firstBrokenID != 0 {
		result["first_broken_at_id"] = firstBrokenID
		result["message"] = fmt.Sprintf("Chain broken at row id=%d. Rows after that point may have been tampered with.", firstBrokenID)
	} else if valid && checked > 0 {
		result["message"] = fmt.Sprintf("Chain intact. %d rows verified.", checked)
	} else if checked == 0 {
		result["message"] = fmt.Sprintf("No chained rows found yet. %d legacy rows skipped.", skipped)
	}

	respondOK(w, result)
}
