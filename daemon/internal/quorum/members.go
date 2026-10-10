package quorum

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// More than two nodes, and voters (Design 0001 phase 3, ADR-0009).
//
// A cluster member is either a DPlaneOS node (paired, NodeKey set: it gets
// configuration pushed and can own storage) or a voter: a machine that runs
// only corosync (a Raspberry Pi, a mini PC, a VM) and adds a full vote. A
// voter has no DPlaneOS daemon, so it pulls its configuration from any
// DPlaneOS member with a token it received when it joined.

// Members returns the DPlaneOS nodes other than self (the ones that receive
// configuration updates).
func (c Config) Members(self string) []Node {
	var out []Node
	for _, n := range c.Nodes {
		if !n.Voter && n.NodeKey != "" && n.NodeKey != self {
			out = append(out, n)
		}
	}
	return out
}

// DPlaneNodes counts the DPlaneOS members.
func (c Config) DPlaneNodes() int {
	k := 0
	for _, n := range c.Nodes {
		if !n.Voter {
			k++
		}
	}
	return k
}

func (c Config) nextID() int {
	id := 0
	for _, n := range c.Nodes {
		if n.ID > id {
			id = n.ID
		}
	}
	return id + 1
}

// qdeviceBundle returns the quorum device CA and this node's cluster
// certificate bundle, for a member that joins a cluster with a third vote.
func qdeviceBundle() (string, string, error) {
	ca, err := os.ReadFile(filepath.Join(xferDir(), "qnetd-cacert.crt"))
	if err != nil {
		return "", "", fmt.Errorf("the third vote's certificate authority is not on this node: %w", err)
	}
	for _, p := range []string{filepath.Join(nodeDB(), "qdevice-net-node.p12"), filepath.Join(xferDir(), "qdevice-net-node.p12")} {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return string(ca), base64.StdEncoding.EncodeToString(b), nil
		}
	}
	return "", "", errors.New("the third vote's cluster certificate is not on this node")
}

// distribute pushes cfg to every DPlaneOS member except self and except
// skip, then saves and applies it here.
func distribute(db *sql.DB, prev *Config, cfg Config, key []byte, self string, push PushFunc, extra func(n Node) Update) error {
	b64 := base64.StdEncoding.EncodeToString(key)
	for _, n := range cfg.Members(self) {
		u := Update{Config: &cfg, AuthkeyB64: b64}
		if extra != nil {
			u = extra(n)
		}
		if err := push(n, u); err != nil {
			return fmt.Errorf("configuring %s: %w", n.Name, err)
		}
	}
	if err := save(db, cfg, key); err != nil {
		return err
	}
	return apply(prev, cfg, key)
}

// AddNode adds a paired DPlaneOS node to the cluster.
func AddNode(db *sql.DB, self string, n Node, push PushFunc) (*Config, error) {
	opMu.Lock()
	defer opMu.Unlock()
	cfg, key, err := Load(db)
	if err != nil || cfg == nil {
		return nil, errors.New("this node is not in a cluster")
	}
	for _, m := range cfg.Nodes {
		if m.NodeKey == n.NodeKey {
			return nil, fmt.Errorf("%s is already in the cluster", n.Name)
		}
	}
	prev := *cfg
	n.ID, n.Voter = cfg.nextID(), false
	cfg.Nodes = append(append([]Node{}, cfg.Nodes...), n)
	if cfg.QDevice != nil {
		q := *cfg.QDevice
		q.Algorithm = DefaultAlgorithm(len(cfg.Nodes))
		cfg.QDevice = &q
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var ca, p12 string
	if cfg.QDevice != nil {
		if ca, p12, err = qdeviceBundle(); err != nil {
			return nil, err
		}
	}
	b64 := base64.StdEncoding.EncodeToString(key)
	err = distribute(db, &prev, *cfg, key, self, push, func(m Node) Update {
		u := Update{Config: cfg, AuthkeyB64: b64}
		if m.NodeKey == n.NodeKey && cfg.QDevice != nil {
			u.QDeviceCA, u.QDeviceP12 = ca, p12
		}
		return u
	})
	if err != nil {
		return cfg, err
	}
	log.Printf("QUORUM: %s (%s) joined cluster %s", n.Name, n.Addr, cfg.ClusterName)
	return cfg, nil
}

// RemoveNode removes a member (DPlaneOS node or voter) by name.
func RemoveNode(db *sql.DB, self, name string, push PushFunc) (*Config, error) {
	opMu.Lock()
	defer opMu.Unlock()
	cfg, key, err := Load(db)
	if err != nil || cfg == nil {
		return nil, errors.New("this node is not in a cluster")
	}
	prev := *cfg
	var gone *Node
	var keep []Node
	for _, n := range cfg.Nodes {
		if n.Name == name {
			n := n
			gone = &n
			continue
		}
		keep = append(keep, n)
	}
	if gone == nil {
		return nil, fmt.Errorf("%s is not a member of the cluster", name)
	}
	if gone.NodeKey == self {
		return nil, errors.New("remove this node from another member, or dissolve the cluster")
	}
	cfg.Nodes = keep
	if cfg.VoterTokens != nil {
		tokens := map[string]string{}
		for k, v := range cfg.VoterTokens {
			if k != name {
				tokens[k] = v
			}
		}
		cfg.VoterTokens = tokens
	}
	if cfg.QDevice != nil {
		q := *cfg.QDevice
		q.Algorithm = DefaultAlgorithm(len(cfg.Nodes))
		cfg.QDevice = &q
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w (dissolve the cluster instead)", err)
	}
	if !gone.Voter && gone.NodeKey != "" {
		if err := push(*gone, Update{Remove: true}); err != nil {
			log.Printf("QUORUM: %s did not confirm leaving: %v (it stops corosync when it can)", gone.Name, err)
		}
	}
	if err := distribute(db, &prev, *cfg, key, self, push, nil); err != nil {
		return cfg, err
	}
	log.Printf("QUORUM: %s left cluster %s", name, cfg.ClusterName)
	return cfg, nil
}

// ── Voters ────────────────────────────────────────────────────────────────────

// VoterJoin is what a voter receives when it joins, and when it pulls.
type VoterJoin struct {
	ClusterName string   `json:"cluster_name"`
	Conf        string   `json:"corosync_conf"`
	AuthkeyB64  string   `json:"authkey"`
	Token       string   `json:"token,omitempty"` // only when joining
	Nodes       []string `json:"nodes"`           // DPlaneOS member addresses to pull from
}

// Text renders the answer for the setup script and the voter's sync timer
// (plain text: a Raspberry Pi needs no JSON tools).
func (j VoterJoin) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "cluster: %s\n", j.ClusterName)
	if j.Token != "" {
		fmt.Fprintf(&b, "token: %s\n", j.Token)
	}
	fmt.Fprintf(&b, "nodes: %s\n", strings.Join(j.Nodes, " "))
	fmt.Fprintf(&b, "authkey: %s\n", j.AuthkeyB64)
	b.WriteString("---\n")
	b.WriteString(j.Conf)
	return b.String()
}

