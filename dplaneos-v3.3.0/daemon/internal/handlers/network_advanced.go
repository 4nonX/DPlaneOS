package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"dplaned/internal/netlinkx"
)

// ═══════════════════════════════════════════════════════════════
//  2+3. SMB VFS MODULE SUPPORT + EXTRA PARAMETERS
// ═══════════════════════════════════════════════════════════════

// SMBGlobalConfig represents configurable global SMB settings
type SMBGlobalConfig struct {
	Workgroup      string `json:"workgroup"`
	ServerString   string `json:"server_string"`
	TimeMachine    bool   `json:"time_machine"`     // enables vfs_fruit globally
	ShadowCopy     bool   `json:"shadow_copy"`      // enables vfs_shadow_copy2
	RecycleBin     bool   `json:"recycle_bin"`       // enables vfs_recycle
	ExtraGlobal    string `json:"extra_global"`      // custom global params
}

// SMBShareVFS represents per-share VFS module options
type SMBShareVFS struct {
	ShareName      string `json:"share_name"`
	TimeMachine    bool   `json:"time_machine"`      // vfs_fruit per share
	ShadowCopy     bool   `json:"shadow_copy"`       // vfs_shadow_copy2 per share
	RecycleBin     bool   `json:"recycle_bin"`        // vfs_recycle per share
	RecycleMaxAge  int    `json:"recycle_max_age"`    // days (0=infinite)
	RecycleMaxSize int    `json:"recycle_max_size"`   // MB (0=infinite)
	ExtraParams    string `json:"extra_params"`       // custom per-share params
}

// GetSMBVFSConfig returns current VFS module configuration
// GET /api/smb/vfs
func GetSMBVFSConfig(w http.ResponseWriter, r *http.Request) {
	// Read current smb.conf and parse VFS settings
	output, err := executeCommandWithTimeout(TimeoutFast, "/bin/cat", []string{"/etc/samba/smb.conf"})
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":       true,
			"time_machine":  false,
			"shadow_copy":   false,
			"recycle_bin":   false,
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success":       true,
		"time_machine":  strings.Contains(output, "vfs_fruit"),
		"shadow_copy":   strings.Contains(output, "shadow_copy2"),
		"recycle_bin":   strings.Contains(output, "vfs_recycle"),
		"raw_config":    output,
	})
}

// SetSMBVFSConfig configures VFS modules for a share
// POST /api/smb/vfs
func SetSMBVFSConfig(w http.ResponseWriter, r *http.Request) {
	var req SMBShareVFS
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Build VFS objects list
	var vfsObjects []string
	var extraLines []string

	if req.TimeMachine {
		vfsObjects = append(vfsObjects, "catia", "fruit", "streams_xattr")
		extraLines = append(extraLines,
			"   fruit:metadata = stream",
			"   fruit:model = MacSamba",
			"   fruit:posix_rename = yes",
			"   fruit:veto_appledouble = no",
			"   fruit:nfs_aces = no",
			"   fruit:wipe_intentionally_left_blank_rfork = yes",
			"   fruit:delete_empty_adfiles = yes",
			"   fruit:time machine = yes",
		)
	}

	if req.ShadowCopy {
		vfsObjects = append(vfsObjects, "shadow_copy2")
		extraLines = append(extraLines,
			"   shadow:snapdir = .zfs/snapshot",
			"   shadow:sort = desc",
			"   shadow:format = %Y-%m-%d-%H%M%S",
		)
	}

	if req.RecycleBin {
		vfsObjects = append(vfsObjects, "recycle")
		extraLines = append(extraLines,
			"   recycle:repository = .recycle/%U",
			"   recycle:keeptree = yes",
			"   recycle:versions = yes",
			"   recycle:touch = yes",
			"   recycle:directory_mode = 0770",
		)
		if req.RecycleMaxAge > 0 {
			extraLines = append(extraLines,
				fmt.Sprintf("   recycle:maxage = %d", req.RecycleMaxAge),
			)
		}
		if req.RecycleMaxSize > 0 {
			extraLines = append(extraLines,
				fmt.Sprintf("   recycle:maxsize = %d", req.RecycleMaxSize*1024*1024),
			)
		}
	}

	if req.ExtraParams != "" {
		for _, line := range strings.Split(req.ExtraParams, "\n") {
			extraLines = append(extraLines, "   "+strings.TrimSpace(line))
		}
	}

	result := map[string]interface{}{
		"success":     true,
		"share":       req.ShareName,
		"vfs_objects": vfsObjects,
	}

	if len(vfsObjects) > 0 {
		result["vfs_line"] = fmt.Sprintf("   vfs objects = %s", strings.Join(vfsObjects, " "))
	}
	if len(extraLines) > 0 {
		result["extra_config"] = strings.Join(extraLines, "\n")
	}
	result["hint"] = "Add these lines to the share section in smb.conf, then reload"

	respondOK(w, result)
}

