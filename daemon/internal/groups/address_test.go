package groups

import "testing"

func TestValidAddress(t *testing.T) {
	for _, c := range []struct {
		cidr, iface string
		ok          bool
	}{
		{"", "", true},
		{"192.168.1.50/24", "eth0", true},
		{"fd00::50/64", "enp1s0", true},
		{"192.168.1.50", "eth0", false},
		{"127.0.0.1/8", "lo", false},
		{"192.168.1.50/24", "", false},
		{"192.168.1.50/24", "eth0; reboot", false},
	} {
		if err := validAddress(c.cidr, c.iface); (err == nil) != c.ok {
			t.Errorf("%q %q: %v", c.cidr, c.iface, err)
		}
	}
}

func TestHoldAddress(t *testing.T) {
	have := map[string]bool{}
	var log []string
	m := &Manager{addr: AddrOps{
		Present:  func(i, c string) (bool, error) { return have[i+c], nil },
		Add:      func(i, c string) error { have[i+c] = true; log = append(log, "add "+c); return nil },
		Del:      func(i, c string) error { delete(have, i+c); log = append(log, "del "+c); return nil },
		Announce: func(i, c string) error { log = append(log, "arp "+c); return nil },
	}}
	g := Group{Name: "data", Address: "10.0.0.5/24", Interface: "eth0"}
	_ = m.holdAddress(g, true)
	_ = m.holdAddress(g, true) // already there: nothing
	g.Address = "10.0.0.6/24"  // changed: the old one goes
	_ = m.holdAddress(g, true)
	_ = m.holdAddress(g, false)
	want := []string{"add 10.0.0.5/24", "arp 10.0.0.5/24", "del 10.0.0.5/24", "add 10.0.0.6/24", "arp 10.0.0.6/24", "del 10.0.0.6/24"}
	if len(log) != len(want) {
		t.Fatalf("got %v", log)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Fatalf("got %v, want %v", log, want)
		}
	}
}
