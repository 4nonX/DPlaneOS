package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/netlinkx"
	"dplaned/internal/security"
)

type SystemHandler struct{}

func NewSystemHandler() *SystemHandler {
	return &SystemHandler{}
}

func (h *SystemHandler) GetUPSStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
	output, err := executeCommand("/usr/bin/upsc", []string{"-l"})
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
	output, err = executeCommand("/usr/bin/upsc", []string{upsName})
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
		Data: map[string]interface{}{
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
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
		Action    string `json:"action"`    // "shutdown" or "hibernate"
		Threshold int    `json:"threshold"` // battery % to trigger
		Grace     int    `json:"grace"`     // seconds grace period
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate
	if req.Action != "shutdown" && req.Action != "hibernate" {
		req.Action = "shutdown"
	}
	if req.Threshold < 1 || req.Threshold > 99 {
		req.Threshold = 20
	}
	if req.Grace < 0 || req.Grace > 600 {
		req.Grace = 30
	}

	// Write upsmon.conf snippet — create/overwrite dplaned section
	conf := fmt.Sprintf(
		"# D-PlaneOS UPS config — do not edit this block manually\n"+
			"MINSUPPLIES 1\n"+
			"SHUTDOWNCMD \"/sbin/%s -h now\"\n"+
			"NOTIFYCMD /usr/sbin/upssched\n"+
			"POLLFREQ 5\n"+
			"POLLFREQALERT 5\n"+
			"HOSTSYNC 15\n"+
			"DEADTIME 15\n"+
			"RBWARNTIME 43200\n"+
			"NOCOMMWARNTIME 300\n"+
			"FINALDELAY %d\n",
		req.Action, req.Grace,
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

	audit.LogActivity(user, "ups_config_save", map[string]interface{}{
		"action":    req.Action,
		"threshold": req.Threshold,
		"grace":     req.Grace,
	})

	respondOK(w, CommandResponse{Success: true, Output: "UPS config saved"})
}

func (h *SystemHandler) GetNetworkInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.handleNetworkGet(w, r, r.Header.Get("X-User"))
		return
	}
	if r.Method == http.MethodPost {
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
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (h *SystemHandler) handleNetworkGet(w http.ResponseWriter, r *http.Request, user string) {
	start := time.Now()

	// Get addresses via netlinkx (reads /proc/net — no exec, no injection surface)
	addrs, addrErr := netlinkx.AddrList("")
	duration := time.Since(start)
	audit.LogCommand(audit.LevelInfo, user, "ip_addr", nil, addrErr == nil, duration, addrErr)
	if addrErr != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": addrErr.Error(), "duration_ms": duration.Milliseconds()})
		return
	}

	// Convert to map slice expected by frontend
	interfaces := make([]map[string]interface{}, 0, len(addrs))
	for _, a := range addrs {
		interfaces = append(interfaces, map[string]interface{}{
			"addr":  a.IP.String(),
			"cidr":  a.CIDR.String(),
		})
	}

	// Get routes via netlinkx (reads /proc/net/route — no exec)
	nlRoutes, routeErr := netlinkx.RouteList()
	routes := make([]map[string]interface{}, 0, len(nlRoutes))
	if routeErr != nil {
		log.Printf("failed to read routes: %v", routeErr)
	} else {
		for _, rt := range nlRoutes {
			routes = append(routes, map[string]interface{}{
				"dst":  rt.Dst.String(),
				"via":  rt.Gateway.String(),
				"dev":  rt.Iface,
			})
		}
	}

	dns := map[string]interface{}{"nameservers": []string{}, "search": []string{}}
	if content, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "nameserver ") {
				dns["nameservers"] = append(dns["nameservers"].([]string), strings.TrimSpace(strings.TrimPrefix(line, "nameserver ")))
			} else if strings.HasPrefix(line, "search ") {
				dns["search"] = strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "search ")))
			}
		}
	}

	respondOK(w, map[string]interface{}{
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
	var req map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	action, _ := req["action"].(string)

	if action == "configure" {
		opStart := time.Now()
		iface, _ := req["interface"].(string)
		address, _ := req["address"].(string)
		if address == "" {
			address, _ = req["ip"].(string)
		}
		netmask, _ := req["netmask"].(string)
		gateway, _ := req["gateway"].(string)

		if !ifaceRe.MatchString(iface) || address == "" {
			respondErrorSimple(w, "Invalid network configuration", http.StatusBadRequest)
			return
		}

		// Convert netmask to CIDR if needed
		if !strings.Contains(address, "/") && netmask != "" {
			prefix := netmaskToCIDR(netmask)
			if prefix < 0 {
				respondErrorSimple(w, "Invalid netmask", http.StatusBadRequest)
				return
			}
			address += "/" + strconv.Itoa(prefix)
		}

		// Validate final address
		if !isValidIPOrCIDR(address) {
			respondErrorSimple(w, "Invalid network configuration", http.StatusBadRequest)
			return
		}

		// Use netlinkx.AddrReplace (atomic RTM_NEWADDR — no exec, no injection surface)
		err := netlinkx.AddrReplace(iface, address)
		if err == nil && gateway != "" {
			if !isValidIPOrCIDR(gateway) {
				respondErrorSimple(w, "Invalid gateway", http.StatusBadRequest)
				return
			}
			_ = netlinkx.RouteReplace("default", strings.Split(gateway, "/")[0], iface)
		}
		if err != nil {
			respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		audit.LogCommand(audit.LevelInfo, user, "network_configure", []string{iface, address}, true, time.Since(opStart), nil)
		// Persist to DB (boot reconciliation) and Nix fragment (NixOS: systemd.network)
		persistStaticIP(iface, address, strings.Split(gateway, "/")[0], nil)
		respondOK(w, map[string]interface{}{"success": true, "message": "Interface configured"})
		return
	}

	if action == "add" || action == "delete" {
		destination, _ := req["destination"].(string)
		gateway, _ := req["gateway"].(string)
		iface, _ := req["interface"].(string)
		if !isValidIPOrCIDR(destination) && destination != "default" {
			respondErrorSimple(w, "Invalid destination", http.StatusBadRequest)
			return
		}
		if gateway != "" && !isValidIPOrCIDR(gateway) {
			respondErrorSimple(w, "Invalid gateway", http.StatusBadRequest)
			return
		}
		var routeErr error
		gwStr := strings.Split(gateway, "/")[0]
		if ifaceRe.MatchString(iface) {
			routeErr = netlinkx.RouteReplace(destination, gwStr, iface)
		} else if gateway != "" {
			routeErr = netlinkx.RouteReplace(destination, gwStr, "")
		} else {
			routeErr = fmt.Errorf("interface or gateway required for route add")
		}
		if action == "delete" {
			// route del: fall back to exec — RTM_DELROUTE needs full route match
			// which requires knowing the exact table entry; exec is safer here
			delArgs := []string{"route", "del", destination}
			if gateway != "" {
				delArgs = append(delArgs, "via", gwStr)
			}
			_, routeErr = executeCommand("/usr/sbin/ip", delArgs)
		}
		if routeErr != nil {
			respondOK(w, map[string]interface{}{"success": false, "error": routeErr.Error()})
			return
		}
		respondOK(w, map[string]interface{}{"success": true})
		return
	}

	if action == "set_dns" {
		// { "action": "set_dns", "nameservers": ["1.1.1.1", "8.8.8.8"] }
		var servers []string
		if raw, ok := req["nameservers"].([]interface{}); ok {
			for _, s := range raw {
				if ip, ok := s.(string); ok && net.ParseIP(strings.TrimSpace(ip)) != nil {
					servers = append(servers, strings.TrimSpace(ip))
				}
			}
		}
		if len(servers) == 0 {
			respondErrorSimple(w, "At least one valid DNS server IP required", http.StatusBadRequest)
			return
		}
		// Write to resolv.conf (imperative — works on Debian/Ubuntu)
		var resolvConf strings.Builder
		resolvConf.WriteString("# Generated by D-PlaneOS\n")
		for _, s := range servers {
			resolvConf.WriteString("nameserver " + s + "\n")
		}
		if err := os.WriteFile("/etc/resolv.conf", []byte(resolvConf.String()), 0644); err != nil {
			log.Printf("WARN: write /etc/resolv.conf: %v", err)
			// Not fatal — DNS may still work via systemd-resolved
		}
		// Persist to Nix fragment (NixOS: networking.nameservers)
		persistDNS(servers)
		audit.LogCommand(audit.LevelInfo, user, "dns_set", servers, true, 0, nil)
		respondOK(w, map[string]interface{}{"success": true, "nameservers": servers})
		return
	}

	// DNS and VPN actions: return explicit not-implemented so frontends don't show false success
	if strings.HasPrefix(action, "add_") || strings.HasPrefix(action, "remove_") {
		respondErrorSimple(w, fmt.Sprintf("network action not implemented: %s", action), http.StatusNotImplemented)
		return
	}
	if action == "vpn" {
		respondErrorSimple(w, "network action not implemented: vpn", http.StatusNotImplemented)
		return
	}

	respondErrorSimple(w, "Unsupported network action", http.StatusBadRequest)
}

func (h *SystemHandler) GetSystemLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
	output, err := executeCommand("/usr/bin/journalctl", []string{"-n", limit, "--no-pager", "-o", "json"})
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

// Helper functions

func parseUPSData(output string) map[string]string {
	data := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
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

func parseJournalLogs(output string) []map[string]interface{} {
	var logs []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var logEntry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &logEntry); err != nil {
			continue
		}
		entry := map[string]interface{}{
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