// ═══════════════════════════════════════════════════════════════
//  11. VLAN MANAGEMENT (802.1Q)
// ═══════════════════════════════════════════════════════════════

// CreateVLAN creates a VLAN interface
// POST /api/network/vlan { "parent": "eth0", "vlan_id": 100, "ip": "10.0.100.1/24" }
func CreateVLAN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Parent string `json:"parent"`  // eth0
		VlanID int    `json:"vlan_id"` // 1-4094
		IP     string `json:"ip"`      // 10.0.100.1/24 (optional)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.Parent, ";|&$`\\\"' /") || len(req.Parent) > 16 {
		respondErrorSimple(w, "Invalid parent interface", http.StatusBadRequest)
		return
	}
	if req.VlanID < 1 || req.VlanID > 4094 {
		respondErrorSimple(w, "VLAN ID must be 1-4094", http.StatusBadRequest)
		return
	}

	ifName := fmt.Sprintf("%s.%d", req.Parent, req.VlanID)

	// Create VLAN interface via netlink (RTM_NEWLINK — no exec, no injection surface)
	err := netlinkx.LinkAdd(netlinkx.LinkAttrs{
		Name:       ifName,
		Type:       netlinkx.LinkTypeVLAN,
		ParentName: req.Parent,
		VLANID:     req.VlanID,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	// Bring up
	_ = netlinkx.LinkSetUp(ifName)

	// Set IP if provided
	if req.IP != "" && !strings.ContainsAny(req.IP, ";|&$`\\\"'") {
		_ = netlinkx.AddrAdd(ifName, req.IP)
	}

	// Persist to DB and Nix fragment
	persistVLAN(ifName, req.Parent, req.VlanID)
	if req.IP != "" {
		persistStaticIP(ifName, req.IP, "", nil)
	}

	respondOK(w, map[string]interface{}{
		"success":   true,
		"interface": ifName,
		"vlan_id":   req.VlanID,
		"parent":    req.Parent,
		"ip":        req.IP,
	})
}

// DeleteVLAN removes a VLAN interface
// DELETE /api/network/vlan { "interface": "eth0.100" }
func DeleteVLAN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Interface string `json:"interface"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.Interface, ";|&$`\\\"' /") || !strings.Contains(req.Interface, ".") {
		respondErrorSimple(w, "Invalid VLAN interface", http.StatusBadRequest)
		return
	}
	if err := netlinkx.LinkDel(req.Interface); err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	persistVLANDelete(req.Interface)
	respondOK(w, map[string]interface{}{"success": true, "deleted": req.Interface})
}

// ListVLANs lists all VLAN interfaces
// GET /api/network/vlan
func ListVLANs(w http.ResponseWriter, r *http.Request) {
	links, err := netlinkx.LinkList()
	if err != nil {
		respondOK(w, map[string]interface{}{"success": true, "vlans": []interface{}{}})
		return
	}
	vlans := make([]map[string]interface{}, 0)
	for _, l := range links {
		// VLAN interfaces are conventionally named PARENT.VLANID (e.g. eth0.100)
		if strings.Contains(l.Name, ".") {
			vlans = append(vlans, map[string]interface{}{
				"name":  l.Name,
				"index": l.Index,
				"flags": l.Flags.String(),
			})
		}
	}
	respondOK(w, map[string]interface{}{"success": true, "vlans": vlans})
}

