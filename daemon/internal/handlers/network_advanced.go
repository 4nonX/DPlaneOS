package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// ═══════════════════════════════════════════════════════════════
//  2+3. SMB VFS MODULE SUPPORT + EXTRA PARAMETERS
// ═══════════════════════════════════════════════════════════════

// SMBGlobalConfig represents configurable global SMB settings
type SMBGlobalConfig struct {
	Workgroup    string `json:"workgroup"`
	ServerString string `json:"server_string"`
	TimeMachine  bool   `json:"time_machine"` // enables vfs_fruit globally
	ShadowCopy   bool   `json:"shadow_copy"`  // enables vfs_shadow_copy2
	RecycleBin   bool   `json:"recycle_bin"`  // enables vfs_recycle
	ExtraGlobal  string `json:"extra_global"` // custom global params
}

// ═══════════════════════════════════════════════════════════════
//  13. NTP CONFIGURATION
// ═══════════════════════════════════════════════════════════════

// GetNTPStatus returns current NTP synchronization status
// GET /api/system/ntp
func GetNTPStatus(w http.ResponseWriter, r *http.Request) {
	// Try timedatectl first (systemd)
	output, err := executeCommandWithTimeout(TimeoutFast, "timedatectl", []string{"show"})
	if err != nil {
		// Fallback: chronyc
		output, err = executeCommandWithTimeout(TimeoutFast, "chronyc", []string{"tracking"})
		if err != nil {
			respondOK(w, map[string]any{
				"success": true,
				"synced":  false,
				"servers": configuredNTPServers(),
				"error":   "Cannot query NTP status",
			})
			return
		}
	}

	synced := strings.Contains(output, "NTPSynchronized=yes") ||
		strings.Contains(output, "Leap status     : Normal")

	respondOK(w, map[string]any{
		"success": true,
		"synced":  synced,
		"servers": configuredNTPServers(),
		"details": strings.TrimSpace(output),
	})
}

// configuredNTPServers: the servers set on the Network page (NixOS state),
// else the NTP= line of timesyncd.conf.
func configuredNTPServers() []string {
	if onNixOS() {
		if s := NixWriter.State().NTPServers; s != nil {
			return s
		}
		return []string{}
	}
	b, _ := os.ReadFile("/etc/systemd/timesyncd.conf")
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "NTP="); ok {
			return strings.Fields(v)
		}
	}
	return []string{}
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

	// On NixOS timesyncd is enabled declaratively; timedatectl set-ntp would try
	// to enable the unit in the read-only /etc.
	if !onNixOS() {
		if out, err := executeCommandWithTimeout(TimeoutFast, "timedatectl", []string{"set-ntp", "true"}); err != nil {
			respondOK(w, map[string]any{"success": false, "error": "timedatectl set-ntp: " + strings.TrimSpace(out) + " " + err.Error()})
			return
		}
	}

	// Write the NTP server list to systemd-timesyncd.conf (on NixOS the file
	// is generated; persistNTP records it for the next rebuild instead).
	if !onNixOS() {
		conf := "[Time]\nNTP=" + strings.Join(req.Servers, " ") + "\n"
		if err := os.WriteFile("/etc/systemd/timesyncd.conf", []byte(conf), 0644); err != nil {
			respondOK(w, map[string]any{"success": false, "error": "write timesyncd.conf: " + err.Error()})
			return
		}
		if out, err := executeCommandWithTimeout(TimeoutMedium, "systemctl", []string{"restart", "systemd-timesyncd"}); err != nil {
			respondOK(w, map[string]any{"success": false, "error": "restart systemd-timesyncd: " + strings.TrimSpace(out) + " " + err.Error()})
			return
		}
	}

	// Persist to Nix fragment (NixOS: networking.timeServers)
	if err := persistNTP(req.Servers); err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}

	respondOK(w, map[string]any{
		"success": true,
		"servers": req.Servers,
	})
}
