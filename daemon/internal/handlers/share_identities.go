package handlers

import (
	"database/sql"
	"log"

	"dplaned/internal/shareusers"
)

// Local users are also system and Samba accounts (internal/shareusers):
// before, they existed only in the database, so no one could log in to an
// SMB share and NFS had no owners to map.

// setSMBPassword stores the SMB (NT) hash of a password that was just set.
// Call it wherever a local user's password is set in plain text.
func setSMBPassword(db *sql.DB, username, password string) {
	sealed, err := shareusers.SealedNTHash(password)
	if err != nil {
		log.Printf("[shareusers] seal the SMB password of %s: %v", username, err)
		return
	}
	if _, err := db.Exec(`UPDATE users SET smb_nt_hash = $1 WHERE username = $2`, sealed, username); err != nil {
		log.Printf("[shareusers] store the SMB password of %s: %v", username, err)
	}
}

// syncShareIdentities applies users and groups to the system accounts and
// Samba. The text is for a response's "warning" ("" when all went well).
func syncShareIdentities(db *sql.DB) string {
	if db == nil {
		return ""
	}
	if err := shareusers.Sync(db); err != nil {
		return "File sharing accounts not fully updated: " + err.Error()
	}
	return ""
}

// SyncShareIdentitiesAtStartup recreates the accounts: an OTA update replaces
// /etc/passwd and /etc/group (the Samba database is kept).
func SyncShareIdentitiesAtStartup(db *sql.DB) {
	if w := syncShareIdentities(db); w != "" {
		log.Printf("WARN: %s", w)
	}
}

// withWarning adds a warning to a response payload.
func withWarning(payload map[string]any, warning string) map[string]any {
	if warning != "" {
		payload["warning"] = warning
	}
	return payload
}

// setSMBPasswordByID is setSMBPassword for a user given by ID.
func setSMBPasswordByID(db *sql.DB, id int, password string) {
	var name string
	if err := db.QueryRow(`SELECT username FROM users WHERE id = $1`, id).Scan(&name); err == nil {
		setSMBPassword(db, name, password)
	}
}
