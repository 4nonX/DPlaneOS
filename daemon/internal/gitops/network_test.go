package gitops

import (
	"reflect"
	"strings"
	"testing"

	"dplaned/internal/netconf"
	"dplaned/internal/nixwriter"
)

// The "system" section of state.yaml and the network configuration in the
// database: what is asked for is what gets saved, and the plan says so.
func TestDesiredNetwork(t *testing.T) {
	cur := netconf.Config{
		Interfaces: []netconf.Interface{{Name: "eth0", CIDR: "10.0.0.5/24", Gateway: "10.0.0.1"}, {Name: "eth1", DHCP: true}},
		Bonds:      []netconf.Bond{{Name: "bond0", Mode: "802.3ad", Slaves: []string{"eth2", "eth3"}}},
		DNS:        []string{"10.0.0.1"},
		Search:     []string{"lan"},
	}

	// A section that does not describe interfaces leaves them alone.
	got := desiredNetwork(&DesiredSystem{Hostname: "nas"}, cur)
	if !reflect.DeepEqual(networkShape(got), networkShape(cur)) {
		t.Errorf("a section without networking changed it: %+v", got)
	}

	// DNS servers are replaced in the order given; search domains stay.
	got = desiredNetwork(&DesiredSystem{DNSServers: []string{"9.9.9.9", "1.1.1.1"}}, cur)
	if !reflect.DeepEqual(got.DNS, []string{"9.9.9.9", "1.1.1.1"}) || !reflect.DeepEqual(got.Search, []string{"lan"}) {
		t.Errorf("dns: %+v", got)
	}

	// A section that describes interfaces is complete.
	ds := &DesiredSystem{Networking: DesiredNetworking{
		Statics: map[string]DesiredNetworkStatic{"eth1": {CIDR: "10.0.1.5/24"}},
		VLANs:   map[string]DesiredNetworkVLAN{"eth1.20": {Parent: "eth1", VID: 20}},
	}}
	got = desiredNetwork(ds, cur)
	want := netconf.Config{
		Interfaces: []netconf.Interface{{Name: "eth1", CIDR: "10.0.1.5/24"}},
		VLANs:      []netconf.VLAN{{Name: "eth1.20", Parent: "eth1", VID: 20}},
		DNS:        []string{"10.0.0.1"}, Search: []string{"lan"},
	}
	if !reflect.DeepEqual(networkShape(got), networkShape(want)) {
		t.Errorf("got %+v\nwant %+v", networkShape(got), networkShape(want))
	}
	if _, err := netconf.Render(got); err != nil {
		t.Errorf("the result cannot be rendered: %v", err)
	}

	// The plan names every one of these changes, and they are high risk.
	live := nixwriter.DPlaneState{
		DNSServers:     cur.DNS,
		NetworkStatics: map[string]nixwriter.NetworkStaticEntry{"eth0": {CIDR: "10.0.0.5/24", Gateway: "10.0.0.1"}},
		NetworkBonds:   map[string]nixwriter.NetworkBondEntry{"bond0": {Mode: "802.3ad", Slaves: []string{"eth2", "eth3"}}},
	}
	changes := strings.Join(diffSystem(*ds, live), "\n")
	for _, w := range []string{"network-add: eth1", "network-remove: eth0", "network-bond-remove: bond0", "network-vlan-add: eth1.20"} {
		if !strings.Contains(changes, w) {
			t.Errorf("plan lacks %q:\n%s", w, changes)
		}
	}
	if riskForSystemChanges(diffSystem(*ds, live)) != "high" {
		t.Error("network changes are not high risk")
	}
	if c := diffSystem(DesiredSystem{}, live); len(c) != 0 {
		t.Errorf("an empty section plans changes: %v", c)
	}
}
