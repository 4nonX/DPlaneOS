// Package firewall keeps the firewall rules made in the web UI on NixOS.
//
// The NixOS firewall (networking.firewall) opens the ports the system
// configuration declares and refuses everything else; changing it needs a
// rebuild. The rules made here apply at once, in an nftables table of their
// own whose input chain runs before the NixOS firewall:
//
//   - allow: the packet is marked (Mark) and accepted; the NixOS firewall has
//     one static rule (module.nix) that accepts marked packets.
//   - deny: the packet is dropped, also for a port the system configuration
//     opens (e.g. SMB from a guest network).
//
// Rules are checked in order; the first that matches decides. Traffic that
// matches no rule is left to the NixOS firewall. The rules are saved as JSON
// and as the nft script, which dplaneos-firewall.service loads at boot.
package firewall

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Table is the nftables table (family inet) the rules live in.
	Table = "dplaneos_fw"
	// Mark is the packet mark bit for allowed traffic; module.nix accepts it.
	Mark = "0x01000000"

	DefaultRulesPath  = "/var/lib/dplaneos/firewall-rules.json"
	DefaultScriptPath = "/var/lib/dplaneos/firewall.nft"
	// SystemPortsPath lists the ports the system configuration opens
	// (written by module.nix).
	SystemPortsPath = "/etc/dplaneos/firewall-system.json"
)

// Rule is one firewall rule.
type Rule struct {
	Action  string `json:"action"`         // allow | deny
	Port    string `json:"port"`           // "443" or "40000-40100"
	Proto   string `json:"proto"`          // tcp | udp | both
	From    string `json:"from,omitempty"` // source address or network; empty = any
	Comment string `json:"comment,omitempty"`
}

// Normalize fills defaults and checks the rule.
func (r *Rule) Normalize() error {
	r.Action = strings.ToLower(strings.TrimSpace(r.Action))
	r.Proto = strings.ToLower(strings.TrimSpace(r.Proto))
	r.Port = strings.TrimSpace(r.Port)
	r.From = strings.TrimSpace(r.From)
	if p, proto, ok := strings.Cut(r.Port, "/"); ok { // "80/tcp"
		r.Port = p
		if r.Proto == "" {
			r.Proto = proto
		}
	}
	if r.Proto == "" {
		r.Proto = "tcp"
	}
	if r.Action != "allow" && r.Action != "deny" {
		return fmt.Errorf("action must be allow or deny")
	}
	if r.Proto == "tcp+udp" {
		r.Proto = "both"
	}
	if r.Proto != "tcp" && r.Proto != "udp" && r.Proto != "both" {
		return fmt.Errorf("protocol must be tcp, udp or both")
	}
	r.Port = strings.Replace(r.Port, ":", "-", 1) // ufw writes ranges as 8000:8100
	lo, hi, isRange := strings.Cut(r.Port, "-")
	a, err1 := strconv.Atoi(lo)
	b := a
	var err2 error
	if isRange {
		b, err2 = strconv.Atoi(hi)
	}
	if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b {
		return fmt.Errorf("invalid port %q (1-65535 or a range like 40000-40100)", r.Port)
	}
	if r.From == "any" || r.From == "Anywhere" {
		r.From = ""
	}
	if r.From != "" {
		if ip := net.ParseIP(r.From); ip != nil {
			r.From = ip.String()
		} else if _, n, err := net.ParseCIDR(r.From); err == nil {
			r.From = n.String()
		} else {
			return fmt.Errorf("invalid source %q: an address or a network like 192.168.1.0/24", r.From)
		}
	}
	if len(r.Comment) > 64 || strings.ContainsAny(r.Comment, "\"\\\n\r") {
		return fmt.Errorf("the comment may have up to 64 characters, no quotes")
	}
	return nil
}

func (r Rule) nft() string {
	var b strings.Builder
	b.WriteString("    ")
	if r.From != "" {
		if ip := net.ParseIP(strings.Split(r.From, "/")[0]); ip != nil && ip.To4() == nil {
			b.WriteString("ip6 saddr " + r.From + " ")
		} else {
			b.WriteString("ip saddr " + r.From + " ")
		}
	}
	if r.Proto == "both" {
		b.WriteString("meta l4proto { tcp, udp } th dport " + r.Port + " ")
	} else {
		b.WriteString(r.Proto + " dport " + r.Port + " ")
	}
	if r.Action == "allow" {
		b.WriteString("meta mark set meta mark | " + Mark + " accept")
	} else {
		b.WriteString("drop")
	}
	return b.String()
}

