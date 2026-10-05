package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"dplaned/internal/secrets"
)

type SecretsRotationHandler struct {
	db       *sql.DB
	keyPath  string
	haLocked bool
}

// DisableForHA makes RotateKeys refuse: in an HA pair both nodes share the key,
// and rotating it on one node would leave the other unable to decrypt.
func (h *SecretsRotationHandler) DisableForHA() { h.haLocked = true }

// SecretsStatus returns the startup secrets check.
// GET /api/system/secrets/status
func SecretsStatus(w http.ResponseWriter, r *http.Request) {
	chk := LastSecretsCheck()
	if chk == nil {
		respondOK(w, map[string]any{"success": true, "checked": false})
		return
	}
	respondOK(w, map[string]any{
		"success":       true,
		"checked":       true,
		"healthy":       len(chk.Undecryptable) == 0,
		"undecryptable": chk.Undecryptable,
		"resealed":      chk.Resealed,
		"fallback_key":  chk.FallbackKey,
	})
}

func NewSecretsRotationHandler(db *sql.DB, keyPath string) *SecretsRotationHandler {
	return &SecretsRotationHandler{db: db, keyPath: keyPath}
}

// errKeep tells reencryptAll to leave a value unchanged without aborting.
var errKeep = errors.New("keep value")

// reencryptAll passes every sealed value in the database through conv and
// stores the result. It is the single list of sealed columns: key rotation and
// the startup re-seal from a fallback key both use it, so a new sealed column
// only has to be added here.
//
// conv returns the new sealed value; errKeep leaves the value as it is; any
// other error aborts (the caller rolls back the transaction). Unchanged values
// are not written. Returns the number of values written.
func reencryptAll(tx *sql.Tx, conv func(label, sealed string) (string, error)) (int, error) {
	written := 0
	apply := func(label, old string, update func(newVal string) error) error {
		if old == "" {
			return nil
		}
		newVal, err := conv(label, old)
		if errors.Is(err, errKeep) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		if newVal == old {
			return nil
		}
		if err := update(newVal); err != nil {
			return fmt.Errorf("%s: update: %w", label, err)
		}
		written++
		return nil
	}
	single := func(label, query, update string) error {
		var v string
		if err := tx.QueryRow(query).Scan(&v); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("%s: %w", label, err)
		}
		return apply(label, v, func(n string) error { _, err := tx.Exec(update, n); return err })
	}

	if err := single("telegram bot_token",
		"SELECT COALESCE(bot_token,'') FROM telegram_config WHERE id=1",
		"UPDATE telegram_config SET bot_token=$1 WHERE id=1"); err != nil {
		return written, err
	}
	if err := single("ldap bind_password",
		"SELECT COALESCE(bind_password,'') FROM ldap_config WHERE id=1",
		"UPDATE ldap_config SET bind_password=$1 WHERE id=1"); err != nil {
		return written, err
	}
	if err := single("oidc client_secret",
		"SELECT COALESCE(client_secret,'') FROM oidc_config WHERE id=1",
		"UPDATE oidc_config SET client_secret=$1 WHERE id=1"); err != nil {
		return written, err
	}

	type row struct {
		id   int64
		a, b string
	}
	collect := func(label, query string, two bool) ([]row, error) {
		rows, err := tx.Query(query)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			if two {
				err = rows.Scan(&r.id, &r.a, &r.b)
			} else {
				err = rows.Scan(&r.id, &r.a)
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", label, err)
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}

	ad, err := collect("ad_domains", "SELECT id, COALESCE(bind_password,'') FROM ad_domains", false)
	if err != nil {
		return written, err
	}
	for _, r := range ad {
		id := r.id
		if err := apply(fmt.Sprintf("ad_domains id=%d", id), r.a, func(n string) error {
			_, err := tx.Exec("UPDATE ad_domains SET bind_password=$1 WHERE id=$2", n, id)
			return err
		}); err != nil {
			return written, err
		}
	}

	gc, err := collect("git_credentials", "SELECT id, COALESCE(token,''), COALESCE(ssh_key,'') FROM git_credentials", true)
	if err != nil {
		return written, err
	}
	for _, r := range gc {
		id := r.id
		if err := apply(fmt.Sprintf("git_credentials id=%d token", id), r.a, func(n string) error {
			_, err := tx.Exec("UPDATE git_credentials SET token=$1 WHERE id=$2", n, id)
			return err
		}); err != nil {
			return written, err
		}
		if err := apply(fmt.Sprintf("git_credentials id=%d ssh_key", id), r.b, func(n string) error {
			_, err := tx.Exec("UPDATE git_credentials SET ssh_key=$1 WHERE id=$2", n, id)
			return err
		}); err != nil {
			return written, err
		}
	}

	// Configuration sync peer secrets (text ids).
	type peerRow struct{ id, secret string }
	var peers []peerRow
	prows, err := tx.Query("SELECT id, secret FROM config_peers")
	if err != nil {
		return written, fmt.Errorf("config_peers: %w", err)
	}
	for prows.Next() {
		var pr peerRow
		if err := prows.Scan(&pr.id, &pr.secret); err != nil {
			prows.Close()
			return written, fmt.Errorf("config_peers: %w", err)
		}
		peers = append(peers, pr)
	}
	prows.Close()
	for _, pr := range peers {
		id := pr.id
		if err := apply("config_peers id="+id+" secret", pr.secret, func(n string) error {
			_, err := tx.Exec("UPDATE config_peers SET secret=$1 WHERE id=$2", n, id)
			return err
		}); err != nil {
			return written, err
		}
	}

	totp, err := collect("totp_secrets", "SELECT user_id, secret FROM totp_secrets WHERE secret != ''", false)
	if err != nil {
		return written, err
	}
	for _, r := range totp {
		id := r.id
		if err := apply(fmt.Sprintf("totp_secrets user_id=%d", id), r.a, func(n string) error {
			_, err := tx.Exec("UPDATE totp_secrets SET secret=$1 WHERE user_id=$2", n, id)
			return err
		}); err != nil {
			return written, err
		}
	}

	// SMTP password inside a JSON settings value. Only the password field is
	// replaced, so fields this code does not know about are preserved.
	var smtpValue string
	switch err := tx.QueryRow("SELECT COALESCE(value,'') FROM settings WHERE key='smtp_config'").Scan(&smtpValue); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return written, fmt.Errorf("smtp_config: %w", err)
	case smtpValue != "":
		var cfg map[string]any
		if err := json.Unmarshal([]byte(smtpValue), &cfg); err != nil {
			return written, fmt.Errorf("smtp_config: %w", err)
		}
		if pw, _ := cfg["password"].(string); pw != "" {
			if err := apply("smtp_config.password", pw, func(n string) error {
				cfg["password"] = n
				updated, err := json.Marshal(cfg)
				if err != nil {
					return err
				}
				_, err = tx.Exec("UPDATE settings SET value=$1, updated_at=NOW() WHERE key='smtp_config'", string(updated))
				return err
			}); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

// RotateKeys re-encrypts every stored secret under a new AES-256-GCM key.
// POST /api/system/secrets/rotate
func (h *SecretsRotationHandler) RotateKeys(w http.ResponseWriter, r *http.Request) {
	if h.haLocked {
		respondErrorSimple(w, "Key rotation is not available while HA is enabled: both nodes share the secrets key, "+
			"and rotating it on one node would leave the other unable to decrypt", http.StatusConflict)
		return
	}
	openOld, sealNew, commit, err := secrets.PrepareRotation(h.keyPath)
	if err != nil {
		respondErrorSimple(w, "Failed to prepare rotation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	tx, err := h.db.Begin()
	if err != nil {
		respondErrorSimple(w, "Failed to begin transaction", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	rotated, err := reencryptAll(tx, func(_, sealed string) (string, error) {
		plain, err := openOld(sealed)
		if err != nil {
			return "", err
		}
		return sealNew(plain)
	})
	if err != nil {
		log.Printf("ROTATE: %v", err)
		respondErrorSimple(w, "Failed to re-encrypt secrets: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		respondErrorSimple(w, "Failed to commit re-encrypted secrets", http.StatusInternalServerError)
		return
	}

	if err := commit(); err != nil {
		log.Printf("ROTATE: key file commit failed after DB commit: %v", err)
		respondErrorSimple(w, "Secrets re-encrypted but key file write failed - restart daemon immediately: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("SECRETS ROTATE: rotated %d secret values under new key", rotated)
	respondOK(w, map[string]any{"success": true, "rotated_count": rotated})
}

// SecretsCheck is the result of the startup secrets check.
type SecretsCheck struct {
	Resealed      int      `json:"resealed"`      // values moved from the fallback key to the active key
	Undecryptable []string `json:"undecryptable"` // values no configured key can open
	FallbackKey   bool     `json:"fallback_key"`  // a fallback key was configured
}

var (
	lastSecretsCheck *SecretsCheck
)

// LastSecretsCheck returns the startup check result (nil before it ran).
func LastSecretsCheck() *SecretsCheck { return lastSecretsCheck }

// CheckAndResealSecrets opens every sealed value at startup. Values only the
// fallback key opens (sealed by the other node of an HA pair before the pair
// shared one key) are re-sealed under the active key. Values no key opens are
// reported: this node cannot use them, typically because they were sealed by a
// peer whose key this node does not have.
func CheckAndResealSecrets(db *sql.DB) (*SecretsCheck, error) {
	res := &SecretsCheck{FallbackKey: secrets.HasFallback()}
	defer func() { lastSecretsCheck = res }()
	tx, err := db.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	n, err := reencryptAll(tx, func(label, sealed string) (string, error) {
		if _, err := secrets.OpenActive(sealed); err == nil {
			return sealed, nil
		}
		plain, err := secrets.OpenFallback(sealed)
		if err != nil {
			res.Undecryptable = append(res.Undecryptable, label)
			return "", errKeep
		}
		return secrets.Seal(plain)
	})
	if err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.Resealed = n
	return res, nil
}
