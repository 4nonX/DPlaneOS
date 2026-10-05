package quorum

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/secrets"
)

// Files and units. /etc/corosync/{corosync.conf,authkey,qdevice,qnetd} are
// symlinks into StateDir (NixOS module), so everything survives reboots on
// an ephemeral root and the certificate tools work with their default paths.
var StateDir = "/var/lib/dplaneos/corosync"

const (
	unitCorosync = "dplaneos-corosync"
	unitQdevice  = "dplaneos-qdevice"
	unitQnetd    = "dplaneos-qnetd"
)

func confFile() string    { return filepath.Join(StateDir, "corosync.conf") }
func authkeyFile() string { return filepath.Join(StateDir, "authkey") }
func qdeviceFlag() string { return filepath.Join(StateDir, "qdevice-enabled") }
func qnetdFlag() string   { return filepath.Join(StateDir, "qnetd-enabled") }
func nodeDB() string      { return filepath.Join(StateDir, "qdevice", "net", "nssdb") }
func qnetdDB() string     { return filepath.Join(StateDir, "qnetd", "nssdb") }
func xferDir() string     { return filepath.Join(StateDir, "xfer") }

// ── Persistence ───────────────────────────────────────────────────────────────

// Load returns the cluster this node belongs to (nil if none).
func Load(db *sql.DB) (*Config, []byte, error) {
	var raw []byte
	var sealed string
	err := db.QueryRow(`SELECT config, authkey FROM cluster_quorum WHERE id = 1`).Scan(&raw, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, nil, err
	}
	b64, err := secrets.Open(sealed)
	if err != nil {
		return nil, nil, fmt.Errorf("opening cluster key: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, nil, err
	}
	return &cfg, key, nil
}

func save(db *sql.DB, cfg Config, authkey []byte) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	sealed, err := secrets.Seal(base64.StdEncoding.EncodeToString(authkey))
	if err != nil {
		return fmt.Errorf("sealing cluster key: %w", err)
	}
	_, err = db.Exec(`INSERT INTO cluster_quorum (id, config, authkey) VALUES (1, $1, $2)
		ON CONFLICT (id) DO UPDATE SET config = EXCLUDED.config, authkey = EXCLUDED.authkey, updated_at = NOW()`, raw, sealed)
	return err
}

// ── Files and units ───────────────────────────────────────────────────────────

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func systemctl(action, unit string) error {
	out, err := cmdutil.RunFast("systemctl_cluster", action, unit)
	if err != nil {
		return fmt.Errorf("systemctl %s %s: %v: %s", action, unit, err, bytes.TrimSpace(out))
	}
	return nil
}

func unitActive(unit string) bool {
	return systemctl("is-active", unit) == nil
}

// apply writes the configuration and (re)starts the units. Corosync reloads
// a changed configuration at runtime; a change of two_node (adding or
// removing the third vote on a two-node cluster) needs a restart.
func apply(prev *Config, cfg Config, authkey []byte) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	oldKey, _ := os.ReadFile(authkeyFile())
	if err := writeAtomic(authkeyFile(), authkey, 0o400); err != nil {
		return fmt.Errorf("writing authkey: %w", err)
	}
	if err := writeAtomic(confFile(), []byte(Render(cfg)), 0o644); err != nil {
		return fmt.Errorf("writing corosync.conf: %w", err)
	}
	twoNode := func(c *Config) bool { return c != nil && c.QDevice == nil && len(c.Nodes) == 2 }
	restart := prev == nil || twoNode(prev) != twoNode(&cfg) || !bytes.Equal(oldKey, authkey) || !unitActive(unitCorosync)
	if restart {
		if err := systemctl("restart", unitCorosync); err != nil {
			return err
		}
	} else if out, err := cmdutil.RunFast("corosync_cfgtool_reload", "-R"); err != nil {
		return fmt.Errorf("reloading corosync: %v: %s", err, bytes.TrimSpace(out))
	}
	if cfg.QDevice != nil {
		if err := writeAtomic(qdeviceFlag(), []byte("1\n"), 0o644); err != nil {
			return err
		}
		return systemctl("restart", unitQdevice)
	}
	_ = os.Remove(qdeviceFlag())
	if unitActive(unitQdevice) {
		return systemctl("stop", unitQdevice)
	}
	return nil
}

