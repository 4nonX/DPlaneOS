package ha

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/bmc"
	"dplaned/internal/secrets"
)

// Power control of other nodes (power fencing): before this node takes a
// storage group over from a node that stopped answering, it can cut that
// node's power, through its BMC (IPMI or Redfish) or a switched PDU.
//
// A target belongs to one node. Powering off "the configured machine"
// whoever had failed (as it was) is right for a pair at best; with three
// nodes it can be the healthy one.

// PowerTarget is how this node powers one other node off.
type PowerTarget struct {
	Node    string `json:"node"` // node key of the machine this powers off
	Kind    string `json:"kind"` // bmc | pdu
	Enabled bool   `json:"enabled"`

	// BMC
	Host        string     `json:"host"`
	Username    string     `json:"username"`
	HasPassword bool       `json:"has_password"`
	Fingerprint string     `json:"tls_fingerprint"`
	PinnedAt    *time.Time `json:"tls_pinned_at,omitempty"`

	// PDU: OffURL cuts the outlet's power. StatusURL, when given, is
	// fetched by the test (it must answer without switching anything).
	OffURL         string `json:"off_url"`
	StatusURL      string `json:"status_url"`
	Method         string `json:"http_method"`
	ExpectedStatus int    `json:"expected_status"` // 0: any 2xx
	TimeoutSecs    int    `json:"timeout_secs"`
}

var (
	nodeKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	bmcHostRe = regexp.MustCompile(`^[A-Za-z0-9\[][A-Za-z0-9.:\]-]{0,252}$`)
	bmcUserRe = regexp.MustCompile(`^[A-Za-z0-9_.@\\-][A-Za-z0-9_.@\\ -]{0,63}$`)
)

// Validate checks a target before it is stored. Host and user name become
// arguments of ipmitool: neither may look like an option.
func (t PowerTarget) Validate() error {
	// Node "" is this system's own BMC (the hardware page): never a
	// fencing target.
	if t.Node != bmc.Self && !nodeKeyRe.MatchString(t.Node) {
		return errors.New("the node is not valid")
	}
	if t.Node == bmc.Self && (t.Kind != "bmc" || t.Enabled) {
		return errors.New("this system's own BMC is not a fencing target")
	}
	switch t.Kind {
	case "bmc":
		if !bmcHostRe.MatchString(t.Host) {
			return errors.New("the BMC address must be a host name or an IP address")
		}
		if t.Username != "" && (!bmcUserRe.MatchString(t.Username) || strings.HasPrefix(t.Username, "-")) {
			return errors.New("the BMC user name has characters that are not allowed")
		}
	case "pdu":
		for name, raw := range map[string]string{"switch-off address": t.OffURL, "status address": t.StatusURL} {
			if raw == "" {
				if name == "status address" {
					continue
				}
				return errors.New("the address that switches the outlet off is required")
			}
			u, err := url.Parse(raw)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return fmt.Errorf("the %s must be an http:// or https:// URL (user name and password go in their own fields)", name)
			}
		}
		if m := strings.ToUpper(t.Method); m != "" && m != "GET" && m != "POST" {
			return errors.New("the method is GET or POST")
		}
	default:
		return errors.New("the kind is bmc or pdu")
	}
	return nil
}

const targetColumns = `node, kind, enabled, host, username, password <> '', tls_fingerprint, tls_pinned_at,
	off_url, status_url, http_method, expected_status, timeout_secs`

func scanTarget(row interface{ Scan(...any) error }) (PowerTarget, error) {
	var t PowerTarget
	var pinned sql.NullTime
	err := row.Scan(&t.Node, &t.Kind, &t.Enabled, &t.Host, &t.Username, &t.HasPassword, &t.Fingerprint, &pinned,
		&t.OffURL, &t.StatusURL, &t.Method, &t.ExpectedStatus, &t.TimeoutSecs)
	if pinned.Valid {
		t.PinnedAt = &pinned.Time
	}
	return t, err
}

