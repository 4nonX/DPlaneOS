package nixwriter

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func newTestWriter(t *testing.T) *Writer {
	t.Helper()
	w := New(filepath.Join(t.TempDir(), "state.json"))
	w.nixOS = true
	return w
}

func TestFirewallRules(t *testing.T) {
	w := newTestWriter(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(w.AddFirewallRule(FirewallRule{Action: "allow", Proto: "tcp", Port: 8080, From: "any"}))
	must(w.AddFirewallRule(FirewallRule{Action: "allow", Port: 8080})) // the same rule again
	must(w.AddFirewallRule(FirewallRule{Action: "allow", Proto: "udp", Port: 53}))
	must(w.AddFirewallRule(FirewallRule{Action: "allow", Proto: "tcp", Port: 5432, From: "10.0.0.7/24"}))
	must(w.AddFirewallRule(FirewallRule{Action: "deny", Proto: "tcp", Port: 22, From: "203.0.113.7"}))
	must(w.AddFirewallRule(FirewallRule{Action: "allow", Proto: "tcp", Port: 6000, PortEnd: 6010}))

	st := w.State()
	if !reflect.DeepEqual(st.FirewallTCP, []int{8080}) || !reflect.DeepEqual(st.FirewallUDP, []int{53}) {
		t.Errorf("open ports: tcp %v udp %v", st.FirewallTCP, st.FirewallUDP)
	}
	want := []FirewallRule{
		{Action: "deny", Proto: "tcp", Port: 22, From: "203.0.113.7"},
		{Action: "allow", Proto: "tcp", Port: 5432, From: "10.0.0.0/24"}, // network address
		{Action: "allow", Proto: "tcp", Port: 6000, PortEnd: 6010},
		{Action: "allow", Proto: "tcp", Port: 8080},
		{Action: "allow", Proto: "udp", Port: 53},
	}
	if got := st.FirewallList(); !reflect.DeepEqual(got, want) {
		t.Fatalf("list:\n%+v\nwant\n%+v", got, want)
	}

	// Rule numbers are positions in that list.
	removed, err := w.RemoveFirewallRule(2)
	must(err)
	if removed.Port != 5432 {
		t.Errorf("removed %+v", removed)
	}
	if _, err := w.RemoveFirewallRule(9); err == nil {
		t.Error("removing a rule that does not exist succeeded")
	}
	removed, err = w.RemoveFirewallRule(3) // now 8080/tcp
	must(err)
	if removed.Port != 8080 || len(w.State().FirewallTCP) != 0 {
		t.Errorf("removed %+v, tcp %v", removed, w.State().FirewallTCP)
	}

	// Setting the open ports keeps the other rules.
	must(w.SetFirewallPorts([]int{443, 80}, nil))
	st = w.State()
	if !reflect.DeepEqual(st.FirewallTCP, []int{80, 443}) || len(st.FirewallUDP) != 0 || len(st.FirewallRules) != 2 {
		t.Errorf("after SetFirewallPorts: %+v", st)
	}

	// What was written is what a new process reads.
	w2 := New(w.path)
	w2.nixOS = true
	must(w2.LoadFromDisk())
	if !reflect.DeepEqual(w2.State().FirewallList(), st.FirewallList()) {
		t.Errorf("reloaded: %+v", w2.State().FirewallList())
	}
}

func TestFirewallRuleValidation(t *testing.T) {
	w := newTestWriter(t)
	for _, r := range []FirewallRule{
		{Action: "drop", Proto: "tcp", Port: 80},
		{Action: "allow", Proto: "icmp", Port: 80},
		{Action: "allow", Proto: "tcp", Port: 0},
		{Action: "allow", Proto: "tcp", Port: 70000},
		{Action: "allow", Proto: "tcp", Port: 90, PortEnd: 80},
		{Action: "allow", Proto: "tcp", Port: 80, From: "10.0.0.0/24; reboot"},
		{Action: "allow", Proto: "tcp", Port: 80, From: "example.com"},
	} {
		if err := w.AddFirewallRule(r); err == nil {
			t.Errorf("%+v accepted", r)
		}
	}
	if len(w.State().FirewallList()) != 0 {
		t.Errorf("invalid rules were stored: %+v", w.State().FirewallList())
	}
	for in, want := range map[string][2]int{"80": {80, 0}, "8000:8100": {8000, 8100}, "8000-8100": {8000, 8100}} {
		a, b, err := ParseFirewallPorts(in)
		if err != nil || a != want[0] || b != want[1] {
			t.Errorf("ParseFirewallPorts(%q) = %d, %d, %v", in, a, b, err)
		}
	}
	for _, bad := range []string{"", "http", "80:", "80:x", "8 0"} {
		if _, _, err := ParseFirewallPorts(bad); err == nil {
			t.Errorf("ParseFirewallPorts(%q) accepted", bad)
		}
	}
}

// A second writer on the same file must not exist in one process: Default
// returns the same instance every time.
func TestDefaultIsOneInstance(t *testing.T) {
	if Default() != Default() || Default() != DefaultWriter() {
		t.Fatal("Default returns different writers")
	}
}

// A change the system refuses is taken back everywhere.
func TestRefusedChangeIsTakenBack(t *testing.T) {
	w := newTestWriter(t)
	prevDefault, prevApply := defaultWriter, Apply
	defaultWriter = w
	t.Cleanup(func() { defaultWriter, Apply = prevDefault, prevApply })

	Apply = func(st DPlaneState) error {
		if st.Timezone == "Mars/Olympus" {
			return fmt.Errorf("unknown time zone")
		}
		return nil
	}
	if err := w.SetTimezone("Europe/Berlin"); err != nil {
		t.Fatal(err)
	}
	err := w.SetTimezone("Mars/Olympus")
	if err == nil || !strings.Contains(err.Error(), "not applied") {
		t.Fatalf("error: %v", err)
	}
	if got := w.State().Timezone; got != "Europe/Berlin" {
		t.Errorf("state in memory: %q", got)
	}
	w2 := New(w.path)
	w2.nixOS = true
	if err := w2.LoadFromDisk(); err != nil || w2.State().Timezone != "Europe/Berlin" {
		t.Errorf("state on disk: %q, %v", w2.State().Timezone, err)
	}
	// The next, unrelated change is not affected.
	if err := w.SetNTP([]string{"10.0.0.1"}); err != nil {
		t.Errorf("following change: %v", err)
	}

	// A system that already has a problem does not refuse every change.
	Apply = func(DPlaneState) error { return fmt.Errorf("UPS: the driver does not start") }
	err = w.SetHostname("nas2")
	if err == nil || !strings.Contains(err.Error(), "saved;") {
		t.Fatalf("error: %v", err)
	}
	if w.State().Hostname != "nas2" {
		t.Errorf("the change was dropped: %+v", w.State())
	}
}
