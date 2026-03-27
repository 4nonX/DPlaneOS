package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/security"
)

// ADJoinRequest represents the request to join an AD domain
type ADJoinRequest struct {
	Username         string `json:"username"`
	Password         []byte `json:"password"` // transient, zeroed after use
	Domain           string `json:"domain"`
	DomainController string `json:"domain_controller"`
}

// JoinADDomain handles the POST /api/directory/join request
func (h *LDAPHandler) JoinADDomain(w http.ResponseWriter, r *http.Request) {
	var req ADJoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, ldapResp{Error: "Invalid request body"})
		return
	}
	defer func() {
		// Security: zero out password in memory
		for i := range req.Password {
			req.Password[i] = 0
		}
	}()

	if req.Username == "" || len(req.Password) == 0 || req.Domain == "" || req.DomainController == "" {
		writeJSON(w, 400, ldapResp{Error: "Username, password, domain, and domain controller are required"})
		return
	}

	// 1. NTP Pre-check
	if err := h.checkNTP(r.Context()); err != nil {
		writeJSON(w, 412, ldapResp{Error: "NTP Synchronization Pre-check Failed: " + err.Error()})
		return
	}

	// 2. Perform Join via 'net ads join'
	// Use environment variable for password to avoid exposure in /proc/pid/cmdline
	// We use security.CommandWhitelist["net_ads_join"].Path to get the binary name ("net")
	// and ValidateCommand to ensure args are safe.
	args := []string{"ads", "join", "-U", req.Username, "-W", req.Domain, "-S", req.DomainController}
	if err := security.ValidateCommand("net_ads_join", args); err != nil {
		writeJSON(w, 403, ldapResp{Error: "Security validation failed: " + err.Error()})
		return
	}

	cmdEntry, _ := security.CommandWhitelist["net_ads_join"]
	cmd := exec.CommandContext(r.Context(), cmdEntry.Path, args...)
	cmd.Env = append(os.Environ(), "PASSWD="+string(req.Password))
	
	if out, err := cmd.CombinedOutput(); err != nil {
		writeJSON(w, 500, ldapResp{Error: fmt.Sprintf("Domain join failed: %v\nOutput: %s", err, string(out))})
		return
	}

	// 3. Verify Join via 'wbinfo -t' (Machine account trust)

	if err := security.ValidateCommand("wbinfo_test", []string{"-t"}); err != nil {
		writeJSON(w, 403, ldapResp{Error: "Security validation failed: " + err.Error()})
		return
	}
	out, err := cmdutil.Run(30*time.Second, "wbinfo_test", "-t")
	if err != nil {
		// handle timeout/error
		if strings.Contains(err.Error(), "deadline exceeded") {
			writeJSON(w, 202, ldapResp{
				Success: true,
				Warning: "Domain joined but winbind verification timed out — check winbind service status.",
			})
		} else {
			writeJSON(w, 500, ldapResp{Error: "Domain joined but trust verification (wbinfo -t) failed: " + string(out)})
		}
		return
	}

	// 4. Update Database
	_, _ = h.db.Exec(`UPDATE ldap_config SET 
		domain_joined = true, 
		domain_joined_at = NOW(),
		provider_type = 'ad',
		realm = $1 
		WHERE id = 1`, strings.ToUpper(req.Domain))

	audit.LogAction("directory.join", r.Header.Get("X-User"), "Domain joined successfully: "+req.Domain, true, 0)
	writeJSON(w, 200, ldapResp{Success: true, Data: "Successfully joined domain " + req.Domain})
}

// GetDirectoryStatus handles GET /api/directory/status
func (h *LDAPHandler) GetDirectoryStatus(w http.ResponseWriter, r *http.Request) {
	// 1. Check database status
	var joined bool
	var joinedAt sql.NullTime
	var realm string
	var providerType string
	err := h.db.QueryRow("SELECT domain_joined, domain_joined_at, realm, provider_type FROM ldap_config WHERE id = 1").
		Scan(&joined, &joinedAt, &realm, &providerType)
	
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Database error: " + err.Error()})
		return
	}

	// 2. Check live status via 'net ads info'
	var liveStatus string
	if providerType == "ad" {
		if err := security.ValidateCommand("net_ads_info", []string{"ads", "info"}); err == nil {
			cmd := exec.Command("net", "ads", "info")
			if out, err := cmd.Output(); err == nil {
				liveStatus = string(out)
			} else {
				liveStatus = "Domain join info unavailable (Samba 'net' error)"
			}
		}
	}

	resp := map[string]interface{}{
		"provider_type":     providerType,
		"domain_joined":     joined,
		"domain_joined_at":  joinedAt,
		"realm":             realm,
		"net_ads_info":      liveStatus,
	}

	writeJSON(w, 200, ldapResp{Success: true, Data: resp})
}

func (h *LDAPHandler) checkNTP(ctx context.Context) error {
	// timedatectl show --property=NTPSynchronized
	args := []string{"show", "--property=NTPSynchronized"}
	if err := security.ValidateCommand("timedatectl_show", args); err != nil {
		return fmt.Errorf("security validation failed: %v", err)
	}
	out, err := cmdutil.Run(cmdutil.TimeoutFast, "timedatectl_show", "show", "--property=NTPSynchronized")
	if err != nil {
		return fmt.Errorf("failed to check NTP status: %v", err)
	}

	if !strings.Contains(string(out), "NTPSynchronized=yes") {
		return fmt.Errorf("system clock is not NTP synchronized (required for Kerberos/AD)")
	}

	return nil
}
