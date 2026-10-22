package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/netlinkx"
	"dplaned/internal/networkdwriter"
	"dplaned/internal/security"
)

type SystemHandler struct{}

func NewSystemHandler() *SystemHandler {
	return &SystemHandler{}
}

func (h *SystemHandler) GetUPSStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if !security.IsValidSessionToken(sessionID) {
		audit.LogSecurityEvent("Invalid session token", user, r.RemoteAddr)
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Check if upsc exists
	start := time.Now()
	_, err := exec.LookPath("upsc")
	if err != nil {
		duration := time.Since(start)
		audit.LogCommand(audit.LevelInfo, user, "upsc_check", nil, false, duration, err)
		respondOK(w, CommandResponse{
			Success:  false,
			Error:    "NUT not installed",
			Duration: duration.Milliseconds(),
		})
		return
	}

	// Get UPS list
	output, err := executeCommand("upsc", []string{"-l"})
	if err != nil || strings.TrimSpace(output) == "" {
		duration := time.Since(start)
		audit.LogCommand(audit.LevelInfo, user, "upsc_list", nil, false, duration, err)
		respondOK(w, CommandResponse{
			Success:  false,
			Error:    "No UPS found",
			Duration: duration.Milliseconds(),
		})
		return
	}

	upsName := strings.TrimSpace(strings.Split(output, "\n")[0])
	output, err = executeCommand("upsc", []string{upsName})
	duration := time.Since(start)

	audit.LogCommand(audit.LevelInfo, user, "upsc_query", []string{upsName}, err == nil, duration, err)

	if err != nil {
		respondOK(w, CommandResponse{
			Success:  false,
			Error:    "Failed to read UPS",
			Duration: duration.Milliseconds(),
		})
		return
	}

	upsData := parseUPSData(output)
	respondOK(w, CommandResponse{
		Success: true,
		Data: map[string]any{
			"battery_charge":  getUPSValue(upsData, "battery.charge", "N/A") + "%",
			"battery_runtime": getUPSValue(upsData, "battery.runtime", "N/A") + " sec",
			"status":          getUPSValue(upsData, "ups.status", "Unknown"),
			"model":           getUPSValue(upsData, "ups.model", "Unknown"),
			"manufacturer":    getUPSValue(upsData, "ups.mfr", "Unknown"),
			"serial":          getUPSValue(upsData, "ups.serial", "Unknown"),
			"load":            getUPSValue(upsData, "ups.load", "0"),
			"input_voltage":   getUPSValue(upsData, "input.voltage", "0"),
			"output_voltage":  getUPSValue(upsData, "output.voltage", "0"),
		},
		Duration: duration.Milliseconds(),
	})
}