// ═══════════════════════════════════════════════════════════════
//  12. LINK AGGREGATION / BONDING (LACP)
// ═══════════════════════════════════════════════════════════════

// CreateBond creates a bonded interface
// POST /api/network/bond { "name": "bond0", "slaves": ["eth0","eth1"], "mode": "802.3ad" }
func CreateBond(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string   `json:"name"`   // bond0
		Slaves []string `json:"slaves"` // [eth0, eth1]
		Mode   string   `json:"mode"`   // balance-rr, active-backup, balance-xor, broadcast, 802.3ad, balance-tlb, balance-alb
		IP     string   `json:"ip"`     // optional
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.Name, ";|&$`\\\"' /") || len(req.Name) > 16 {
		respondErrorSimple(w, "Invalid bond name", http.StatusBadRequest)
		return
	}
	validModes := map[string]bool{
		"balance-rr": true, "active-backup": true, "balance-xor": true,
		"broadcast": true, "802.3ad": true, "balance-tlb": true, "balance-alb": true,
	}
	if !validModes[req.Mode] {
		respondErrorSimple(w, "Invalid bond mode", http.StatusBadRequest)
		return
	}

	// Create bond via netlink (RTM_NEWLINK — no exec, no injection surface)
	if err := netlinkx.LinkAdd(netlinkx.LinkAttrs{
		Name:     req.Name,
		Type:     netlinkx.LinkTypeBond,
		BondMode: req.Mode,
	}); err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	// Add slaves
	for _, slave := range req.Slaves {
		if strings.ContainsAny(slave, ";|&$`\\\"' /") {
			continue
		}
		_ = netlinkx.LinkSetDown(slave)
		_ = netlinkx.LinkSetMaster(slave, req.Name)
	}

	// Bring up
	_ = netlinkx.LinkSetUp(req.Name)

	if req.IP != "" && !strings.ContainsAny(req.IP, ";|&$`\\\"'") {
		_ = netlinkx.AddrAdd(req.Name, req.IP)
	}

	// Persist to DB (boot reconciliation) and Nix fragment (NixOS declarative)
	persistBond(req.Name, req.Slaves, req.Mode)
	if req.IP != "" {
		persistStaticIP(req.Name, req.IP, "", nil)
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"name":    req.Name,
		"mode":    req.Mode,
		"slaves":  req.Slaves,
	})
}

// ═══════════════════════════════════════════════════════════════
//  13. NTP CONFIGURATION
// ═══════════════════════════════════════════════════════════════

// GetNTPStatus returns current NTP synchronization status
// GET /api/system/ntp
func GetNTPStatus(w http.ResponseWriter, r *http.Request) {
	// Try timedatectl first (systemd)
	output, err := executeCommandWithTimeout(TimeoutFast, "/usr/bin/timedatectl", []string{"show"})
	if err != nil {
		// Fallback: chronyc
		output, err = executeCommandWithTimeout(TimeoutFast, "/usr/bin/chronyc", []string{"tracking"})
		if err != nil {
			respondOK(w, map[string]interface{}{
				"success": true,
				"synced":  false,
				"error":   "Cannot query NTP status",
			})
			return
		}
	}

	synced := strings.Contains(output, "NTPSynchronized=yes") ||
		strings.Contains(output, "Leap status     : Normal")

	respondOK(w, map[string]interface{}{
		"success": true,
		"synced":  synced,
		"details": strings.TrimSpace(output),
	})
}

