package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/networkdwriter"
)

// Network changes (interfaces, VLANs, bonds, routes, DNS) and firewall rules
// are applied at once and kept only when confirmed within the confirm window,
// as TrueNAS does: a change that cuts the browser off is reverted on its own.
// One change waits for confirmation at a time.

// networkConfirmWindow: time to confirm, including opening the UI on a new
// address and logging in there.
var networkConfirmWindow = 90 * time.Second

var (
	pendingMu       sync.Mutex
	pendingTimer    *time.Timer
	pendingWhat     string
	pendingDeadline time.Time
	pendingConfirm  func() error
	pendingRevert   func() error
)

var errChangePending = errors.New("another change is waiting for confirmation; confirm it or wait until it is reverted")

// applyConfirmable runs apply (which changes the system) and then waits for
// confirmation: revert runs when the window ends, confirm when the operator
// confirms. apply is not called while another change waits.
func applyConfirmable(what string, apply func(deadline time.Time) error, confirm, revert func() error) error {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if pendingTimer != nil {
		return errChangePending
	}
	deadline := time.Now().Add(networkConfirmWindow)
	if err := apply(deadline); err != nil {
		return err
	}
	pendingWhat, pendingDeadline, pendingConfirm, pendingRevert = what, deadline, confirm, revert
	var t *time.Timer
	t = time.AfterFunc(networkConfirmWindow, func() {
		pendingMu.Lock()
		defer pendingMu.Unlock()
		if pendingTimer != t {
			return
		}
		log.Printf("NETWORK: %q not confirmed within %s, reverting", pendingWhat, networkConfirmWindow)
		if err := pendingRevert(); err != nil {
			log.Printf("NETWORK: revert %q: %v", pendingWhat, err)
		}
		audit.LogActivity("system", "network_change_reverted", map[string]any{"change": pendingWhat})
		pendingTimer = nil
	})
	pendingTimer = t
	return nil
}

// ConfirmNetwork keeps the change that waits for confirmation.
// POST /api/network/confirm
func ConfirmNetwork(w http.ResponseWriter, r *http.Request) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if pendingTimer == nil {
		respondOK(w, map[string]any{"success": false, "error": "No change is waiting for confirmation (it may already have been reverted)"})
		return
	}
	pendingTimer.Stop()
	pendingTimer = nil
	if err := pendingConfirm(); err != nil {
		respondOK(w, map[string]any{"success": false, "error": "Confirming: " + err.Error()})
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "network_change_confirmed", map[string]any{"change": pendingWhat})
	respondOK(w, map[string]any{"success": true, "message": "Change confirmed"})
}

// RevertNetworkNow reverts the waiting change without waiting for the window.
// POST /api/network/revert
func RevertNetworkNow(w http.ResponseWriter, r *http.Request) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if pendingTimer == nil {
		respondOK(w, map[string]any{"success": false, "error": "No change is waiting for confirmation"})
		return
	}
	pendingTimer.Stop()
	pendingTimer = nil
	if err := pendingRevert(); err != nil {
		respondOK(w, map[string]any{"success": false, "error": "Reverting: " + err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true, "message": "Change reverted"})
}

// GetNetworkPending says whether a change waits for confirmation.
// GET /api/network/pending
func GetNetworkPending(w http.ResponseWriter, r *http.Request) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if pendingTimer == nil {
		respondOK(w, map[string]any{"success": true, "pending": false})
		return
	}
	left := max(int(time.Until(pendingDeadline).Seconds()), 0)
	respondOK(w, map[string]any{"success": true, "pending": true, "change": pendingWhat, "seconds_left": left})
}

// RevertUnconfirmedNetwork runs at daemon start: a change that was waiting
// for confirmation when the daemon stopped (or the machine rebooted) was not
// confirmed and is reverted.
func RevertUnconfirmedNetwork() {
	if NetWriter == nil {
		return
	}
	if ok, _ := NetWriter.HasPending(); ok {
		log.Printf("NETWORK: a change was not confirmed before the daemon stopped; reverting it")
		if err := NetWriter.RevertPending(); err != nil {
			log.Printf("NETWORK: revert: %v", err)
		}
	}
	if NetWriter.IsNetworkd() {
		if err := NetWriter.Sync(); err != nil {
			log.Printf("NETWORK: writing the saved configuration: %v", err)
		}
	}
}

