package configstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"dplaned/internal/gitops"
	"dplaned/internal/secrets"
)

// Secret material between paired nodes (Design 0001 section 5.9).
//
// Revisions never store secrets: a secret field holds a fingerprint of the
// plaintext, so history shows that it changed and both nodes can tell
// whether they hold the same value. When a node adopts a revision whose
// secret changed, it fetches the material from the node that wrote it
// (GET /api/config/sync/peer/secret). The material travels encrypted with a
// key derived from the pair's peer secret, so it is protected even over a
// plain-HTTP peer URL; the receiver checks it against the revision's
// fingerprints and stores sealed values under its own secrets key. No node
// ever needs another node's secrets key.
//
// Covered: user credentials (password hash, SCRAM verifiers, TOTP secret
// and backup codes), the LDAP bind password, ACME DNS-provider credentials,
// certificate private keys.

// SecretKinds carry secret material.
var SecretKinds = map[string]bool{KindUser: true, KindLDAP: true, KindACME: true, KindCertificate: true}

// ErrSecretChanged: the other node's current secret is not the one the
// revision refers to (it changed again; a newer revision follows).
var ErrSecretChanged = errors.New("the other node's secret changed since this revision")

// Material is a resource's secret values (plaintext), by field.
type Material map[string]string

// credentialFields of a user, in fingerprint order.
var credentialFields = []string{"password_hash", "must_change_password", "totp_enabled",
	"scram_salt", "scram_iterations", "scram_stored_key", "scram_server_key",
	"totp_secret", "totp_secret_enabled", "totp_backup_codes"}

// credentialsFingerprint is the fingerprint of a user's credentials.
func credentialsFingerprint(m Material) string {
	if m == nil || m["password_hash"] == "" {
		return ""
	}
	var b strings.Builder
	for _, f := range credentialFields {
		b.WriteString(f)
		b.WriteByte('=')
		b.WriteString(m[f])
		b.WriteByte('\n')
	}
	return fingerprint(b.String())
}

