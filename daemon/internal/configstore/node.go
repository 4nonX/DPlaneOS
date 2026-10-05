package configstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Node identity and mode (Design 0001 section 5.5).
//
// The node id is generated once per database, so two daemons sharing one
// database (an HA pair on Patroni) have the same id and cannot be paired:
// they already share their configuration.

const (
	settingNodeID           = "config_node_id"
	settingNodeMode         = "config_node_mode" // "" or "independent"
	settingIndependentSince = "config_independent_since"
)

func getSetting(db *sql.DB, key string) string {
	var v string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key = $1`, key).Scan(&v)
	return v
}

func setSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, key, value)
	return err
}

// NodeID returns this node's stable identity, creating it on first use.
func NodeID(db *sql.DB) (string, error) {
	if id := getSetting(db, settingNodeID); id != "" {
		return id, nil
	}
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ($1, gen_random_uuid()::text)
		ON CONFLICT (key) DO NOTHING`, settingNodeID); err != nil {
		return "", fmt.Errorf("creating node id: %w", err)
	}
	id := getSetting(db, settingNodeID)
	if id == "" {
		return "", errors.New("node id missing after creation")
	}
	return id, nil
}

// ── Peer HTTP client ──────────────────────────────────────────────────────────

const (
	hdrNode      = "X-DPlane-Node"
	hdrSecret    = "X-DPlane-Peer-Secret"
	hdrJoinToken = "X-DPlane-Join-Token"
)

// ErrFingerprintChanged: the peer presented a different TLS certificate than
// the one pinned when the nodes were paired.
var ErrFingerprintChanged = errors.New("TLS certificate of the other node changed")

// peerCall is one request to another node. Certificates are pinned on first
// use: NAS installations usually run self-signed certificates, so the CA
// chain is not checked; the fingerprint seen at pairing time is.
type peerCall struct {
	baseURL string
	pin     string // expected SHA-256 of the leaf certificate ("" = not pinned yet)
	headers map[string]string
	timeout time.Duration
	seen    string // fingerprint presented by the peer (https only)
}

func certFingerprint(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// FormatFingerprint renders a fingerprint as AA:BB:... for display.
func FormatFingerprint(fp string) string {
	var parts []string
	for i := 0; i+2 <= len(fp); i += 2 {
		parts = append(parts, strings.ToUpper(fp[i:i+2]))
	}
	return strings.Join(parts, ":")
}

func (c *peerCall) do(method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	timeout := c.timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // replaced by the pin check below
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return errors.New("no certificate presented")
				}
				c.seen = certFingerprint(cs.PeerCertificates[0].Raw)
				if c.pin != "" && c.seen != c.pin {
					return fmt.Errorf("%w: expected %s, got %s", ErrFingerprintChanged,
						FormatFingerprint(c.pin), FormatFingerprint(c.seen))
				}
				return nil
			},
		},
	}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: invalid response: %w", method, path, err)
		}
	}
	return nil
}