// SetNTPServers configures NTP servers
// POST /api/system/ntp { "servers": ["0.pool.ntp.org", "1.pool.ntp.org"] }
func SetNTPServers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Servers []string `json:"servers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if len(req.Servers) == 0 {
		respondErrorSimple(w, "At least one NTP server required", http.StatusBadRequest)
		return
	}
	for _, s := range req.Servers {
		if strings.ContainsAny(s, ";|&$`\\\"'") || len(s) > 253 {
			respondErrorSimple(w, "Invalid server address", http.StatusBadRequest)
			return
		}
	}

	// Use timedatectl
	args := append([]string{"set-ntp", "true"}, req.Servers...)
	executeCommandWithTimeout(TimeoutFast, "/usr/bin/timedatectl", []string{"set-ntp", "true"})

	// Set servers via systemd-timesyncd config
	conf := "[Time]\n"
	conf += fmt.Sprintf("NTP=%s\n", strings.Join(req.Servers, " "))

	executeCommandWithTimeout(TimeoutFast, "/usr/bin/tee", []string{"/etc/systemd/timesyncd.conf"})

	// Restart timesyncd
	executeCommandWithTimeout(TimeoutMedium, "/usr/bin/systemctl", []string{"restart", "systemd-timesyncd"})

	// Persist to Nix fragment (NixOS: networking.timeServers)
	persistNTP(req.Servers)

	respondOK(w, map[string]interface{}{
		"success": true,
		"servers": req.Servers,
	})

	_ = args // suppress unused
}

// ListBonds lists all bonded interfaces currently present on the system.
// GET /api/network/bond
func ListBonds(w http.ResponseWriter, r *http.Request) {
	links, err := netlinkx.LinkList()
	if err != nil {
		http.Error(w, "failed to list interfaces: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type bondInfo struct {
		Name   string   `json:"name"`
		Slaves []string `json:"slaves"`
		State  string   `json:"state"`
	}

	// Find bond interfaces by type field
	bondNames := map[string]bool{}
	for _, l := range links {
		if l.Type == "bond" || strings.HasPrefix(l.Name, "bond") {
			bondNames[l.Name] = true
		}
	}

	// Find slaves by reading /sys/class/net/<iface>/master symlink
	bondSlaves := map[string][]string{}
	for _, l := range links {
		target, err2 := bondMasterOf(l.Name)
		if err2 == nil {
			parts := strings.Split(target, "/")
			master := parts[len(parts)-1]
			bondSlaves[master] = append(bondSlaves[master], l.Name)
			bondNames[master] = true
		}
	}

	var bonds []bondInfo
	for _, l := range links {
		if !bondNames[l.Name] {
			continue
		}
		state := "down"
		if l.Flags&0x1 != 0 { // net.FlagUp == 1
			state = "up"
		}
		bonds = append(bonds, bondInfo{
			Name:   l.Name,
			Slaves: bondSlaves[l.Name],
			State:  state,
		})
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"bonds":   bonds,
	})
}

// bondMasterOf reads /sys/class/net/<iface>/master symlink to find the bond it belongs to.
// Returns master interface name or error. Uses exec to avoid importing "os".
func bondMasterOf(ifaceName string) (string, error) {
	out, err := executeCommandWithTimeout(TimeoutFast, "/bin/readlink", []string{
		"/sys/class/net/" + ifaceName + "/master",
	})
	if err != nil {
		return "", err
	}
	target := strings.TrimSpace(out)
	parts := strings.Split(target, "/")
	return parts[len(parts)-1], nil
}

// DeleteBond removes a bonded interface.
// DELETE /api/network/bond/{name}
func DeleteBond(w http.ResponseWriter, r *http.Request) {
	// Extract name from path: /api/network/bond/{name}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/network/bond/"), "/")
	name := parts[0]

	if strings.ContainsAny(name, ";|&$`\\\"' /") || len(name) > 16 || name == "" {
		http.Error(w, "invalid bond name", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(name, "bond") {
		http.Error(w, "name must start with 'bond'", http.StatusBadRequest)
		return
	}

	// Bring down before deleting
	_ = netlinkx.LinkSetDown(name)

	if err := netlinkx.LinkDel(name); err != nil {
		http.Error(w, "failed to delete bond: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Remove from persistence layers
	persistBondDelete(name)

	respondOK(w, map[string]interface{}{
		"success": true,
		"message": "bond deleted",
	})
}
