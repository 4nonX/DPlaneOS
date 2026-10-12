package bmc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"dplaned/internal/secrets"
)

// Self is the entry of this system's own BMC in power_targets (the hardware
// page and the metrics). Every other entry is how this node powers another
// node off; see internal/ha.
const Self = ""

// ErrNotConfigured: no BMC address is stored.
var ErrNotConfigured = errors.New("no BMC is configured for this system (System, IPMI: BMC settings)")

// LoadFromDB reads the credentials and the pinned TLS certificate of this
// system's own BMC.
func LoadFromDB(db *sql.DB) (Credentials, PinnedCert, error) { return LoadNode(db, Self) }

// LoadNode reads the BMC credentials stored for a node (Self: this system).
// The password is stored sealed and opened here, for the caller's request.
func LoadNode(db *sql.DB, node string) (Credentials, PinnedCert, error) {
	var host, user, sealed, fingerprint string
	var pinnedAt sql.NullTime
	err := db.QueryRow(`SELECT host, username, password, tls_fingerprint, tls_pinned_at
		FROM power_targets WHERE node = $1 AND kind = 'bmc'`, node).Scan(&host, &user, &sealed, &fingerprint, &pinnedAt)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && host == "") {
		return Credentials{}, PinnedCert{}, ErrNotConfigured
	}
	if err != nil {
		return Credentials{}, PinnedCert{}, fmt.Errorf("load BMC settings: %w", err)
	}
	creds := Credentials{Host: host, Username: user}
	if sealed != "" {
		if creds.Password, err = secrets.Open(sealed); err != nil {
			return Credentials{}, PinnedCert{}, fmt.Errorf("the stored BMC password cannot be read: %w", err)
		}
	}
	pin := PinnedCert{Fingerprint: fingerprint}
	if pinnedAt.Valid {
		pin.PinnedAt = pinnedAt.Time
	}
	return creds, pin, nil
}

// SaveTLSFingerprint stores the certificate fingerprint captured for a
// node's BMC at enrolment.
func SaveTLSFingerprint(db *sql.DB, node string, pin PinnedCert) error {
	_, err := db.Exec(`UPDATE power_targets SET tls_fingerprint = $2, tls_pinned_at = NOW() WHERE node = $1`, node, pin.Fingerprint)
	return err
}

// ClearTLSFingerprint removes the stored fingerprint so that the BMC can be
// enrolled again (after a firmware update gave it a new certificate).
func ClearTLSFingerprint(db *sql.DB, node string) error {
	_, err := db.Exec(`UPDATE power_targets SET tls_fingerprint = '', tls_pinned_at = NULL WHERE node = $1`, node)
	return err
}

// GetPowerStateFast asks only for the power state: over the pinned Redfish
// channel when a certificate is enrolled, with one ipmitool call otherwise
// (GetPowerState reads every sensor first).
func GetPowerStateFast(ctx context.Context, creds Credentials, pin PinnedCert) (PowerState, error) {
	if pin.Fingerprint != "" {
		return GetPowerState(ctx, creds, pin)
	}
	timeout := 10
	if dl, ok := ctx.Deadline(); ok {
		if left := int(time.Until(dl).Seconds()); left > 0 && left < timeout {
			timeout = left
		}
	}
	out, err := ipmiOutput(timeout, creds.Host, creds.Username, creds.Password, []string{"chassis", "power", "status"})
	if err != nil {
		return PowerUnknown, fmt.Errorf("%v: %s", err, firstLine(out))
	}
	switch low := strings.ToLower(out); {
	case strings.Contains(low, "is on"):
		return PowerOn, nil
	case strings.Contains(low, "is off"):
		return PowerOff, nil
	}
	return PowerUnknown, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
