package gitops

import (
	"database/sql"
	"log"
	"maps"
	"slices"

	"dplaned/internal/networkdwriter"
	"dplaned/internal/nixwriter"
	"dplaned/internal/shareusers"
)

// Network settings (static addresses, bonds, VLANs, DNS servers) live in the
// networkd configuration that the Network page also writes, not in the NixOS
// bridge: the bridge's network units sorted after the distribution's and
// were ignored for configured interfaces, a VLAN unit replaced its parent's
// address configuration, and the page did not show them.

// overlayLiveNetwork replaces the network fields of the bridge state with
// the networkd configuration.
func overlayLiveNetwork(s *nixwriter.DPlaneState, ns networkdwriter.State) {
	s.NetworkStatics, s.NetworkBonds, s.NetworkVLANs = nil, nil, nil
	s.DNSServers = slices.Clone(ns.DNS.Servers)
	for name, ifc := range ns.Interfaces {
		if len(ifc.Addresses) == 0 {
			continue
		}
		if s.NetworkStatics == nil {
			s.NetworkStatics = map[string]nixwriter.NetworkStaticEntry{}
		}
		s.NetworkStatics[name] = nixwriter.NetworkStaticEntry{CIDR: ifc.Addresses[0], Gateway: ifc.Gateway}
	}
	for name, b := range ns.Bonds {
		if s.NetworkBonds == nil {
			s.NetworkBonds = map[string]nixwriter.NetworkBondEntry{}
		}
		s.NetworkBonds[name] = nixwriter.NetworkBondEntry{Slaves: slices.Clone(b.Slaves), Mode: b.Mode}
	}
	for name, v := range ns.VLANs {
		if s.NetworkVLANs == nil {
			s.NetworkVLANs = map[string]nixwriter.NetworkVLANEntry{}
		}
		s.NetworkVLANs[name] = nixwriter.NetworkVLANEntry{Parent: v.Parent, VID: v.ID}
	}
}

// desiredNetworkState applies the declared network settings to the current
// networkd state. Declared statics, bonds and VLANs are set; a static address
// that is no longer declared is removed when the statics are declared at all
// (the interface goes back to the system configuration). DNS servers are set
// when declared.
func desiredNetworkState(cur networkdwriter.State, ds *DesiredSystem) networkdwriter.State {
	next := cur.Clone()
	if next.Interfaces == nil {
		next.Interfaces = map[string]networkdwriter.Iface{}
	}
	if next.Bonds == nil {
		next.Bonds = map[string]networkdwriter.Bond{}
	}
	if next.VLANs == nil {
		next.VLANs = map[string]networkdwriter.VLAN{}
	}
	if ds.Networking.Statics != nil {
		for name, ifc := range maps.Clone(next.Interfaces) {
			if _, ok := ds.Networking.Statics[name]; !ok && len(ifc.Addresses) > 0 {
				ifc.Addresses, ifc.Gateway = nil, ""
				if ifc.DHCP || len(ifc.Routes) > 0 || isDevice(next, name) {
					next.Interfaces[name] = ifc
				} else {
					delete(next.Interfaces, name)
				}
			}
		}
	}
	for name, st := range ds.Networking.Statics {
		ifc := next.Interfaces[name]
		ifc.DHCP, ifc.Addresses, ifc.Gateway = false, []string{st.CIDR}, st.Gateway
		next.Interfaces[name] = ifc
	}
	for name, b := range ds.Networking.Bonds {
		next.Bonds[name] = networkdwriter.Bond{Mode: b.Mode, Slaves: slices.Clone(b.Slaves)}
		for _, sl := range b.Slaves {
			delete(next.Interfaces, sl) // a member carries no address of its own
		}
	}
	for name, v := range ds.Networking.VLANs {
		next.VLANs[name] = networkdwriter.VLAN{Parent: v.Parent, ID: v.VID}
	}
	if len(ds.DNSServers) > 0 {
		next.DNS.Servers = slices.Clone(ds.DNSServers)
	}
	return next
}

func isDevice(s networkdwriter.State, name string) bool {
	_, b := s.Bonds[name]
	_, v := s.VLANs[name]
	return b || v
}

// syncShareAccounts applies users and groups to the system and Samba
// accounts (internal/shareusers). A failure there does not undo the change.
func syncShareAccounts(db *sql.DB) error {
	if err := shareusers.Sync(db); err != nil {
		log.Printf("GITOPS: file sharing accounts: %v", err)
	}
	return nil
}