// changeNetwork applies a change to the networkd state as a confirmable change.
func changeNetwork(w http.ResponseWriter, r *http.Request, what string, mutate func(s *networkdwriter.State) error) {
	if NetWriter == nil || !NetWriter.IsNetworkd() {
		respondErrorSimple(w, "Network configuration needs systemd-networkd", http.StatusNotImplemented)
		return
	}
	s := NetWriter.State()
	if s.Interfaces == nil {
		s.Interfaces = map[string]networkdwriter.Iface{}
	}
	if s.Bonds == nil {
		s.Bonds = map[string]networkdwriter.Bond{}
	}
	if s.VLANs == nil {
		s.VLANs = map[string]networkdwriter.VLAN{}
	}
	if err := mutate(&s); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Validate(); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	start := time.Now()
	err := applyConfirmable(what,
		func(deadline time.Time) error { return NetWriter.ApplyPending(s, deadline) },
		NetWriter.Confirm, NetWriter.RevertPending)
	audit.LogCommand(audit.LevelInfo, r.Header.Get("X-User"), "network_change", []string{what}, err == nil, time.Since(start), err)
	switch {
	case errors.Is(err, errChangePending), errors.Is(err, networkdwriter.ErrPending):
		respondErrorSimple(w, errChangePending.Error(), http.StatusConflict)
	case err != nil:
		respondOK(w, map[string]any{"success": false, "error": err.Error() + " (the previous configuration was put back)"})
	default:
		respondOK(w, map[string]any{
			"success":        true,
			"message":        what + ": applied; confirm to keep it",
			"revert_seconds": int(networkConfirmWindow.Seconds()),
		})
	}
}

// adopt puts an interface's current configuration into the state before the
// state starts to configure it (DPlaneOS's file then replaces the
// distribution's, so it must carry the address the interface has now).
func adopt(s *networkdwriter.State, iface string) error {
	if s.Mentions(iface) {
		return nil
	}
	live, err := NetWriter.LiveConfig(iface)
	if err != nil {
		return err
	}
	if live.DHCP || len(live.Addresses) > 0 {
		s.Interfaces[iface] = live
	}
	return nil
}

func linkExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

// configureInterface: { interface, dhcp, address|ip, netmask, gateway }
func configureInterface(w http.ResponseWriter, r *http.Request, req map[string]any) {
	iface, _ := req["interface"].(string)
	dhcp, _ := req["dhcp"].(bool)
	address, _ := req["address"].(string)
	if address == "" {
		address, _ = req["ip"].(string)
	}
	netmask, _ := req["netmask"].(string)
	gateway, _ := req["gateway"].(string)
	if err := networkdwriter.ValidateIface(iface); err != nil {
		respondErrorSimple(w, "Invalid interface", http.StatusBadRequest)
		return
	}
	if _, ok := req["mtu"]; ok {
		respondErrorSimple(w, "Changing the MTU is not supported", http.StatusBadRequest)
		return
	}
	if dhcp && (address != "" || gateway != "") {
		respondErrorSimple(w, "Use either DHCP or a static address, not both", http.StatusBadRequest)
		return
	}
	if !dhcp {
		if address == "" {
			respondErrorSimple(w, "A static configuration needs an address", http.StatusBadRequest)
			return
		}
		if !strings.Contains(address, "/") && netmask != "" {
			prefix := netmaskToCIDR(netmask)
			if prefix < 0 {
				respondErrorSimple(w, "Invalid netmask", http.StatusBadRequest)
				return
			}
			address += "/" + strconv.Itoa(prefix)
		}
		if _, _, err := net.ParseCIDR(address); err != nil {
			respondErrorSimple(w, "Invalid address: use address/prefix or give a netmask", http.StatusBadRequest)
			return
		}
		if gateway != "" && net.ParseIP(gateway) == nil {
			respondErrorSimple(w, "Invalid gateway", http.StatusBadRequest)
			return
		}
	}
	what := iface + ": DHCP"
	if !dhcp {
		what = iface + ": " + address
	}
	changeNetwork(w, r, what, func(s *networkdwriter.State) error {
		if !linkExists(iface) && !s.Mentions(iface) {
			return fmt.Errorf("interface %s does not exist", iface)
		}
		ifc := s.Interfaces[iface]
		ifc.DHCP, ifc.Addresses, ifc.Gateway = dhcp, nil, ""
		if !dhcp {
			ifc.Addresses, ifc.Gateway = []string{address}, gateway
		}
		s.Interfaces[iface] = ifc
		return nil
	})
}