func voterJoin(cfg Config, key []byte) VoterJoin {
	j := VoterJoin{ClusterName: cfg.ClusterName, Conf: Render(cfg), AuthkeyB64: base64.StdEncoding.EncodeToString(key)}
	for _, n := range cfg.Nodes {
		if !n.Voter {
			j.Nodes = append(j.Nodes, n.Addr)
		}
	}
	return j
}

// EnrollVoter adds a voter with a one-time code (the same codes as the
// third vote) and returns its configuration and pull token.
func EnrollVoter(db *sql.DB, code, name, addr, self string, push PushFunc) (*VoterJoin, error) {
	opMu.Lock()
	defer opMu.Unlock()
	var ok bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM quorum_enrollments
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > NOW())`, hashToken(code)).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalidCode
	}
	cfg, key, err := Load(db)
	if err != nil || cfg == nil {
		return nil, errors.New("this node is not in a cluster")
	}
	name = strings.ToLower(strings.TrimSpace(name))
	for _, n := range cfg.Nodes {
		if n.Name == name {
			return nil, fmt.Errorf("a member named %s exists; give the voter another hostname", name)
		}
		if n.Addr == addr {
			return nil, fmt.Errorf("%s is already a member (%s)", addr, n.Name)
		}
	}
	prev := *cfg
	token := "dpv_" + randomHex(24)
	cfg.Nodes = append(append([]Node{}, cfg.Nodes...), Node{ID: cfg.nextID(), Name: name, Addr: addr, Voter: true})
	tokens := map[string]string{}
	for k, v := range cfg.VoterTokens {
		tokens[k] = v
	}
	tokens[name] = hashToken(token)
	cfg.VoterTokens = tokens
	if cfg.QDevice != nil {
		q := *cfg.QDevice
		q.Algorithm = DefaultAlgorithm(len(cfg.Nodes))
		cfg.QDevice = &q
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := distribute(db, &prev, *cfg, key, self, push, nil); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`UPDATE quorum_enrollments SET used_at = NOW() WHERE token_hash = $1`, hashToken(code)); err != nil {
		return nil, err
	}
	log.Printf("QUORUM: voter %s (%s) joined cluster %s", name, addr, cfg.ClusterName)
	j := voterJoin(*cfg, key)
	j.Token = token
	return &j, nil
}

// VoterConfig returns the current configuration to a voter that presents
// its token.
func VoterConfig(db *sql.DB, name, token string) (*VoterJoin, error) {
	cfg, key, err := Load(db)
	if err != nil || cfg == nil {
		return nil, errors.New("this node is not in a cluster")
	}
	want, ok := cfg.VoterTokens[name]
	if !ok || token == "" || hashToken(token) != want {
		return nil, ErrInvalidCode
	}
	j := voterJoin(*cfg, key)
	return &j, nil
}