func removeAll() error {
	var errs []error
	for _, u := range []string{unitQdevice, unitCorosync} {
		if unitActive(u) {
			if err := systemctl("stop", u); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for _, f := range []string{qdeviceFlag(), confFile(), authkeyFile()} {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	_ = os.RemoveAll(filepath.Join(StateDir, "qdevice"))
	return errors.Join(errs...)
}

// ── Status ────────────────────────────────────────────────────────────────────

// Info is what the HA engine needs (ADR-0009): quorum, and whether automatic
// failover is possible at all (three votes or more).
type Info struct {
	Configured     bool   `json:"configured"`
	Quorate        bool   `json:"quorate"`
	ExpectedVotes  int    `json:"expected_votes"`
	AutoFailover   bool   `json:"auto_failover"`
	AutoFailoverNo string `json:"auto_failover_reason,omitempty"`
}

// Monitor polls corosync-quorumtool in the background.
type Monitor struct {
	db   *sql.DB
	mu   sync.RWMutex
	cfg  *Config
	st   Status
	err  string
	when time.Time
}

// NewMonitor creates the monitor; call Start to poll.
func NewMonitor(db *sql.DB) *Monitor { return &Monitor{db: db} }

// Start polls every interval.
func (m *Monitor) Start(interval time.Duration) {
	go func() {
		for {
			m.Refresh()
			time.Sleep(interval)
		}
	}()
}

// Refresh reads the configuration and the live quorum state now.
func (m *Monitor) Refresh() {
	cfg, _, err := Load(m.db)
	var st Status
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	} else if cfg != nil {
		out, runErr := cmdutil.RunFast("corosync_quorumtool", "-s")
		st = ParseQuorumtool(string(out))
		if !st.Running && runErr != nil {
			errMsg = fmt.Sprintf("corosync is not running: %v", runErr)
		}
	}
	m.mu.Lock()
	m.cfg, m.st, m.err, m.when = cfg, st, errMsg, time.Now()
	m.mu.Unlock()
}

// Snapshot returns the last configuration and state.
func (m *Monitor) Snapshot() (*Config, Status, string, time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg, m.st, m.err, m.when
}

// Info summarises the state for the HA engine.
func (m *Monitor) Info() Info {
	cfg, st, _, _ := m.Snapshot()
	if cfg == nil {
		return Info{}
	}
	in := Info{Configured: true, Quorate: st.Running && st.Quorate, ExpectedVotes: cfg.ExpectedVotes()}
	if st.ExpectedVotes > in.ExpectedVotes {
		in.ExpectedVotes = st.ExpectedVotes
	}
	switch {
	case in.ExpectedVotes < 3:
		in.AutoFailoverNo = "no third vote: with two votes a network split leaves both nodes quorate, so failover is a manual takeover"
	case !in.Quorate:
		in.AutoFailoverNo = "this node is not part of the quorate partition"
	default:
		in.AutoFailover = true
	}
	return in
}

// ── Certificates (QDevice TLS) ────────────────────────────────────────────────
//
// The flow is the one corosync-qdevice-net-certutil -Q performs over SSH,
// split into steps that run over HTTPS with a one-time code instead:
//   witness: init CA                  → CA certificate to the node
//   node:    init NSS DB with CA, CSR → CSR to the witness
//   witness: sign CSR                 → certificate to the node
//   node:    import, export PKCS#12   → PKCS#12 + CA to the other nodes

func certutil(name string, args ...string) error {
	out, err := cmdutil.Run(60*time.Second, name, args...)
	if err != nil {
		return fmt.Errorf("%v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func nodeInitCA(caPEM []byte) error {
	if err := os.RemoveAll(nodeDB()); err != nil {
		return err
	}
	ca := filepath.Join(xferDir(), "qnetd-cacert.crt")
	if err := writeAtomic(ca, caPEM, 0o600); err != nil {
		return err
	}
	if err := certutil("qdevice_certutil", "-i", "-c", ca); err != nil {
		return fmt.Errorf("initialising the certificate store: %w", err)
	}
	return nil
}

func nodeCSR(cluster string) ([]byte, error) {
	if err := certutil("qdevice_certutil", "-r", "-n", cluster); err != nil {
		return nil, fmt.Errorf("creating the certificate request: %w", err)
	}
	return os.ReadFile(filepath.Join(nodeDB(), "qdevice-net-node.crq"))
}

func nodeImportSigned(cluster string, cert []byte) ([]byte, error) {
	f := filepath.Join(nodeDB(), "cluster-"+cluster+".crt")
	if err := writeAtomic(f, cert, 0o600); err != nil {
		return nil, err
	}
	if err := certutil("qdevice_certutil", "-M", "-c", f); err != nil {
		return nil, fmt.Errorf("importing the signed certificate: %w", err)
	}
	return os.ReadFile(filepath.Join(nodeDB(), "qdevice-net-node.p12"))
}

func nodeImportP12(caPEM, p12 []byte) error {
	if err := nodeInitCA(caPEM); err != nil {
		return err
	}
	f := filepath.Join(xferDir(), "qdevice-net-node.p12")
	if err := writeAtomic(f, p12, 0o600); err != nil {
		return err
	}
	if err := certutil("qdevice_certutil", "-m", "-c", f); err != nil {
		return fmt.Errorf("importing the cluster certificate: %w", err)
	}
	return nil
}

// ── Cluster operations ────────────────────────────────────────────────────────

// Update is sent to the other members when the cluster changes.
type Update struct {
	Config     *Config `json:"config,omitempty"`
	AuthkeyB64 string  `json:"authkey,omitempty"`
	QDeviceCA  string  `json:"qdevice_ca,omitempty"`  // PEM
	QDeviceP12 string  `json:"qdevice_p12,omitempty"` // base64
	Remove     bool    `json:"remove,omitempty"`
}

// PushFunc delivers an update to another member (over the paired-node channel).
type PushFunc func(n Node, u Update) error

var opMu sync.Mutex

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Form creates a two-node cluster of this node and peer, configures the peer
// first, then this node.
func Form(db *sql.DB, self, peer Node, push PushFunc) (*Config, error) {
	opMu.Lock()
	defer opMu.Unlock()
	if cur, _, err := Load(db); err != nil {
		return nil, err
	} else if cur != nil {
		return nil, errors.New("this node is already in a cluster; remove it first")
	}
	self.ID, peer.ID = 1, 2
	cfg := Config{ClusterName: "dplane-" + randomHex(3), Nodes: []Node{self, peer}}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	key := make([]byte, 256)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := push(peer, Update{Config: &cfg, AuthkeyB64: base64.StdEncoding.EncodeToString(key)}); err != nil {
		return nil, fmt.Errorf("configuring %s: %w", peer.Name, err)
	}
	if err := save(db, cfg, key); err != nil {
		return nil, err
	}
	if err := apply(nil, cfg, key); err != nil {
		return &cfg, err
	}
	log.Printf("QUORUM: formed cluster %s with %s (%s)", cfg.ClusterName, peer.Name, peer.Addr)
	return &cfg, nil
}

// Receive applies an update from another member.
func Receive(db *sql.DB, u Update) error {
	opMu.Lock()
	defer opMu.Unlock()
	if u.Remove {
		if _, err := db.Exec(`DELETE FROM cluster_quorum`); err != nil {
			return err
		}
		return removeAll()
	}
	if u.Config == nil {
		return errors.New("update without configuration")
	}
	if err := u.Config.Validate(); err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(u.AuthkeyB64)
	if err != nil || len(key) < 128 {
		return errors.New("invalid cluster key")
	}
	if u.QDeviceP12 != "" {
		p12, err := base64.StdEncoding.DecodeString(u.QDeviceP12)
		if err != nil {
			return err
		}
		if err := nodeImportP12([]byte(u.QDeviceCA), p12); err != nil {
			return err
		}
	}
	prev, _, err := Load(db)
	if err != nil {
		return err
	}
	if err := save(db, *u.Config, key); err != nil {
		return err
	}
	return apply(prev, *u.Config, key)
}

// Dissolve removes the cluster on every member (best effort for the others).
func Dissolve(db *sql.DB, self string, push PushFunc) error {
	opMu.Lock()
	defer opMu.Unlock()
	cfg, _, err := Load(db)
	if err != nil {
		return err
	}
	var errs []error
	if cfg != nil {
		for _, n := range cfg.Nodes {
			if n.NodeKey != self {
				if err := push(n, Update{Remove: true}); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w (remove it there too)", n.Name, err))
				}
			}
		}
	}
	if _, err := db.Exec(`DELETE FROM cluster_quorum`); err != nil {
		return err
	}
	errs = append(errs, removeAll())
	return errors.Join(errs...)
}

// RemoveThirdVote drops the quorum device from the cluster.
func RemoveThirdVote(db *sql.DB, self string, push PushFunc) error {
	opMu.Lock()
	defer opMu.Unlock()
	cfg, key, err := Load(db)
	if err != nil || cfg == nil {
		return errors.New("this node is not in a cluster")
	}
	prev := *cfg
	cfg.QDevice = nil
	b64 := base64.StdEncoding.EncodeToString(key)
	for _, n := range cfg.Nodes {
		if n.NodeKey != self {
			if err := push(n, Update{Config: cfg, AuthkeyB64: b64}); err != nil {
				return fmt.Errorf("%s: %w", n.Name, err)
			}
		}
	}
	if err := save(db, *cfg, key); err != nil {
		return err
	}
	return apply(&prev, *cfg, key)
}

// ── Third vote enrollment (node side) ─────────────────────────────────────────

// ErrInvalidCode: the code is unknown, expired or used.
var ErrInvalidCode = errors.New("the code is invalid, expired or already used")

func hashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

// CreateEnrollCode issues a code a witness uses to add itself (30 minutes).
func CreateEnrollCode(db *sql.DB) (string, time.Time, error) {
	if cfg, _, err := Load(db); err != nil {
		return "", time.Time{}, err
	} else if cfg == nil {
		return "", time.Time{}, errors.New("form a cluster first")
	}
	code := "dpq_" + randomHex(20)
	exp := time.Now().Add(30 * time.Minute)
	_, _ = db.Exec(`DELETE FROM quorum_enrollments WHERE expires_at < NOW() - INTERVAL '1 day'`)
	_, err := db.Exec(`INSERT INTO quorum_enrollments (token_hash, expires_at) VALUES ($1, $2)`, hashToken(code), exp)
	return code, exp, err
}

// EnrollCA is step 1: the witness sends its CA certificate, the node answers
// with its cluster name and a certificate request.
func EnrollCA(db *sql.DB, code string, caPEM []byte) (string, []byte, error) {
	opMu.Lock()
	defer opMu.Unlock()
	if !bytes.Contains(caPEM, []byte("BEGIN CERTIFICATE")) {
		return "", nil, errors.New("not a PEM certificate")
	}
	res, err := db.Exec(`UPDATE quorum_enrollments SET ca_pem = $2
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > NOW()`, hashToken(code), string(caPEM))
	if err != nil {
		return "", nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", nil, ErrInvalidCode
	}
	cfg, _, err := Load(db)
	if err != nil || cfg == nil {
		return "", nil, errors.New("this node is not in a cluster")
	}
	if err := nodeInitCA(caPEM); err != nil {
		return "", nil, err
	}
	csr, err := nodeCSR(cfg.ClusterName)
	if err != nil {
		return "", nil, err
	}
	return cfg.ClusterName, csr, nil
}

// EnrollCert is step 2: the witness sends the signed certificate and the
// address the nodes reach it at; the node distributes the certificate to the
// other members and enables the third vote everywhere.
func EnrollCert(db *sql.DB, code string, cert []byte, witnessAddr, self string, push PushFunc) (*Config, error) {
	opMu.Lock()
	defer opMu.Unlock()
	var caPEM string
	err := db.QueryRow(`SELECT ca_pem FROM quorum_enrollments
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > NOW()`, hashToken(code)).Scan(&caPEM)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && caPEM == "") {
		return nil, ErrInvalidCode
	}
	if err != nil {
		return nil, err
	}
	if !hostRe.MatchString(witnessAddr) {
		return nil, fmt.Errorf("invalid witness address %q", witnessAddr)
	}
	cfg, key, err := Load(db)
	if err != nil || cfg == nil {
		return nil, errors.New("this node is not in a cluster")
	}
	p12, err := nodeImportSigned(cfg.ClusterName, cert)
	if err != nil {
		return nil, err
	}
	prev := *cfg
	cfg.QDevice = &QDevice{Host: witnessAddr, Algorithm: DefaultAlgorithm(len(cfg.Nodes))}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	upd := Update{Config: cfg, AuthkeyB64: base64.StdEncoding.EncodeToString(key),
		QDeviceCA: caPEM, QDeviceP12: base64.StdEncoding.EncodeToString(p12)}
	for _, n := range cfg.Nodes {
		if n.NodeKey != self {
			if err := push(n, upd); err != nil {
				return nil, fmt.Errorf("configuring %s: %w", n.Name, err)
			}
		}
	}
	if err := save(db, *cfg, key); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`UPDATE quorum_enrollments SET used_at = NOW() WHERE token_hash = $1`, hashToken(code)); err != nil {
		return nil, err
	}
	if err := apply(&prev, *cfg, key); err != nil {
		return cfg, err
	}
	log.Printf("QUORUM: third vote %s added to cluster %s", witnessAddr, cfg.ClusterName)
	return cfg, nil
}

// ── Serving as third vote (witness side) ──────────────────────────────────────

// WitnessEnable initialises the qnetd certificate authority once and starts
// corosync-qnetd.
func WitnessEnable() error {
	if _, err := os.Stat(filepath.Join(qnetdDB(), "qnetd-cacert.crt")); os.IsNotExist(err) {
		if err := certutil("qnetd_certutil", "-i"); err != nil {
			return fmt.Errorf("initialising the witness certificate authority: %w", err)
		}
	}
	if err := writeAtomic(qnetdFlag(), []byte("1\n"), 0o644); err != nil {
		return err
	}
	return systemctl("restart", unitQnetd)
}

// WitnessCA returns the qnetd CA certificate.
func WitnessCA() ([]byte, error) {
	return os.ReadFile(filepath.Join(qnetdDB(), "qnetd-cacert.crt"))
}

// WitnessSign signs a cluster's certificate request.
func WitnessSign(db *sql.DB, cluster, nodeURL string, csr []byte) ([]byte, error) {
	if !nameRe.MatchString(cluster) {
		return nil, fmt.Errorf("invalid cluster name %q", cluster)
	}
	f := filepath.Join(xferDir(), cluster+".crq")
	if err := writeAtomic(f, csr, 0o600); err != nil {
		return nil, err
	}
	if err := certutil("qnetd_certutil", "-s", "-c", f, "-n", cluster); err != nil {
		return nil, fmt.Errorf("signing the certificate request: %w", err)
	}
	cert, err := os.ReadFile(filepath.Join(qnetdDB(), "cluster-"+cluster+".crt"))
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`INSERT INTO quorum_witness_clusters (cluster_name, node_url) VALUES ($1, $2)
		ON CONFLICT (cluster_name) DO UPDATE SET node_url = EXCLUDED.node_url, enrolled_at = NOW()`, cluster, nodeURL)
	return cert, err
}

// WitnessClusters lists the clusters this node serves.
func WitnessClusters(db *sql.DB) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT cluster_name, node_url, enrolled_at FROM quorum_witness_clusters ORDER BY enrolled_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var name, url string
		var at time.Time
		if err := rows.Scan(&name, &url, &at); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"cluster_name": name, "node_url": url, "enrolled_at": at})
	}
	return out, rows.Err()
}

// WitnessActive reports whether this node serves as third vote.
func WitnessActive() bool {
	_, err := os.Stat(qnetdFlag())
	return err == nil && unitActive(unitQnetd)
}