// changeRoute: { action: add|delete, destination, gateway, interface }.
// "default" sets or clears the interface's gateway.
func changeRoute(w http.ResponseWriter, r *http.Request, action string, req map[string]any) {
	dst, _ := req["destination"].(string)
	gw, _ := req["gateway"].(string)
	iface, _ := req["interface"].(string)
	gw = strings.Split(gw, "/")[0]
	if dst != "default" {
		if ip := net.ParseIP(dst); ip != nil {
			if ip.To4() != nil {
				dst += "/32"
			} else {
				dst += "/128"
			}
		}
		if _, _, err := net.ParseCIDR(dst); err != nil {
			respondErrorSimple(w, "Invalid destination", http.StatusBadRequest)
			return
		}
	}
	if action == "add" && net.ParseIP(gw) == nil {
		respondErrorSimple(w, "A route needs a gateway address", http.StatusBadRequest)
		return
	}
	if iface == "" && gw != "" {
		iface = interfaceForGateway(net.ParseIP(gw))
	}
	if err := networkdwriter.ValidateIface(iface); err != nil {
		respondErrorSimple(w, "Give the interface of the route (no interface is on the gateway's network)", http.StatusBadRequest)
		return
	}
	changeNetwork(w, r, fmt.Sprintf("route %s %s via %s", action, dst, gw), func(s *networkdwriter.State) error {
		if err := adopt(s, iface); err != nil {
			return err
		}
		ifc := s.Interfaces[iface]
		if dst == "default" {
			if action == "add" {
				ifc.Gateway = gw
			} else {
				ifc.Gateway = ""
			}
			s.Interfaces[iface] = ifc
			return nil
		}
		i := slices.IndexFunc(ifc.Routes, func(rt networkdwriter.Route) bool {
			return rt.Destination == dst && (gw == "" || rt.Gateway == gw)
		})
		if action == "add" {
			if i >= 0 {
				ifc.Routes[i].Gateway = gw
			} else {
				ifc.Routes = append(ifc.Routes, networkdwriter.Route{Destination: dst, Gateway: gw})
			}
		} else {
			if i < 0 {
				return fmt.Errorf("no route to %s on %s was configured here", dst, iface)
			}
			ifc.Routes = slices.Delete(ifc.Routes, i, i+1)
		}
		s.Interfaces[iface] = ifc
		return nil
	})
}

// interfaceForGateway: the interface with an address in the gateway's subnet.
func interfaceForGateway(gw net.IP) string {
	if gw == nil {
		return ""
	}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.Contains(gw) {
				return ifc.Name
			}
		}
	}
	return ""
}

// setDNS: { nameservers: [...], search: [...] }
func setDNS(w http.ResponseWriter, r *http.Request, req map[string]any) {
	var servers, search []string
	if raw, ok := req["nameservers"].([]any); ok {
		for _, s := range raw {
			ip, _ := s.(string)
			if ip = strings.TrimSpace(ip); ip == "" {
				continue
			}
			if net.ParseIP(ip) == nil {
				respondErrorSimple(w, "Invalid DNS server: "+ip, http.StatusBadRequest)
				return
			}
			servers = append(servers, ip)
		}
	}
	if len(servers) == 0 {
		respondErrorSimple(w, "At least one valid DNS server IP required", http.StatusBadRequest)
		return
	}
	if raw, ok := req["search"].([]any); ok {
		for _, s := range raw {
			if d, _ := s.(string); strings.TrimSpace(d) != "" {
				search = append(search, strings.TrimSpace(d))
			}
		}
	}
	changeNetwork(w, r, "DNS "+strings.Join(servers, ", "), func(s *networkdwriter.State) error {
		s.DNS = networkdwriter.DNS{Servers: servers, Search: search}
		return nil
	})
}

// ── VLANs ─────────────────────────────────────────────────────────────────────

