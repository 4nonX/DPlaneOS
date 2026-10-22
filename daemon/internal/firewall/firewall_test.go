package firewall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	r := Rule{Action: "Allow", Port: "445/tcp", From: "192.168.10.7/24"}
	if err := r.Normalize(); err != nil {
		t.Fatal(err)
	}
	if r.Action != "allow" || r.Port != "445" || r.Proto != "tcp" || r.From != "192.168.10.0/24" {
		t.Errorf("normalized: %+v", r)
	}
	for _, bad := range []Rule{
		{Action: "allow", Port: "0"}, {Action: "allow", Port: "70000"}, {Action: "allow", Port: "100-90"},
		{Action: "allow", Port: "22; flush ruleset"}, {Action: "reject", Port: "22"}, {Action: "allow", Port: "22", Proto: "icmp"},
		{Action: "allow", Port: "22", From: "10.0.0.0/8 accept"}, {Action: "allow", Port: "22", Comment: `x" drop`},
	} {
		if bad.Normalize() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestScript(t *testing.T) {
	rules := []Rule{
		{Action: "allow", Port: "8080", Proto: "tcp", From: "10.0.0.0/8"},
		{Action: "deny", Port: "445", Proto: "tcp", From: "fd00::/64"},
		{Action: "allow", Port: "40000-40100", Proto: "udp"},
		{Action: "allow", Port: "53", Proto: "both"},
	}
	s := Script(rules)
	for _, want := range []string{
		"table inet dplaneos_fw {}\ndelete table inet dplaneos_fw\n", // atomic replace
		"priority filter - 5; policy accept;",
		"ip saddr 10.0.0.0/8 tcp dport 8080 meta mark set meta mark | 0x01000000 accept",
		"ip6 saddr fd00::/64 tcp dport 445 drop",
		"udp dport 40000-40100 meta mark set meta mark | 0x01000000 accept",
		"meta l4proto { tcp, udp } th dport 53 meta mark set meta mark | 0x01000000 accept",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
	// Rule order is kept: the first matching rule decides.
	if strings.Index(s, "dport 8080") > strings.Index(s, "dport 445") {
		t.Error("order changed")
	}
}

func TestPendingRevert(t *testing.T) {
	dir := t.TempDir()
	var loaded []string
	s := &Store{rulesPath: filepath.Join(dir, "r.json"), scriptPath: filepath.Join(dir, "fw.nft"),
		run: func(n string, a ...string) ([]byte, error) {
			b, _ := os.ReadFile(a[len(a)-1])
			loaded = append(loaded, string(b))
			return nil, nil
		}}
	base := []Rule{{Action: "allow", Port: "22", Proto: "tcp"}}
	if err := s.ApplyPending(base); err != nil {
		t.Fatal(err)
	}
	s.Confirm()
	if err := s.ApplyPending([]Rule{{Action: "deny", Port: "443", Proto: "tcp"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPending(base); err != ErrPending {
		t.Errorf("second pending: %v", err)
	}
	if err := s.RevertPending(); err != nil {
		t.Fatal(err)
	}
	if got := s.Rules(); len(got) != 1 || got[0].Port != "22" {
		t.Errorf("after revert: %+v", got)
	}
	if last := loaded[len(loaded)-1]; !strings.Contains(last, "dport 22") || strings.Contains(last, "dport 443") {
		t.Errorf("kernel not reverted:\n%s", last)
	}
	if b, _ := os.ReadFile(s.scriptPath); !strings.Contains(string(b), "dport 22") {
		t.Error("boot script not reverted")
	}
	// An invalid saved rule does not load half a rule set.
	if err := s.ApplyPending([]Rule{{Action: "allow", Port: "nope"}}); err == nil {
		t.Error("invalid rule applied")
	}
	if s.HasPending() {
		t.Error("failed change left pending")
	}
}