// ListPowerTargets returns the targets of other nodes (not this system's
// own BMC, which is node "").
func ListPowerTargets(db *sql.DB) ([]PowerTarget, error) {
	rows, err := db.Query(`SELECT ` + targetColumns + ` FROM power_targets WHERE node <> '' ORDER BY node`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PowerTarget
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetPowerTarget returns the target of a node, and whether there is one.
func GetPowerTarget(db *sql.DB, node string) (PowerTarget, bool, error) {
	t, err := scanTarget(db.QueryRow(`SELECT `+targetColumns+` FROM power_targets WHERE node = $1`, node))
	if errors.Is(err, sql.ErrNoRows) {
		return PowerTarget{}, false, nil
	}
	return t, err == nil, err
}

// SavePowerTarget stores a target. password nil keeps the stored one.
// A changed BMC address drops the pinned certificate: it belonged to the
// machine at the old address.
func SavePowerTarget(db *sql.DB, t PowerTarget, password *string) error {
	if err := t.Validate(); err != nil {
		return err
	}
	t.Method = strings.ToUpper(t.Method)
	if t.Method == "" {
		t.Method = "GET"
	}
	if t.TimeoutSecs <= 0 || t.TimeoutSecs > 120 {
		t.TimeoutSecs = 10
	}
	sealed := sql.NullString{}
	if password != nil {
		sealed.Valid = true
		if *password != "" {
			s, err := secrets.Seal(*password)
			if err != nil {
				return fmt.Errorf("the password could not be stored: %w", err)
			}
			sealed.String = s
		}
	}
	_, err := db.Exec(`
		INSERT INTO power_targets (node, kind, enabled, host, username, password, off_url, status_url,
			http_method, expected_status, timeout_secs, updated_at)
		VALUES ($1, $2, $3, $4, $5, COALESCE($6, ''), $7, $8, $9, $10, $11, NOW())
		ON CONFLICT (node) DO UPDATE SET
			kind = EXCLUDED.kind, enabled = EXCLUDED.enabled, username = EXCLUDED.username,
			password = COALESCE($6, power_targets.password),
			off_url = EXCLUDED.off_url, status_url = EXCLUDED.status_url, http_method = EXCLUDED.http_method,
			expected_status = EXCLUDED.expected_status, timeout_secs = EXCLUDED.timeout_secs,
			tls_fingerprint = CASE WHEN power_targets.host = EXCLUDED.host THEN power_targets.tls_fingerprint ELSE '' END,
			tls_pinned_at   = CASE WHEN power_targets.host = EXCLUDED.host THEN power_targets.tls_pinned_at END,
			host = EXCLUDED.host, updated_at = NOW()`,
		t.Node, t.Kind, t.Enabled, t.Host, t.Username, sealed, t.OffURL, t.StatusURL,
		t.Method, t.ExpectedStatus, t.TimeoutSecs)
	return err
}

// DeletePowerTarget removes a node's target.
func DeletePowerTarget(db *sql.DB, node string) error {
	_, err := db.Exec(`DELETE FROM power_targets WHERE node = $1 AND node <> ''`, node)
	return err
}

func targetPassword(db *sql.DB, node string) (string, error) {
	var sealed string
	if err := db.QueryRow(`SELECT password FROM power_targets WHERE node = $1`, node).Scan(&sealed); err != nil {
		return "", err
	}
	if sealed == "" {
		return "", nil
	}
	return secrets.Open(sealed)
}

// PowerTest is the result of trying a target without switching anything.
type PowerTest struct {
	OK     bool   `json:"ok"`
	State  string `json:"power_state,omitempty"` // on, off (BMC)
	Detail string `json:"detail"`
	// Confirms says what the test showed: "sign-in and power state" for a
	// BMC; for a PDU either "status address answers" or only "reachable".
	Confirms string `json:"confirms"`
}

// httpDo is replaced in tests.
var httpDo = func(req *http.Request, timeout time.Duration) (*http.Response, error) {
	return (&http.Client{Timeout: timeout}).Do(req)
}

// bmcPowerState and bmcPowerOff are replaced in tests.
var (
	bmcPowerState = func(ctx context.Context, c bmc.Credentials, pin bmc.PinnedCert) (bmc.PowerState, error) {
		return bmc.GetPowerStateFast(ctx, c, pin)
	}
	bmcPowerOff = func(ctx context.Context, c bmc.Credentials, pin bmc.PinnedCert) error {
		return bmc.SendPowerAction(ctx, c, pin, bmc.PowerActionOff)
	}
)

// TestPowerTarget tries a node's target: it signs in to the BMC and reads
// the power state, or asks the PDU for its status. Nothing is switched.
func TestPowerTarget(ctx context.Context, db *sql.DB, node string) PowerTest {
	t, ok, err := GetPowerTarget(db, node)
	if err != nil {
		return PowerTest{Detail: err.Error()}
	}
	if !ok {
		return PowerTest{Detail: "nothing is configured for this node"}
	}
	pw, err := targetPassword(db, node)
	if err != nil {
		return PowerTest{Detail: "the stored password cannot be read: " + err.Error()}
	}
	if t.Kind == "bmc" {
		state, err := bmcPowerState(ctx, bmc.Credentials{Host: t.Host, Username: t.Username, Password: pw}, bmc.PinnedCert{Fingerprint: t.Fingerprint})
		if err != nil {
			return PowerTest{Detail: fmt.Sprintf("The BMC at %s did not answer the question for the power state: %v", t.Host, err)}
		}
		if state != bmc.PowerOn && state != bmc.PowerOff {
			return PowerTest{Detail: fmt.Sprintf("The BMC at %s answered, but did not say whether the machine is on or off.", t.Host)}
		}
		return PowerTest{OK: true, State: string(state), Confirms: "sign-in and power state",
			Detail: fmt.Sprintf("Signed in to the BMC at %s; the machine is %s. Nothing was switched.", t.Host, state)}
	}
	// PDU
	target, confirms := t.StatusURL, "status address answers"
	if target == "" {
		// Without a status address the only request this system knows is the
		// one that cuts the power. All that can be shown is that the PDU is
		// there.
		u, _ := url.Parse(t.OffURL)
		target, confirms = u.Scheme+"://"+u.Host+"/", "reachable"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return PowerTest{Detail: err.Error()}
	}
	if t.Username != "" && t.StatusURL != "" {
		req.SetBasicAuth(t.Username, pw)
	}
	resp, err := httpDo(req, time.Duration(t.TimeoutSecs)*time.Second)
	if err != nil {
		return PowerTest{Detail: fmt.Sprintf("The PDU did not answer at %s: %v", target, err)}
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) //nolint:errcheck
	if t.StatusURL == "" {
		return PowerTest{OK: true, Confirms: confirms,
			Detail: fmt.Sprintf("The PDU answers at %s (HTTP %d). Whether the user name, the password and the outlet are right is not known: give a status address to have that checked. Nothing was switched.", target, resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return PowerTest{Detail: fmt.Sprintf("The PDU answered the status request with HTTP %d (wrong user name or password, or wrong address).", resp.StatusCode)}
	}
	return PowerTest{OK: true, Confirms: confirms,
		Detail: fmt.Sprintf("The PDU answered the status request (HTTP %d) with the stored user name and password. Nothing was switched.", resp.StatusCode)}
}

// PowerOff cuts the power of a node through its target and waits until the
// BMC says the machine is off (a PDU cannot be asked: its 2xx answer is
// taken as done). It is the fencing step of an automatic takeover.
func PowerOff(db *sql.DB, node string) error {
	t, ok, err := GetPowerTarget(db, node)
	if err != nil {
		return err
	}
	if !ok || !t.Enabled {
		return ErrNoPowerTarget
	}
	start := time.Now()
	fail := func(format string, a ...any) error {
		msg := fmt.Sprintf(format, a...)
		audit.LogAction("ha_fence", "system", msg, false, time.Since(start))
		return errors.New(msg)
	}
	cfg, _ := GetFencingConfig(db)
	maxMs := cfg.JitterMaxMs
	if maxMs == 0 {
		maxMs = 3000
	}
	// A random wait: two nodes that decide to fence each other at the same
	// moment do not both fire.
	delay := stonithJitter(maxMs)
	log.Printf("FENCING: powering off node %s through its %s in %v", node, t.Kind, delay)
	sleep(delay)

	pw, err := targetPassword(db, node)
	if err != nil {
		return fail("power fencing of %s: the stored password cannot be read: %v", node, err)
	}

	if t.Kind == "pdu" {
		req, err := http.NewRequest(t.Method, t.OffURL, nil)
		if err != nil {
			return fail("power fencing of %s: %v", node, err)
		}
		if t.Username != "" {
			req.SetBasicAuth(t.Username, pw)
		}
		resp, err := httpDo(req, time.Duration(t.TimeoutSecs)*time.Second)
		if err != nil {
			return fail("power fencing of %s: the PDU did not answer: %v", node, err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) //nolint:errcheck
		okStatus := resp.StatusCode >= 200 && resp.StatusCode <= 299
		if t.ExpectedStatus != 0 {
			okStatus = resp.StatusCode == t.ExpectedStatus
		}
		if !okStatus {
			return fail("power fencing of %s: the PDU answered HTTP %d", node, resp.StatusCode)
		}
		audit.LogAction("ha_fence", "system", fmt.Sprintf("Outlet of node %s switched off (PDU answered HTTP %d)", node, resp.StatusCode), true, time.Since(start))
		return nil
	}

	creds := bmc.Credentials{Host: t.Host, Username: t.Username, Password: pw}
	pin := bmc.PinnedCert{Fingerprint: t.Fingerprint}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = bmcPowerOff(ctx, creds, pin)
	cancel()
	if err != nil {
		return fail("power fencing of %s: the BMC at %s refused or did not answer: %v", node, t.Host, err)
	}
	// Off means off: wait until the BMC says so.
	deadline := time.Now().Add(powerOffConfirm)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		state, err := bmcPowerState(ctx, creds, pin)
		cancel()
		if err == nil && state == bmc.PowerOff {
			break
		}
		if time.Now().After(deadline) {
			return fail("power fencing of %s: the BMC at %s did not report the machine as off within %v", node, t.Host, powerOffConfirm)
		}
		sleep(powerOffPoll)
	}
	audit.LogAction("ha_fence", "system", fmt.Sprintf("Node %s powered off through its BMC at %s (confirmed off)", node, t.Host), true, time.Since(start))
	return nil
}

// ErrNoPowerTarget: no enabled target for the node. The caller falls back
// on the watchdog, or does not take over.
var ErrNoPowerTarget = errors.New("no power fencing is configured for this node")

var (
	powerOffConfirm = 60 * time.Second
	powerOffPoll    = 5 * time.Second
	sleep           = time.Sleep
)