// ReadMaterial reads the secret material of a resource on this node.
func ReadMaterial(db *sql.DB, kind, key string) (Material, error) {
	switch kind {
	case KindUser:
		m := Material{}
		var mustChange, totpEnabled, iterations int
		var hash, salt, stored, server string
		err := db.QueryRow(`SELECT password_hash, must_change_password, totp_enabled, scram_salt, scram_iterations,
			scram_stored_key, scram_server_key FROM users WHERE username = $1`, key).
			Scan(&hash, &mustChange, &totpEnabled, &salt, &iterations, &stored, &server)
		if err != nil {
			return nil, err
		}
		m["password_hash"], m["must_change_password"], m["totp_enabled"] = hash, strconv.Itoa(mustChange), strconv.Itoa(totpEnabled)
		m["scram_salt"], m["scram_iterations"], m["scram_stored_key"], m["scram_server_key"] = salt, strconv.Itoa(iterations), stored, server
		var sealed, codes string
		var enabled int
		err = db.QueryRow(`SELECT t.secret, t.enabled, t.backup_codes FROM totp_secrets t JOIN users u ON u.id = t.user_id
			WHERE u.username = $1`, key).Scan(&sealed, &enabled, &codes)
		if err == nil {
			plain, err := secrets.Open(sealed)
			if err != nil {
				return nil, fmt.Errorf("opening the TOTP secret of %s: %w", key, err)
			}
			m["totp_secret"], m["totp_secret_enabled"], m["totp_backup_codes"] = plain, strconv.Itoa(enabled), codes
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return m, nil
	case KindLDAP:
		var sealed string
		if err := db.QueryRow(`SELECT COALESCE(bind_password, '') FROM ldap_config WHERE id = 1`).Scan(&sealed); err != nil {
			return nil, err
		}
		plain := ""
		if sealed != "" {
			var err error
			if plain, err = secrets.Open(sealed); err != nil {
				return nil, fmt.Errorf("opening the LDAP bind password: %w", err)
			}
		}
		return Material{"bind_password": plain}, nil
	case KindACME:
		var raw string
		if err := db.QueryRow(`SELECT COALESCE(dns_config, '{}') FROM acme_config WHERE id = 1`).Scan(&raw); err != nil {
			return nil, err
		}
		var cfg map[string]string
		_ = json.Unmarshal([]byte(raw), &cfg)
		m := Material{}
		for k, v := range cfg {
			m["dns_config."+k] = v
		}
		return m, nil
	case KindCertificate:
		var keyPEM string
		if err := db.QueryRow(`SELECT key_pem FROM certificates WHERE name = $1`, key).Scan(&keyPEM); err != nil {
			return nil, err
		}
		return Material{"key": keyPEM}, nil
	}
	return nil, fmt.Errorf("%s carries no secrets", kind)
}

// secretFingerprints puts the fingerprints of this node's secret material
// into captured payloads (users: "credentials"; LDAP: bind_password from the
// plaintext, not from this node's ciphertext).
func secretFingerprints(db *sql.DB, rs []Resource) {
	for i := range rs {
		r := &rs[i]
		if r.Payload == nil {
			continue
		}
		switch r.Kind {
		case KindUser:
			if m, err := ReadMaterial(db, KindUser, r.Key); err == nil {
				r.Payload["credentials"] = credentialsFingerprint(m)
			}
		case KindLDAP:
			if m, err := ReadMaterial(db, KindLDAP, "ldap"); err == nil {
				r.Payload["bind_password"] = fingerprint(m["bind_password"])
			}
		}
	}
}

// materialMatches checks fetched material against a revision's fingerprints.
func materialMatches(r Resource, m Material) error {
	p := r.Payload
	switch r.Kind {
	case KindUser:
		want, _ := p["credentials"].(string)
		if want != "" && credentialsFingerprint(m) != want {
			return ErrSecretChanged
		}
	case KindLDAP:
		want, _ := p["bind_password"].(string)
		if fingerprint(m["bind_password"]) != want {
			return ErrSecretChanged
		}
	case KindACME:
		dns, _ := p["dns_config"].(map[string]any)
		for k, v := range dns {
			if want, _ := v.(string); fingerprint(m["dns_config."+k]) != want {
				return ErrSecretChanged
			}
		}
	case KindCertificate:
		want, _ := p["key"].(string)
		if fingerprint(m["key"]) != want {
			return ErrSecretChanged
		}
	}
	return nil
}

// withMaterial returns the resource with plaintext secrets in its payload
// where the GitOps engine applies them (ACME DNS credentials, certificate
// key); user credentials and the LDAP bind password are written by
// writeMaterial instead (sealed, outside the engine).
func withMaterial(r Resource, m Material) Resource {
	if r.Payload == nil {
		return r
	}
	p := make(map[string]any, len(r.Payload))
	for k, v := range r.Payload {
		p[k] = v
	}
	switch r.Kind {
	case KindACME:
		if dns, ok := p["dns_config"].(map[string]any); ok {
			plain := make(map[string]any, len(dns))
			for k := range dns {
				plain[k] = m["dns_config."+k]
			}
			p["dns_config"] = plain
		}
	case KindCertificate:
		p["key"] = m["key"]
	case KindLDAP:
		p["bind_password"] = "" // written sealed by writeMaterial
	case KindUser:
		delete(p, "credentials")
		p["password_hash"] = ""
	}
	r.Payload = p
	return r
}

// writeMaterial stores the material the engine does not apply.
func writeMaterial(db *sql.DB, r Resource, m Material) error {
	switch r.Kind {
	case KindUser:
		if m["password_hash"] == "" {
			return nil
		}
		atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var uid int64
		if err := tx.QueryRow(`UPDATE users SET password_hash = $2, must_change_password = $3, totp_enabled = $4,
			scram_salt = $5, scram_iterations = $6, scram_stored_key = $7, scram_server_key = $8, updated_at = NOW()
			WHERE username = $1 RETURNING id`, r.Key, m["password_hash"], atoi(m["must_change_password"]),
			atoi(m["totp_enabled"]), m["scram_salt"], atoi(m["scram_iterations"]), m["scram_stored_key"],
			m["scram_server_key"]).Scan(&uid); err != nil {
			return fmt.Errorf("storing the credentials of %s: %w", r.Key, err)
		}
		if m["totp_secret"] == "" {
			if _, err := tx.Exec(`DELETE FROM totp_secrets WHERE user_id = $1`, uid); err != nil {
				return err
			}
		} else {
			sealed, err := secrets.Seal(m["totp_secret"])
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO totp_secrets (user_id, secret, enabled, backup_codes) VALUES ($1, $2, $3, $4)
				ON CONFLICT (user_id) DO UPDATE SET secret = EXCLUDED.secret, enabled = EXCLUDED.enabled,
					backup_codes = EXCLUDED.backup_codes`, uid, sealed, atoi(m["totp_secret_enabled"]), m["totp_backup_codes"]); err != nil {
				return err
			}
		}
		return tx.Commit()
	case KindLDAP:
		sealed := ""
		if m["bind_password"] != "" {
			var err error
			if sealed, err = secrets.Seal(m["bind_password"]); err != nil {
				return err
			}
		}
		_, err := db.Exec(`UPDATE ldap_config SET bind_password = $1 WHERE id = 1`, sealed)
		return err
	}
	return nil
}

// needsMaterial reports whether applying r needs secret material: a secret
// kind whose payload refers to a non-empty secret.
func needsMaterial(r Resource) bool {
	if !SecretKinds[r.Kind] || r.Payload == nil {
		return false
	}
	switch r.Kind {
	case KindUser:
		s, _ := r.Payload["credentials"].(string)
		return s != ""
	case KindLDAP:
		s, _ := r.Payload["bind_password"].(string)
		return s != ""
	case KindACME:
		dns, _ := r.Payload["dns_config"].(map[string]any)
		return len(dns) > 0
	case KindCertificate:
		s, _ := r.Payload["key"].(string)
		return s != ""
	}
	return false
}

// ── Transport ─────────────────────────────────────────────────────────────────

func transportKey(peerSecret string) ([]byte, error) {
	return hkdf.Key(sha256.New, []byte(peerSecret), nil, "dplaneos config secrets v1", 32)
}

// sealedMaterial is the wire form of Material.
type sealedMaterial struct {
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

func sealForPeer(peerSecret, kind, key string, m Material) (*sealedMaterial, error) {
	k, err := transportKey(peerSecret)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, _ := json.Marshal(m)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// The resource is bound as additional data: material for one resource
	// cannot be replayed as another's.
	return &sealedMaterial{Nonce: nonce, Ciphertext: gcm.Seal(nil, nonce, plain, []byte(kind+"/"+key))}, nil
}

func openFromPeer(peerSecret, kind, key string, s *sealedMaterial) (Material, error) {
	k, err := transportKey(peerSecret)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(s.Nonce) != gcm.NonceSize() {
		return nil, errors.New("invalid secret material from the other node")
	}
	plain, err := gcm.Open(nil, s.Nonce, s.Ciphertext, []byte(kind+"/"+key))
	if err != nil {
		return nil, errors.New("cannot decrypt the secret material from the other node")
	}
	var m Material
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ServeMaterial answers a peer's request for a resource's secret material.
func ServeMaterial(db *sql.DB, peer *Peer, kind, key string) (*sealedMaterial, error) {
	if !SecretKinds[kind] {
		return nil, fmt.Errorf("%s carries no secrets", kind)
	}
	m, err := ReadMaterial(db, kind, key)
	if err != nil {
		return nil, err
	}
	return sealForPeer(peer.secret, kind, key, m)
}

// fetchMaterial asks a peer for a resource's secret material.
func fetchMaterial(db *sql.DB, p Peer, kind, key string) (Material, error) {
	var resp struct {
		Material *sealedMaterial `json:"material"`
	}
	path := "/api/config/sync/peer/secret?kind=" + url.QueryEscape(kind) + "&key=" + url.QueryEscape(key)
	if err := CallPeer(db, p.ID, "GET", path, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Material == nil {
		return nil, errors.New("the other node sent no secret material")
	}
	return openFromPeer(p.secret, kind, key, resp.Material)
}

// errMaterialUnavailable: the secret material could not be fetched now.
var errMaterialUnavailable = errors.New("secret material not available")

// applyRemote applies another node's revision of a resource, with its secret
// material: this node's own when it already matches the revision, else the
// material fetched from the peer.
func (s *Syncer) applyRemote(p Peer, r Resource, live *gitops.LiveState) error {
	var m Material
	if needsMaterial(r) {
		if local, err := ReadMaterial(s.db, r.Kind, r.Key); err == nil && materialMatches(r, local) == nil {
			m = local
		} else {
			fetched, err := fetchMaterial(s.db, p, r.Kind, r.Key)
			if err != nil {
				return fmt.Errorf("%w: %v", errMaterialUnavailable, err)
			}
			if err := materialMatches(r, fetched); err != nil {
				return err
			}
			m = fetched
		}
		r = withMaterial(r, m)
	}
	if _, err := applyOne(s.ctx, r, live); err != nil {
		return err
	}
	if m != nil {
		return writeMaterial(s.db, r, m)
	}
	return nil
}