// SaveUPSConfig handles POST /api/system/ups
// Persists UPS shutdown action and thresholds to /etc/nut/upsmon.conf
func (h *SystemHandler) SaveUPSConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if !security.IsValidSessionToken(sessionID) {
		audit.LogSecurityEvent("Invalid session token", user, r.RemoteAddr)
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Action    string `json:"action"`    // "shutdown" (hibernation is impossible with ZFS: nohibernate)
		Threshold int    `json:"threshold"` // battery % to trigger
		Grace     int    `json:"grace"`     // seconds grace period
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate
	if req.Action == "" {
		req.Action = "shutdown"
	}
	if req.Action != "shutdown" {
		respondErrorSimple(w, "The only UPS action is shutdown: hibernation is not possible with ZFS (the system boots with nohibernate, so a hibernate would leave the machine running until the battery is empty)", http.StatusBadRequest)
		return
	}
	if req.Threshold < 1 || req.Threshold > 99 {
		req.Threshold = 20
	}
	if req.Grace < 0 || req.Grace > 600 {
		req.Grace = 30
	}

	// NixOS: NUT is configured by services.dplaneos.ups; the policy goes into
	// the bridge (lowBattery, finalDelay, action) and applies on rebuild.
	if onNixOS() {
		if err := NixWriter.SetUPSPolicy(req.Action, req.Threshold, req.Grace); err != nil {
			respondErrorSimple(w, "Cannot save UPS policy: "+err.Error(), http.StatusBadRequest)
			return
		}
		audit.LogActivity(user, "ups_config_save", map[string]any{
			"action": req.Action, "threshold": req.Threshold, "grace": req.Grace,
		})
		respondOK(w, CommandResponse{Success: true, Output: "UPS policy saved; applies after the pending NixOS changes are applied"})
		return
	}

	// Write upsmon.conf snippet - create/overwrite dplaned section
	conf := fmt.Sprintf(
		"# DPlaneOS UPS config - do not edit this block manually\n"+
			"MINSUPPLIES 1\n"+
			"SHUTDOWNCMD \"/sbin/shutdown -h now\"\n"+
			"NOTIFYCMD upssched\n"+
			"POLLFREQ 5\n"+
			"POLLFREQALERT 5\n"+
			"HOSTSYNC 15\n"+
			"DEADTIME 15\n"+
			"RBWARNTIME 43200\n"+
			"NOCOMMWARNTIME 300\n"+
			"FINALDELAY %d\n",
		req.Grace,
	)

	confPath := "/etc/nut/upsmon.d/dplaneos.conf"
	if err := os.MkdirAll("/etc/nut/upsmon.d", 0750); err != nil {
		respondErrorSimple(w, "Cannot create config dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(confPath, []byte(conf), 0640); err != nil {
		respondErrorSimple(w, "Cannot write config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	audit.LogActivity(user, "ups_config_save", map[string]any{
		"action":    req.Action,
		"threshold": req.Threshold,
		"grace":     req.Grace,
	})

	respondOK(w, CommandResponse{Success: true, Output: "UPS config saved"})
}

// GetUPSPolicy returns the saved shutdown policy (the UPS page edits it).
// GET /api/system/ups/policy
// The NUT connection itself (driver, port, UPS name) comes from the NixOS
// configuration (services.dplaneos.ups), not from this API.
func (h *SystemHandler) GetUPSPolicy(w http.ResponseWriter, r *http.Request) {
	action, threshold, grace := "shutdown", 20, 30
	if onNixOS() && NixWriter != nil {
		st := NixWriter.State() // the action is always shutdown (hibernate in older states too)
		if st.UPSLowBattery > 0 {
			threshold = st.UPSLowBattery
		}
		if st.UPSFinalDelay > 0 {
			grace = st.UPSFinalDelay
		}
	}
	respondOK(w, map[string]any{
		"success":   true,
		"action":    action,
		"threshold": threshold,
		"grace":     grace,
	})
}

func (h *SystemHandler) HandleNetwork(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.handleNetworkGet(w, r, r.Header.Get("X-User"))
		return
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		user := r.Header.Get("X-User")
		sessionID := r.Header.Get("X-Session-ID")
		if !security.IsValidSessionToken(sessionID) {
			audit.LogSecurityEvent("Invalid session token", user, r.RemoteAddr)
			respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		h.handleNetworkPost(w, r, user)
		return
	}
	respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (h *SystemHandler) handleNetworkGet(w http.ResponseWriter, _ *http.Request, user string) {
	start := time.Now()

	// Get addresses via netlinkx (reads /proc/net - no exec, no injection surface)
	addrs, addrErr := netlinkx.AddrList("")
	duration := time.Since(start)
	audit.LogCommand(audit.LevelInfo, user, "ip_addr", nil, addrErr == nil, duration, addrErr)
	if addrErr != nil {
		respondOK(w, map[string]any{"success": false, "error": addrErr.Error(), "duration_ms": duration.Milliseconds()})
		return
	}

	_ = addrs // address list validated above; per-interface data built below
	var interfaces []map[string]any

	// Get routes via netlinkx (reads /proc/net/route - no exec)
	nlRoutes, routeErr := netlinkx.RouteList()
	routes := make([]map[string]any, 0, len(nlRoutes))
	if routeErr != nil {
		log.Printf("failed to read routes: %v", routeErr)
	} else {
		for _, rt := range nlRoutes {
			routes = append(routes, map[string]any{
				"dst": rt.Dst.String(),
				"via": rt.Gateway.String(),
				"dev": rt.Iface,
			})
		}
	}

	defaultGW := map[string]string{}
	for _, rt := range nlRoutes {
		if rt.Dst == nil || rt.Dst.String() == "0.0.0.0/0" {
			defaultGW[rt.Iface] = rt.Gateway.String()
		}
	}
	interfaces = networkInterfaces(defaultGW)

	dns := map[string]any{"nameservers": []string{}, "search": []string{}}
	// With systemd-resolved /etc/resolv.conf only names the local stub
	// (127.0.0.53); the servers in use are in resolved's own file.
	resolvConf := "/etc/resolv.conf"
	if _, err := os.Stat("/run/systemd/resolve/resolv.conf"); err == nil {
		resolvConf = "/run/systemd/resolve/resolv.conf"
	}
	if NetWriter != nil {
		if d := NetWriter.State().DNS; len(d.Servers) > 0 {
			dns["configured"] = d
		}
	}
	if content, err := os.ReadFile(resolvConf); err == nil {
		for line := range strings.SplitSeq(string(content), "\n") {
			line = strings.TrimSpace(line)
			if s, ok := strings.CutPrefix(line, "nameserver "); ok {
				dns["nameservers"] = append(dns["nameservers"].([]string), strings.TrimSpace(s))
			} else if s, ok := strings.CutPrefix(line, "search "); ok {
				dns["search"] = strings.Fields(strings.TrimSpace(s))
			}
		}
	}

	respondOK(w, map[string]any{
		"success":     true,
		"interfaces":  interfaces,
		"routes":      routes,
		"dns":         dns,
		"duration_ms": duration.Milliseconds(),
	})
}

var ifaceRe = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,32}$`)

func isValidIPOrCIDR(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	if strings.Contains(v, "/") {
		_, _, err := net.ParseCIDR(v)
		return err == nil
	}
	return net.ParseIP(v) != nil
}

func bitsPop(b byte) int {
	count := 0
	for b != 0 {
		count += int(b & 1)
		b >>= 1
	}
	return count
}

// netmaskToCIDR converts a dotted-quad netmask to CIDR prefix length.
// Returns -1 on invalid input.
func netmaskToCIDR(mask string) int {
	parts := strings.Split(mask, ".")
	if len(parts) != 4 {
		return -1
	}
	prefix := 0
	seenZero := false
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return -1
		}
		b := byte(n)
		if seenZero && b != 0 {
			return -1
		}
		bits := bitsPop(b)
		prefix += bits
		if bits != 8 {
			// validate it's a contiguous mask octet
			if b != 0 && ((^b)+1)&^b != 0 {
				return -1
			}
			seenZero = true
		}
	}
	return prefix
}

func (h *SystemHandler) handleNetworkPost(w http.ResponseWriter, r *http.Request, user string) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	switch action, _ := req["action"].(string); action {
	case "configure":
		configureInterface(w, r, req)
	case "add", "delete":
		changeRoute(w, r, action, req)
	case "set_dns":
		setDNS(w, r, req)
	default:
		respondErrorSimple(w, "Unsupported network action", http.StatusBadRequest)
	}
}

func (h *SystemHandler) GetSystemLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if !security.IsValidSessionToken(sessionID) {
		audit.LogSecurityEvent("Invalid session token", user, r.RemoteAddr)
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	limit := r.URL.Query().Get("limit")
	if limit == "" {
		limit = "100"
	}

	start := time.Now()
	output, err := executeCommand("journalctl", []string{"-n", limit, "--no-pager", "-o", "json"})
	duration := time.Since(start)

	audit.LogCommand(audit.LevelInfo, user, "journalctl", []string{limit}, err == nil, duration, err)

	if err != nil {
		respondOK(w, CommandResponse{
			Success:  false,
			Error:    err.Error(),
			Duration: duration.Milliseconds(),
		})
		return
	}

	logs := parseJournalLogs(output)
	respondOK(w, CommandResponse{
		Success:  true,
		Data:     logs,
		Duration: duration.Milliseconds(),
	})
}

// Reboot triggers a system reboot after a 2-second delay
// POST /api/system/reboot
func (h *SystemHandler) Reboot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := r.Header.Get("X-User")
	audit.LogActivity(user, "system_reboot", nil)

	respondOK(w, map[string]any{
		"success": true,
		"message": "System rebooting in 2 seconds...",
	})

	go func() {
		time.Sleep(2 * time.Second)
		_, _ = cmdutil.Run(cmdutil.TimeoutFast, "systemctl_reboot", "reboot")
	}()
}

// Poweroff triggers a system poweroff after a 2-second delay
// POST /api/system/poweroff
func (h *SystemHandler) Poweroff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := r.Header.Get("X-User")
	audit.LogActivity(user, "system_poweroff", nil)

	respondOK(w, map[string]any{
		"success": true,
		"message": "System powering off in 2 seconds...",
	})

	go func() {
		time.Sleep(2 * time.Second)
		_, _ = cmdutil.Run(cmdutil.TimeoutFast, "systemctl_poweroff", "poweroff")
	}()
}

func (h *SystemHandler) RunDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Type   string `json:"type"`   // "ping", "dns", "traceroute"
		Target string `json:"target"` // hostname or IP
		Count  int    `json:"count"`  // for ping
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// === STRICT VALIDATION ===
	// Only allow alphanumeric, dots, and dashes for target to prevent injection.
	targetRe := regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)
	if !targetRe.MatchString(req.Target) || len(req.Target) > 255 {
		respondErrorSimple(w, "Invalid target format", http.StatusBadRequest)
		return
	}

	if req.Count <= 0 || req.Count > 10 {
		req.Count = 4
	}

	var name string
	var args []string
	switch req.Type {
	case "ping":
		name = "ping"
		args = []string{"-c", strconv.Itoa(req.Count), req.Target}
	case "dns":
		name = "dig"
		args = []string{"+short", req.Target}
		if strings.Contains(req.Target, ":") {
			args = []string{"-x", req.Target}
		}
	case "traceroute":
		name = "traceroute"
		args = []string{"-m", "20", "-w", "2", req.Target}
	default:
		respondErrorSimple(w, "Unsupported diagnostic type", http.StatusBadRequest)
		return
	}

	// Finding 23: Force validation against secure whitelist
	if err := security.ValidateCommand(name, args); err != nil {
		respondErrorSimple(w, "Prohibited diagnostic: "+err.Error(), http.StatusForbidden)
		return
	}

	cmdEntry := security.CommandWhitelist[name]
	cmd := exec.Command(cmdEntry.Path, args...)

	start := time.Now()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		respondErrorSimple(w, "Failed to create stdout pipe", http.StatusInternalServerError)
		return
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		respondErrorSimple(w, "Failed to start command: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Set headers for streaming
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		// Fallback to non-streaming if flusher not supported (unlikely in this environment)
		out, _ := io.ReadAll(stdout)
		_ = cmd.Wait()
		w.Write(out)
		return
	}

	// Use a scanner to read line by line
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		fmt.Fprintf(w, "%s\n", scanner.Text())
		flusher.Flush()
	}

	if err := cmd.Wait(); err != nil {
		fmt.Fprintf(w, "\n--- Command failed: %v ---\n", err)
	}

	duration := time.Since(start)
	user := r.Header.Get("X-User")
	audit.LogCommand(audit.LevelInfo, user, "network_diagnostic_stream", []string{req.Type, req.Target}, err == nil, duration, err)
}

// Helper functions

func parseUPSData(output string) map[string]string {
	data := make(map[string]string)
	for line := range strings.SplitSeq(output, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			data[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return data
}

func getUPSValue(data map[string]string, key, defaultValue string) string {
	if val, ok := data[key]; ok {
		return val
	}
	return defaultValue
}

func parseJournalLogs(output string) []map[string]any {
	var logs []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var logEntry map[string]any
		if err := json.Unmarshal([]byte(line), &logEntry); err != nil {
			continue
		}
		entry := map[string]any{
			"time":    fmt.Sprintf("%v", logEntry["__REALTIME_TIMESTAMP"]),
			"message": fmt.Sprintf("%v", logEntry["MESSAGE"]),
			"unit":    fmt.Sprintf("%v", logEntry["_SYSTEMD_UNIT"]),
		}
		if priority, ok := logEntry["PRIORITY"].(float64); ok {
			level := "info"
			if priority <= 3 {
				level = "error"
			} else if priority == 4 {
				level = "warning"
			}
			entry["level"] = level
		}
		logs = append(logs, entry)
	}
	return logs
}

// networkInterfaces lists one entry per interface with the fields the Network
// page uses (name, ip, netmask, mac, mtu, up, state, type, speed, gateway,
// primary). The previous response only had {addr, cidr} per address, so the
// page had no interface names and its VLAN/bond tabs crashed.
func networkInterfaces(defaultGW map[string]string) []map[string]any {
	ifaces, err := net.Interfaces()
	if err != nil {
		return []map[string]any{}
	}
	sysRead := func(name, attr string) string {
		b, err := os.ReadFile("/sys/class/net/" + name + "/" + attr)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	sysHas := func(name, sub string) bool {
		_, err := os.Stat("/sys/class/net/" + name + "/" + sub)
		return err == nil
	}
	var st networkdwriter.State
	var dhcpLive map[string]bool
	if NetWriter != nil {
		st, dhcpLive = NetWriter.State(), NetWriter.LiveDHCP()
	}
	out := make([]map[string]any, 0, len(ifaces))
	for _, ifc := range ifaces {
		e := map[string]any{
			"name": ifc.Name,
			"mtu":  ifc.MTU,
			"mac":  ifc.HardwareAddr.String(),
			"up":   ifc.Flags&net.FlagUp != 0,
		}
		switch {
		case ifc.Flags&net.FlagLoopback != 0:
			e["type"] = "loopback"
		case strings.Contains(ifc.Name, "."):
			e["type"] = "vlan"
		case sysHas(ifc.Name, "bonding"):
			e["type"] = "bond"
		case sysHas(ifc.Name, "bridge"):
			e["type"] = "bridge"
		default:
			e["type"] = "ethernet"
		}
		if st := sysRead(ifc.Name, "operstate"); st != "" {
			e["state"] = st
		}
		if sp, err := strconv.Atoi(sysRead(ifc.Name, "speed")); err == nil && sp > 0 {
			e["speed"] = fmt.Sprintf("%d Mb/s", sp)
		}
		if addrs, err := ifc.Addrs(); err == nil {
			for _, a := range addrs {
				ipnet, ok := a.(*net.IPNet)
				if !ok || ipnet.IP.To4() == nil {
					continue
				}
				e["ip"] = ipnet.IP.String()
				e["netmask"] = net.IP(ipnet.Mask).String()
				e["addr"] = ipnet.IP.String()
				e["cidr"] = ipnet.String()
				break
			}
		}
		if gw, ok := defaultGW[ifc.Name]; ok {
			e["gateway"] = gw
			e["primary"] = true
		}
		// How it is configured: from the DPlaneOS configuration when it has
		// the interface, else from the live addresses (dynamic = DHCP). The
		// page preselects DHCP from this; without it a static interface
		// opened with DHCP ticked and saving switched it to DHCP.
		if master, err := os.Readlink("/sys/class/net/" + ifc.Name + "/master"); err == nil {
			e["member_of"] = filepath.Base(master)
		}
		if NetWriter != nil {
			if c, ok := st.Interfaces[ifc.Name]; ok {
				e["managed"], e["dhcp"] = true, c.DHCP
				if c.Gateway != "" {
					e["configured_gateway"] = c.Gateway
				}
			} else if st.Mentions(ifc.Name) {
				e["managed"], e["dhcp"] = true, false
			} else {
				e["managed"], e["dhcp"] = false, dhcpLive[ifc.Name]
			}
		}
		out = append(out, e)
	}
	return out
}