// CreateVLAN creates a VLAN interface.
// POST /api/network/vlan { "parent": "eth0", "vlan_id": 100, "ip": "10.0.100.1/24", "dhcp": false }
func CreateVLAN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Parent string `json:"parent"`
		VlanID int    `json:"vlan_id"`
		IP     string `json:"ip"`
		DHCP   bool   `json:"dhcp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := networkdwriter.ValidateIface(req.Parent); err != nil {
		respondErrorSimple(w, "Invalid parent interface", http.StatusBadRequest)
		return
	}
	if req.VlanID < 1 || req.VlanID > 4094 {
		respondErrorSimple(w, "VLAN ID must be 1-4094", http.StatusBadRequest)
		return
	}
	if req.IP != "" {
		if _, _, err := net.ParseCIDR(req.IP); err != nil {
			respondErrorSimple(w, "Invalid IP: use address/prefix, e.g. 10.0.100.1/24", http.StatusBadRequest)
			return
		}
	}
	name := fmt.Sprintf("%s.%d", req.Parent, req.VlanID)
	if len(name) > 15 {
		respondErrorSimple(w, "The VLAN name "+name+" is longer than 15 characters", http.StatusBadRequest)
		return
	}
	changeNetwork(w, r, "VLAN "+name, func(s *networkdwriter.State) error {
		if _, ok := s.Bonds[req.Parent]; !ok && !linkExists(req.Parent) {
			return fmt.Errorf("parent interface %s does not exist", req.Parent)
		}
		if _, ok := s.VLANs[name]; ok || linkExists(name) {
			return fmt.Errorf("%s exists already", name)
		}
		// The parent keeps the address it has: its file gets the VLAN line.
		if err := adopt(s, req.Parent); err != nil {
			return err
		}
		s.VLANs[name] = networkdwriter.VLAN{Parent: req.Parent, ID: req.VlanID}
		if req.IP != "" || req.DHCP {
			ifc := networkdwriter.Iface{DHCP: req.DHCP}
			if req.IP != "" {
				ifc.Addresses = []string{req.IP}
			}
			s.Interfaces[name] = ifc
		}
		return nil
	})
}

// DeleteVLAN removes a VLAN interface.
// DELETE /api/network/vlan { "interface": "eth0.100" }
func DeleteVLAN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Interface string `json:"interface"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	changeNetwork(w, r, "delete VLAN "+req.Interface, func(s *networkdwriter.State) error {
		if _, ok := s.VLANs[req.Interface]; !ok {
			return fmt.Errorf("%s was not created here (it may come from the system configuration)", req.Interface)
		}
		delete(s.VLANs, req.Interface)
		delete(s.Interfaces, req.Interface)
		return nil
	})
}

// ListVLANs lists VLAN interfaces.
// GET /api/network/vlan
func ListVLANs(w http.ResponseWriter, r *http.Request) {
	var st networkdwriter.State
	if NetWriter != nil {
		st = NetWriter.State()
	}
	ifaces, _ := net.Interfaces()
	vlans := make([]map[string]any, 0)
	seen := map[string]bool{}
	for _, ifc := range ifaces {
		if _, err := os.Stat("/proc/net/vlan/" + ifc.Name); err != nil && !strings.Contains(ifc.Name, ".") {
			continue
		}
		seen[ifc.Name] = true
		v, managed := st.VLANs[ifc.Name]
		e := map[string]any{"name": ifc.Name, "index": ifc.Index, "flags": ifc.Flags.String(), "managed": managed, "up": ifc.Flags&net.FlagUp != 0}
		if managed {
			e["parent"], e["vlan_id"] = v.Parent, v.ID
		}
		vlans = append(vlans, e)
	}
	for n, v := range st.VLANs { // configured, not (yet) present
		if !seen[n] {
			vlans = append(vlans, map[string]any{"name": n, "parent": v.Parent, "vlan_id": v.ID, "managed": true, "up": false, "flags": ""})
		}
	}
	respondOK(w, map[string]any{"success": true, "vlans": vlans})
}

// ── Bonds ─────────────────────────────────────────────────────────────────────