// Script renders the rules as an nft script that replaces the table at once.
func Script(rules []Rule) string {
	var b strings.Builder
	b.WriteString("# Managed by DPlaneOS - firewall rules from the web UI\n")
	b.WriteString("table inet " + Table + " {}\ndelete table inet " + Table + "\n")
	b.WriteString("table inet " + Table + " {\n  chain input {\n    type filter hook input priority filter - 5; policy accept;\n")
	b.WriteString("    iif \"lo\" accept\n    ct state established,related accept\n")
	for _, r := range rules {
		b.WriteString(r.nft() + "\n")
	}
	b.WriteString("  }\n}\n")
	return b.String()
}

// Store keeps the rules and applies them.
type Store struct {
	mu         sync.Mutex
	rulesPath  string
	scriptPath string
	run        func(name string, args ...string) ([]byte, error)
}

// New returns a Store for the default paths.
func New() *Store {
	return &Store{rulesPath: DefaultRulesPath, scriptPath: DefaultScriptPath,
		run: func(n string, a ...string) ([]byte, error) { return exec.Command(n, a...).CombinedOutput() }}
}

// Rules returns the saved rules.
func (s *Store) Rules() []Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) load() []Rule {
	var rules []Rule
	if b, err := os.ReadFile(s.rulesPath); err == nil {
		_ = json.Unmarshal(b, &rules)
	}
	return rules
}

func (s *Store) apply(rules []Rule) error {
	for i := range rules {
		if err := rules[i].Normalize(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.scriptPath), 0o755); err != nil {
		return err
	}
	script := Script(rules)
	tmp := s.scriptPath + ".new"
	if err := os.WriteFile(tmp, []byte(script), 0o600); err != nil {
		return err
	}
	if out, err := s.run("nft", "-f", tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("nft: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, s.scriptPath); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(rules, "", "  ")
	return writeAtomic(s.rulesPath, b)
}

// Sync loads the saved rules into the kernel (daemon start).
func (s *Store) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apply(s.load())
}

func (s *Store) pendingPath() string { return s.rulesPath + ".pending" }

// ErrPending: a change waits for confirmation.
var ErrPending = errors.New("another firewall change is waiting for confirmation")

// ApplyPending applies rules and keeps the previous ones to revert to.
func (s *Store) ApplyPending(rules []Rule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.pendingPath()); err == nil {
		return ErrPending
	}
	prev := s.load()
	b, _ := json.Marshal(prev)
	if err := writeAtomic(s.pendingPath(), b); err != nil {
		return err
	}
	if err := s.apply(rules); err != nil {
		os.Remove(s.pendingPath())
		return err
	}
	return nil
}

// Confirm keeps the pending change.
func (s *Store) Confirm() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.pendingPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// HasPending reports whether a change waits for confirmation.
func (s *Store) HasPending() bool {
	_, err := os.Stat(s.pendingPath())
	return err == nil
}

// RevertPending applies the rules from before the pending change.
func (s *Store) RevertPending() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.pendingPath())
	if err != nil {
		return err
	}
	var prev []Rule
	_ = json.Unmarshal(b, &prev)
	err = s.apply(prev)
	os.Remove(s.pendingPath())
	return err
}

// Active reports whether the rule table is loaded.
func (s *Store) Active() bool {
	_, err := s.run("nft", "list", "table", "inet", Table)
	return err == nil
}

// SystemPorts are the ports the NixOS configuration opens.
type SystemPorts struct {
	Enabled    bool        `json:"enabled"`
	TCP        []int       `json:"tcp"`
	UDP        []int       `json:"udp"`
	TCPRanges  []PortRange `json:"tcp_ranges"`
	UDPRanges  []PortRange `json:"udp_ranges"`
	Interfaces []string    `json:"trusted_interfaces"`
}

// PortRange is an inclusive port range.
type PortRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// ReadSystemPorts reads the ports the system configuration opens.
func ReadSystemPorts() (*SystemPorts, error) {
	b, err := os.ReadFile(SystemPortsPath)
	if err != nil {
		return nil, err
	}
	var p SystemPorts
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func writeAtomic(path string, b []byte) error {
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
