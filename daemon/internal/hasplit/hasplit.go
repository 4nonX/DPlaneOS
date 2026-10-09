// Package hasplit moves an HA pair off the shared Patroni database (Design
// 0001 phase 3e).
//
// Patroni keeps a full copy of the database on both nodes. The migration
// lets each node keep its copy as its own node-local database:
//
//  1. The node holding the pools (the coordinator) writes the plan into the
//     shared database; the other node adds its row (its new configuration
//     identity and address) and says it is ready.
//  2. The replica switches first: once its copy has replayed everything, its
//     NixOS configuration drops Patroni, etcd, HAProxy and keepalived and
//     starts PostgreSQL on the same data directory. Then the leader does.
//  3. A boot-time step (dplaneos-patroni-split) gives each copy its own
//     configuration identity and pairs it with the other node from the plan.
//  4. The coordinator forms the Corosync cluster with the other node and
//     turns the pools into a storage group (with the old floating address).
//
// This file holds the pure parts; the daemon wiring is in handlers/ha_split.go.
package hasplit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Member is a Patroni cluster member.
type Member struct {
	Name  string `json:"name"`
	Host  string `json:"host"`
	Role  string `json:"role"`  // leader, replica, sync_standby, standby_leader
	State string `json:"state"` // running, streaming, ...
	Lag   int64  `json:"-"`     // bytes behind the leader; -1 unknown
}

// Leader reports whether m is the Patroni leader.
func (m Member) Leader() bool { return m.Role == "leader" || m.Role == "master" || m.Role == "primary" }

// ParseCluster reads Patroni's GET /cluster.
func ParseCluster(b []byte) ([]Member, error) {
	var raw struct {
		Members []struct {
			Member
			Lag json.RawMessage `json:"lag"`
		} `json:"members"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("reading Patroni cluster state: %w", err)
	}
	out := make([]Member, 0, len(raw.Members))
	for _, r := range raw.Members {
		m := r.Member
		m.Lag = -1
		if m.Leader() {
			m.Lag = 0
		} else if n, err := strconv.ParseInt(strings.Trim(string(r.Lag), `"`), 10, 64); err == nil {
			m.Lag = n
		}
		out = append(out, m)
	}
	return out, nil
}

// SelfName reads this member's name from Patroni's GET /patroni.
func SelfName(b []byte) (string, error) {
	var raw struct {
		Patroni struct {
			Name string `json:"name"`
		} `json:"patroni"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || raw.Patroni.Name == "" {
		return "", fmt.Errorf("reading Patroni member name: %v", err)
	}
	return raw.Patroni.Name, nil
}

// Preflight checks the Patroni cluster can be split: exactly two members,
// one leader, the other replicating.
func Preflight(members []Member, self string) (me, other Member, problems []string) {
	if len(members) != 2 {
		problems = append(problems, fmt.Sprintf("the database cluster has %d members; the migration needs exactly the two HA nodes", len(members)))
	}
	leaders := 0
	found := false
	for _, m := range members {
		if m.Leader() {
			leaders++
		}
		if m.Name == self {
			me, found = m, true
		} else {
			other = m
		}
	}
	if !found {
		problems = append(problems, "this node is not a member of the database cluster")
	}
	if leaders != 1 {
		problems = append(problems, fmt.Sprintf("the database cluster has %d leaders", leaders))
	}
	for _, m := range members {
		if !m.Leader() && m.State != "streaming" && m.State != "running" {
			problems = append(problems, fmt.Sprintf("%s is %s, not replicating", m.Name, m.State))
		}
		if m.Host == "" || net.ParseIP(m.Host) == nil {
			problems = append(problems, fmt.Sprintf("%s has no address in the database cluster", m.Name))
		}
	}
	return me, other, problems
}

// CaughtUp reports whether every replica has replayed everything.
func CaughtUp(members []Member) bool {
	for _, m := range members {
		if !m.Leader() && m.Lag != 0 {
			return false
		}
	}
	return len(members) > 0
}

// MaySwitch decides whether this node switches now. The replica goes first
// (its copy must have everything, including its own "switching" row); the
// leader goes once the replica is switching or gone.
func MaySwitch(selfIsLeader bool, members []Member, otherStatus string) (bool, string) {
	if !selfIsLeader {
		if !CaughtUp(members) {
			return false, "waiting for this node's database copy to catch up"
		}
		return true, ""
	}
	if otherStatus == "switching" || otherStatus == "split" {
		return true, ""
	}
	return false, "waiting for the other node to switch first"
}

var (
	vipBlockRe = regexp.MustCompile(`virtual_ipaddress\s*\{([^}]*)\}`)
	ifaceRe    = regexp.MustCompile(`(?m)^\s*interface\s+([A-Za-z0-9_.@-]+)`)
)

// ParseKeepalived finds the floating address and its interface in a
// keepalived configuration (the first VRRP instance).
func ParseKeepalived(conf string) (vip, iface string) {
	if m := vipBlockRe.FindStringSubmatch(conf); m != nil {
		sc := bufio.NewScanner(strings.NewReader(m[1]))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) > 0 {
				vip = f[0]
				break
			}
		}
	}
	if m := ifaceRe.FindStringSubmatch(conf); m != nil {
		iface = m[1]
	}
	return vip, iface
}

// WithPrefix gives a bare floating address the prefix length of the
// interface's network that contains it ("10.0.0.50" + 10.0.0.1/24 →
// "10.0.0.50/24"). Addresses that already have one are returned unchanged.
func WithPrefix(vip string, ifaceAddrs []*net.IPNet) string {
	if vip == "" || strings.Contains(vip, "/") {
		return vip
	}
	ip := net.ParseIP(vip)
	if ip == nil {
		return ""
	}
	for _, n := range ifaceAddrs {
		if n.Contains(ip) {
			ones, _ := n.Mask.Size()
			return fmt.Sprintf("%s/%d", vip, ones)
		}
	}
	if ip.To4() != nil {
		return vip + "/24"
	}
	return vip + "/64"
}

// Topology guesses the storage topology from the Patroni configuration: a
// co-located etcd witness (port 2381) is Path A' (shared storage); a
// separate witness is Path B (replicated).
func Topology(patroniYAML string) string {
	if strings.Contains(patroniYAML, ":2381") {
		return "shared"
	}
	return "replicated"
}
