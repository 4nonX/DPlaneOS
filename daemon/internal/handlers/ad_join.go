package handlers

import (
	"database/sql"
	"encoding/json"
	"log"
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
	Username string `json:"username"`
	// Password is a JSON string (the web UI sends one). As []byte it had to
	// be base64, so every join failed with "Invalid request body".
	Password         string `json:"password"`
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
	if req.Username == "" || len(req.Password) == 0 || req.Domain == "" || req.DomainController == "" {
		writeJSON(w, 400, ldapResp{Error: "Username, password, domain, and domain controller are required"})
		return
	}

	// 1. Clock: Kerberos needs it within 5 minutes of the DC's.
	if err := checkClockSkew(req.DomainController); err != nil {
		writeJSON(w, 412, ldapResp{Error: err.Error(), Code: "ntp_skew",
			Guide: "Synchronise the clock (Settings → System → Time; the domain controller is a good NTP server) and check that the domain controller is reachable."})
		return
	}

	// 2. Samba must already be an AD member of this realm (applied AD
	// configuration); -W is the NetBIOS workgroup (the DNS domain was passed).
	workgroup, err := adJoinReady(req.Domain, runTestparm)
	if err != nil {
		writeJSON(w, 409, ldapResp{Error: err.Error(), Code: "samba_not_configured",
			Guide: "Directory Services: save the Active Directory configuration, then apply the pending NixOS changes (Settings → NixOS)."})
		return
	}

	// 3. Perform Join via 'net ads join'
	args := []string{"ads", "join", "-U", req.Username, "-W", workgroup, "-S", req.DomainController}
	if err := security.ValidateCommand("net_ads_join", args); err != nil {
		writeJSON(w, 403, ldapResp{Error: "The domain join request was blocked by the security validator.", Code: "security_validation"})
		return
	}

	cmdEntry := security.CommandWhitelist["net_ads_join"]
	cmd := exec.CommandContext(r.Context(), cmdEntry.Path, args...)
	cmd.Env = append(os.Environ(), "PASSWD="+string(req.Password))

	if out, err := cmd.CombinedOutput(); err != nil {
		ue := SanitizeDomainJoin(err, string(out))
		log.Printf("ad_join: net ads join failed for domain %s: %v output: %s", req.Domain, err, string(out))
		writeJSON(w, 500, ldapResp{Error: ue.Error, Code: ue.Code, Guide: ue.Guide})
		return
	}

	// 3. Verify Join via 'wbinfo -t' (Machine account trust)
	if err := security.ValidateCommand("wbinfo_test", []string{"-t"}); err != nil {
		writeJSON(w, 403, ldapResp{Error: "The winbind verification request was blocked by the security validator.", Code: "security_validation"})
		return
	}
	// winbindd exits at startup while the host is not joined ("Could not
	// fetch our SID - did we join?"): start it now, then give it a moment to
	// contact the DC.
	if out, err := cmdutil.Run(60*time.Second, "systemctl_restart_winbindd", "restart", "samba-winbindd.service"); err != nil {
		log.Printf("ad_join: restart winbind after join to %s: %v %s", req.Domain, err, out)
	}
	var out []byte
	for i := 0; i < 10; i++ {
		if out, err = cmdutil.Run(30*time.Second, "wbinfo_test", "-t"); err == nil || strings.Contains(err.Error(), "deadline exceeded") {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		if strings.Contains(err.Error(), "deadline exceeded") {
			writeJSON(w, 202, ldapResp{
				Success: true,
				Warning: "Domain joined but the winbind service did not respond in time. The join may still succeed - check the HA status page in a few seconds.",
			})
		} else {
			log.Printf("ad_join: wbinfo -t failed after join to %s: %v output: %s", req.Domain, err, string(out))
			writeJSON(w, 500, ldapResp{
				Error: "The domain was joined but trust verification failed. The join may still be functional - try again in a few seconds.",
				Code:  "winbind_verification_failed",
				Guide: "Check Directory Services status and the winbind service.",
			})
		}
		return
	}

	// 4. Update Database
	if _, err := h.db.Exec(`UPDATE ldap_config SET
		domain_joined = true,
		domain_joined_at = NOW(),
		provider_type = 'ad',
		realm = $1
		WHERE id = 1`, strings.ToUpper(req.Domain)); err != nil {
		log.Printf("ad_join: persist domain join state for %s: %v", req.Domain, err)
	}

	// 5. Sync IDMAP config to nixwriter so next nixos-rebuild activates winbind
	// and NSS resolution. Without this the samba.nix module stays in "user"
	// security mode and winbind never starts.
	syncIDMAPToNixwriter(h.db)

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

	resp := map[string]any{
		"provider_type":    providerType,
		"domain_joined":    joined,
		"domain_joined_at": joinedAt,
		"realm":            realm,
		"net_ads_info":     liveStatus,
	}

	writeJSON(w, 200, ldapResp{Success: true, Data: resp})
}
