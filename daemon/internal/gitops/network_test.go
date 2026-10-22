package gitops

import (
	"testing"

	"dplaned/internal/networkdwriter"
	"dplaned/internal/nixwriter"
)

func TestDesiredNetworkState(t *testing.T) {
	cur := networkdwriter.State{
		Interfaces: map[string]networkdwriter.Iface{
			"eth0": {Addresses: []string{"10.0.0.5/24"}, Gateway: "10.0.0.1"},
			"eth3": {Addresses: []string{"10.3.0.5/24"}}, // no longer declared
			"eth4": {DHCP: true},
		},
		DNS: networkdwriter.DNS{Servers: []string{"1.1.1.1"}},
	}
	ds := &DesiredSystem{
		DNSServers: []string{"10.0.0.53"},
		Networking: DesiredNetworking{
			Statics: map[string]DesiredNetworkStatic{"eth0": {CIDR: "10.0.0.6/24", Gateway: "10.0.0.1"}},
			Bonds:   map[string]DesiredNetworkBond{"bond0": {Mode: "802.3ad", Slaves: []string{"eth1", "eth2"}}},
			VLANs:   map[string]DesiredNetworkVLAN{"eth0.20": {Parent: "eth0", VID: 20}},
		},
	}
	next := desiredNetworkState(cur, ds)
	if err := next.Validate(); err != nil {
		t.Fatal(err)
	}
	if next.Interfaces["eth0"].Addresses[0] != "10.0.0.6/24" || next.Bonds["bond0"].Mode != "802.3ad" ||
		next.VLANs["eth0.20"].ID != 20 || next.DNS.Servers[0] != "10.0.0.53" {
		t.Errorf("next: %+v", next)
	}
	if _, ok := next.Interfaces["eth3"]; ok {
		t.Error("undeclared static kept")
	}
	if !next.Interfaces["eth4"].DHCP {
		t.Error("DHCP interface dropped")
	}

	// Statics not declared at all: nothing is removed.
	next = desiredNetworkState(cur, &DesiredSystem{})
	if _, ok := next.Interfaces["eth3"]; !ok {
		t.Error("static removed without a statics section")
	}

	// The live view built from it matches the declaration (no endless diff).
	live := nixwriter.DPlaneState{}
	overlayLiveNetwork(&live, desiredNetworkState(cur, ds))
	for _, c := range diffSystem(*ds, live) {
		if c != "" && (len(c) < 3 || c[:3] != "ntp" && c[:8] != "firewall") {
			t.Errorf("unexpected change after apply: %s", c)
		}
	}
}
