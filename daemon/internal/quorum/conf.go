// Package quorum manages cluster membership and quorum with Corosync
// votequorum and an optional third vote (QDevice) (Design 0001 phase 3a,
// ADR-0004, ADR-0009).
//
// The daemon owns the configuration: it renders corosync.conf from the
// cluster settings in its database, distributes it to the other nodes over
// the paired-node channel (configstore), and runs corosync, corosync-qdevice
// and (when this node serves as a third vote for other clusters)
// corosync-qnetd as systemd units provided by the NixOS module.
package quorum

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Node is one cluster member.
type Node struct {
	ID      int    `json:"nodeid"`   // corosync node id, 1..n
	Name    string `json:"name"`     // display name (hostname)
	Addr    string `json:"addr"`     // ring0 address (IP)
	NodeKey string `json:"node_key"` // configstore node id, used to reach the node
}

// QDevice is the third vote: a corosync-qnetd server.
type QDevice struct {
	Host      string `json:"host"`
	Port      int    `json:"port,omitempty"` // 0 = 5403
	Algorithm string `json:"algorithm"`      // ffsplit (even node counts) or lms
}

// Config is the cluster definition rendered into corosync.conf.
type Config struct {
	ClusterName string   `json:"cluster_name"`
	Nodes       []Node   `json:"nodes"`
	QDevice     *QDevice `json:"qdevice,omitempty"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
var hostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

// Validate checks the configuration before it is written.
func (c Config) Validate() error {
	if !nameRe.MatchString(c.ClusterName) {
		return fmt.Errorf("invalid cluster name %q", c.ClusterName)
	}
	if len(c.Nodes) < 2 {
		return errors.New("a cluster needs at least two nodes")
	}
	ids, addrs := map[int]bool{}, map[string]bool{}
	for _, n := range c.Nodes {
		if n.ID < 1 || ids[n.ID] {
			return fmt.Errorf("invalid or duplicate node id %d", n.ID)
		}
		ids[n.ID] = true
		if net.ParseIP(n.Addr) == nil {
			return fmt.Errorf("node %q: %q is not an IP address", n.Name, n.Addr)
		}
		if addrs[n.Addr] {
			return fmt.Errorf("address %s is used by two nodes", n.Addr)
		}
		addrs[n.Addr] = true
		if !nameRe.MatchString(n.Name) {
			return fmt.Errorf("invalid node name %q", n.Name)
		}
	}
	if q := c.QDevice; q != nil {
		if !hostRe.MatchString(q.Host) {
			return fmt.Errorf("invalid third-vote host %q", q.Host)
		}
		if q.Port < 0 || q.Port > 65535 {
			return fmt.Errorf("invalid third-vote port %d", q.Port)
		}
		if q.Algorithm != "ffsplit" && q.Algorithm != "lms" {
			return fmt.Errorf("invalid algorithm %q", q.Algorithm)
		}
	}
	return nil
}

// DefaultAlgorithm follows Proxmox: ffsplit (one extra vote) for an even
// number of nodes; for odd counts a third vote is discouraged, lms is the
// only option that helps.
func DefaultAlgorithm(nodes int) string {
	if nodes%2 == 0 {
		return "ffsplit"
	}
	return "lms"
}

// ExpectedVotes is the total number of votes with every member present.
func (c Config) ExpectedVotes() int {
	v := len(c.Nodes)
	if c.QDevice != nil {
		v += c.qdeviceVotes()
	}
	return v
}

func (c Config) qdeviceVotes() int {
	if c.QDevice != nil && c.QDevice.Algorithm == "lms" {
		return len(c.Nodes) - 1
	}
	return 1
}

// Render produces corosync.conf.
//
// Two nodes without a third vote use two_node (which implies wait_for_all):
// membership works, but both halves of a split stay quorate, so automatic
// failover is not allowed in that configuration (ADR-0009). two_node cannot
// be combined with a quorum device.
func Render(c Config) string {
	var b strings.Builder
	b.WriteString("# Written by dplaned (System > High Availability). Changes made here are overwritten.\n")
	b.WriteString("totem {\n")
	b.WriteString("  version: 2\n")
	fmt.Fprintf(&b, "  cluster_name: %s\n", c.ClusterName)
	b.WriteString("  transport: knet\n")
	b.WriteString("  crypto_cipher: aes256\n")
	b.WriteString("  crypto_hash: sha256\n")
	b.WriteString("}\n\n")

	b.WriteString("nodelist {\n")
	for _, n := range c.Nodes {
		b.WriteString("  node {\n")
		fmt.Fprintf(&b, "    nodeid: %d\n", n.ID)
		fmt.Fprintf(&b, "    name: %s\n", n.Name)
		fmt.Fprintf(&b, "    ring0_addr: %s\n", n.Addr)
		b.WriteString("  }\n")
	}
	b.WriteString("}\n\n")

	b.WriteString("quorum {\n")
	b.WriteString("  provider: corosync_votequorum\n")
	if q := c.QDevice; q != nil {
		b.WriteString("  device {\n")
		b.WriteString("    model: net\n")
		fmt.Fprintf(&b, "    votes: %d\n", c.qdeviceVotes())
		b.WriteString("    net {\n")
		b.WriteString("      tls: on\n")
		fmt.Fprintf(&b, "      host: %s\n", q.Host)
		if q.Port != 0 && q.Port != 5403 {
			fmt.Fprintf(&b, "      port: %d\n", q.Port)
		}
		fmt.Fprintf(&b, "      algorithm: %s\n", q.Algorithm)
		b.WriteString("    }\n")
		b.WriteString("  }\n")
	} else if len(c.Nodes) == 2 {
		b.WriteString("  two_node: 1\n")
	}
	b.WriteString("}\n\n")

	b.WriteString("logging {\n")
	b.WriteString("  to_syslog: yes\n")
	b.WriteString("  to_stderr: no\n")
	b.WriteString("}\n")
	return b.String()
}