// CreateBond creates a bond.
// POST /api/network/bond { "name": "bond0", "slaves": ["eth0","eth1"], "mode": "802.3ad", "ip": "", "dhcp": false }
// Without an address the bond takes over the address configuration of its
// first member that has one (the members themselves carry none any more).
func CreateBond(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string   `json:"name"`
		Slaves []string `json:"slaves"`
		Mode   string   `json:"mode"`
		IP     string   `json:"ip"`
		DHCP   bool     `json:"dhcp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := networkdwriter.ValidateIface(req.Name); err != nil {
		respondErrorSimple(w, "Invalid bond name", http.StatusBadRequest)
		return
	}
	if len(req.Slaves) == 0 {
		respondErrorSimple(w, "A bond needs at least one interface", http.StatusBadRequest)
		return
	}
	if req.IP != "" {
		if _, _, err := net.ParseCIDR(req.IP); err != nil {
			respondErrorSimple(w, "Invalid IP: use address/prefix, e.g. 192.168.1.10/24", http.StatusBadRequest)
			return
		}
	}
	changeNetwork(w, r, "bond "+req.Name+" ("+strings.Join(req.Slaves, ", ")+")", func(s *networkdwriter.State) error {
		if _, ok := s.Bonds[req.Name]; ok || linkExists(req.Name) {
			return fmt.Errorf("%s exists already", req.Name)
		}
		var moved *networkdwriter.Iface
		for _, sl := range req.Slaves {
			if err := networkdwriter.ValidateIface(sl); err != nil {
				return err
			}
			if !linkExists(sl) {
				return fmt.Errorf("interface %s does not exist", sl)
			}
			if err := adopt(s, sl); err != nil {
				return err
			}
			if ifc, ok := s.Interfaces[sl]; ok {
				if moved == nil && (ifc.DHCP || len(ifc.Addresses) > 0) {
					c := ifc
					moved = &c
				}
				delete(s.Interfaces, sl)
			}
		}
		s.Bonds[req.Name] = networkdwriter.Bond{Mode: req.Mode, Slaves: req.Slaves}
		switch {
		case req.IP != "" || req.DHCP:
			ifc := networkdwriter.Iface{DHCP: req.DHCP}
			if req.IP != "" {
				ifc.Addresses = []string{req.IP}
			}
			s.Interfaces[req.Name] = ifc
		case moved != nil:
			s.Interfaces[req.Name] = *moved
		}
		return nil
	})
}

// DeleteBond removes a bond; its members go back to the system configuration.
// DELETE /api/network/bond/{name}
func DeleteBond(w http.ResponseWriter, r *http.Request) {
	name := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/network/bond/"), "/")[0]
	changeNetwork(w, r, "delete bond "+name, func(s *networkdwriter.State) error {
		if _, ok := s.Bonds[name]; !ok {
			return fmt.Errorf("bond %s was not created here (it may come from the system configuration)", name)
		}
		for n, v := range s.VLANs {
			if v.Parent == name {
				return fmt.Errorf("delete VLAN %s on %s first", n, name)
			}
		}
		delete(s.Bonds, name)
		delete(s.Interfaces, name)
		return nil
	})
}

// ListBonds lists bond interfaces present on the system.
// GET /api/network/bond
func ListBonds(w http.ResponseWriter, r *http.Request) {
	var st networkdwriter.State
	if NetWriter != nil {
		st = NetWriter.State()
	}
	type bondInfo struct {
		Name    string   `json:"name"`
		Slaves  []string `json:"slaves"`
		Mode    string   `json:"mode"`
		State   string   `json:"state"`
		Managed bool     `json:"managed"`
	}
	bonds := []bondInfo{}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		dir := "/sys/class/net/" + ifc.Name + "/bonding/"
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		b := bondInfo{Name: ifc.Name, State: "down"}
		if ifc.Flags&net.FlagUp != 0 {
			b.State = "up"
		}
		if v, err := os.ReadFile(dir + "slaves"); err == nil {
			b.Slaves = strings.Fields(string(v))
		}
		if v, err := os.ReadFile(dir + "mode"); err == nil {
			b.Mode = strings.Fields(string(v) + " ")[0]
		}
		_, b.Managed = st.Bonds[ifc.Name]
		bonds = append(bonds, b)
	}
	respondOK(w, map[string]any{"success": true, "bonds": bonds})
}
