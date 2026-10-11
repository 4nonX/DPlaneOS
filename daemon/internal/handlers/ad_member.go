package handlers

// Membership in an Active Directory domain.
//
// The system joins one domain (a Samba member server cannot be in two).
// Users of that domain, and of domains it trusts, then use the SMB shares
// with their domain accounts and appear as owners of their files. Further
// rows in ad_domains are trusted domains: they are not joined, they only
// give the users of that domain an ID range of their own.
//
// Joining, in this order (the order matters: `net ads join` reads smb.conf):
//
//  1. checks: this Samba can do it, no other domain is joined, the domain's
//     controllers are found through DNS, the host name fits;
//  2. smb.conf is written for the domain (security = ads, realm, workgroup),
//     without touching the running services;
//  3. net ads join, with the account's password on standard input;
//  4. Samba is restarted and the trust is verified (wbinfo -t);
//  5. the membership is recorded.
//
// If step 3 or 4 fails, smb.conf goes back to what it was.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"dplaned/internal/audit"
	"dplaned/internal/jobs"
	"dplaned/internal/nixwriter"
	"dplaned/internal/smbconf"
)

// sambaShares is the handler that owns smb.conf (set when it is created).
var sambaShares *ShareCRUDHandler

// adMu serialises joins and leaves.
var adMu sync.Mutex

// adCommands are the external steps of a join; replaced in tests.
type adCommands struct {
	// HasADS reports whether this Samba was built with Active Directory support.
	HasADS func() (bool, error)
	// LookupSRV finds the domain controllers of a realm through DNS.
	LookupSRV func(realm string) ([]string, error)
	// Net runs `net <args>` with the password on standard input.
	Net func(password string, args ...string) (string, error)
	// RestartSamba restarts smbd, nmbd and winbindd.
	RestartSamba func() error
	// TrustOK checks the machine account against the domain (wbinfo -t).
	TrustOK  func() error
	Hostname func() (string, error)
}

var adCmd = adCommands{
	HasADS: func() (bool, error) {
		out, err := exec.Command("smbd", "-b").CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("smbd -b: %v", err)
		}
		return bytes.Contains(out, []byte("WITH_ADS")) || bytes.Contains(out, []byte("HAVE_ADS")), nil
	},
	LookupSRV: func(realm string) ([]string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, srvs, err := net.DefaultResolver.LookupSRV(ctx, "ldap", "tcp", strings.ToLower(realm))
		if err != nil {
			return nil, err
		}
		var hosts []string
		for _, s := range srvs {
			hosts = append(hosts, strings.TrimSuffix(s.Target, "."))
		}
		return hosts, nil
	},
	Net: func(password string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "net", args...)
		// The password goes in on a pipe: not on the command line, not in
		// the environment.
		cmd.Env = append(os.Environ(), "PASSWD_FD=0")
		cmd.Stdin = strings.NewReader(password)
		out, err := cmd.CombinedOutput()
		return string(out), err
	},
	RestartSamba: func() error {
		out, err := exec.Command("systemctl", "restart", "samba-winbindd.service", "samba-smbd.service", "samba-nmbd.service").CombinedOutput()
		if err != nil {
			return fmt.Errorf("restarting Samba: %v %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	},
	TrustOK: func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "wbinfo", "-t").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	},
	Hostname: os.Hostname,
}

// adAccountRe: an account name as it is passed to net(8) after -U.
func validADAccount(s string) bool {
	if s == "" || len(s) > 104 || strings.HasPrefix(s, "-") {
		return false
	}
	return !strings.ContainsAny(s, "%\x00\r\n\"'`$;|&<> \t")
}

// validADOU: an organizational unit path such as Servers/NAS.
func validADOU(s string) bool {
	if len(s) > 200 || strings.HasPrefix(s, "-") {
		return false
	}
	return !strings.ContainsAny(s, "\x00\r\n\"'`$;|&<>%")
}

// explainADError turns what net(8) says into what to do about it.
func explainADError(out string) string {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "ads support not compiled in"):
		return "This system's Samba was built without Active Directory support."
	case strings.Contains(low, "clock skew"):
		return "The clocks of this system and of the domain controller differ by more than five minutes. Set the domain controller as the time server (System › Settings) and try again."
	case strings.Contains(low, "logon failure") || strings.Contains(low, "preauthentication failed") || strings.Contains(low, "nt_status_logon_failure"):
		return "The domain did not accept this user name and password."
	case strings.Contains(low, "access denied") || strings.Contains(low, "insufficient access"):
		return "This account may not join computers to the domain (or not in this organizational unit)."
	case strings.Contains(low, "failed to find dc") || strings.Contains(low, "no logon servers") || strings.Contains(low, "couldn't find a dc"):
		return "No domain controller answered. Check that this system uses the domain's DNS server (Network › DNS) and reaches the controllers."
	case strings.Contains(low, "cannot contact any kdc") || strings.Contains(low, "unable to reach any kdc"):
		return "The domain's Kerberos service did not answer (TCP/UDP 88 on the domain controllers)."
	case strings.Contains(low, "already exists"):
		return "A computer account with this system's name exists in the domain and this account may not reuse it."
	}
	// The last line that says something.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			if len(t) > 300 {
				t = t[:300]
			}
			return t
		}
	}
	return "the join failed without a message"
}

type adDomain struct {
	Name, Realm, Server string
	Joined              bool
}

func loadADDomain(db *sql.DB, name string) (*adDomain, error) {
	d := adDomain{Name: name}
	err := db.QueryRow(`SELECT realm, COALESCE(server,''), domain_joined FROM ad_domains WHERE name = $1`, name).Scan(&d.Realm, &d.Server, &d.Joined)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// setADMode switches the Samba configuration between domain member and
// standalone, in the system settings and in the workgroup setting. It
// returns the workgroup that was set before.
func setADMode(db *sql.DB, d *adDomain) (previousWorkgroup string, err error) {
	_ = db.QueryRow(`SELECT COALESCE(value,'') FROM settings WHERE key = 'smb_workgroup'`).Scan(&previousWorkgroup)
	st := nixwriter.Default().State()
	opts := nixwriter.SambaGlobalOpts{
		Workgroup: st.SambaWorkgroup, ServerString: st.SambaServerString, TimeMachine: st.SambaTimeMachine,
		AllowGuest: st.SambaAllowGuest, ExtraGlobal: st.SambaExtraGlobal,
	}
	workgroup := previousWorkgroup
	if d != nil {
		opts.SecurityMode, opts.Realm, opts.DomainController, opts.Workgroup = "ads", d.Realm, d.Server, d.Name
		workgroup = d.Name
		// Remembered for the day the domain is left.
		if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('smb_workgroup_before_domain', $1)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, previousWorkgroup); err != nil {
			return previousWorkgroup, err
		}
	} else {
		opts.SecurityMode = "user"
		var before string
		_ = db.QueryRow(`SELECT COALESCE(value,'') FROM settings WHERE key = 'smb_workgroup_before_domain'`).Scan(&before)
		if before == "" {
			before = "WORKGROUP"
		}
		workgroup, opts.Workgroup = before, before
	}
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('smb_workgroup', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, workgroup); err != nil {
		return previousWorkgroup, err
	}
	if err := nixwriter.Default().SetSambaGlobals(opts); err != nil {
		return previousWorkgroup, err
	}
	syncIDMAPToNixwriter(db)
	return previousWorkgroup, nil
}

// writeSambaConf writes smb.conf from the settings without touching the
// services; applySambaConf also reloads or restarts them.
func writeSambaConf() error {
	if sambaShares == nil {
		return errors.New("the share configuration is not available")
	}
	_, _, _, _, err := sambaShares.writeSMBConf()
	return err
}

func applySambaConf() error {
	if sambaShares == nil {
		return errors.New("the share configuration is not available")
	}
	_, err := sambaShares.regenerateSMBConf()
	return err
}

// applyIDRanges makes the ID ranges of the registered domains the ones
// Samba uses. They only matter to a member of a domain: there the
// configuration is rewritten and winbind, which maps the IDs, restarted.
func applyIDRanges(db *sql.DB) {
	syncIDMAPToNixwriter(db)
	if nixwriter.Default().State().SambaSecurityMode != "ads" {
		return
	}
	if err := applySambaConf(); err != nil {
		log.Printf("AD: applying the ID ranges: %v", err)
		return
	}
	if out, err := exec.Command("systemctl", "try-restart", "samba-winbindd.service").CombinedOutput(); err != nil {
		log.Printf("AD: restarting winbind for the new ID ranges: %v %s", err, strings.TrimSpace(string(out)))
	}
}

// joinADDomain joins the domain registered as name. step reports progress.
func joinADDomain(db *sql.DB, name, username, password, ou string, step func(string)) error {
	adMu.Lock()
	defer adMu.Unlock()

	d, err := loadADDomain(db, name)
	if err != nil {
		return errors.New("the domain is not registered")
	}
	if d.Joined {
		return errors.New("this system is already a member of " + name)
	}
	var other string
	if err := db.QueryRow(`SELECT name FROM ad_domains WHERE domain_joined AND name <> $1 LIMIT 1`, name).Scan(&other); err == nil {
		return fmt.Errorf("this system is a member of %s. A system is a member of one domain; users of other domains get in through a trust between the domains. Leave %s first to join another", other, other)
	}
	if !validADAccount(username) {
		return errors.New("the user name contains characters that are not allowed (give it without the domain)")
	}
	if !validADOU(ou) {
		return errors.New("the organizational unit contains characters that are not allowed")
	}
	if !smbconf.ValidWorkgroup(name) {
		return fmt.Errorf("%q cannot be used as the short (NetBIOS) name of a domain: at most 15 letters, digits, dots, hyphens", name)
	}
	if ok, err := adCmd.HasADS(); err == nil && !ok {
		return errors.New("this system's Samba was built without Active Directory support")
	}
	if host, err := adCmd.Hostname(); err == nil && len(strings.SplitN(host, ".", 2)[0]) > 15 {
		return fmt.Errorf("the host name %q is longer than 15 characters, which a computer account in a domain cannot be. Shorten it under System › Settings", host)
	}
	step("Looking for the domain controllers of " + d.Realm)
	dcs, err := adCmd.LookupSRV(d.Realm)
	if err != nil || len(dcs) == 0 {
		return fmt.Errorf("no domain controllers of %s were found through DNS (%v). Set the domain's DNS server under Network › DNS: a member finds its domain through DNS", d.Realm, err)
	}
	step("Found " + strings.Join(dcs, ", "))

	previousWorkgroup, err := setADMode(db, d)
	if err == nil {
		err = writeSambaConf()
	}
	revert := func() {
		if _, rerr := db.Exec(`INSERT INTO settings (key, value) VALUES ('smb_workgroup_before_domain', $1)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, previousWorkgroup); rerr != nil {
			log.Printf("AD: %v", rerr)
		}
		if _, rerr := setADMode(db, nil); rerr != nil {
			log.Printf("AD: restoring the standalone configuration: %v", rerr)
		}
		if rerr := applySambaConf(); rerr != nil {
			log.Printf("AD: restoring the standalone configuration: %v", rerr)
		}
	}
	if err != nil {
		revert()
		return fmt.Errorf("preparing the Samba configuration: %w", err)
	}

	step("Joining " + d.Realm)
	args := []string{"ads", "join", "-U", username}
	if d.Server != "" {
		args = append(args, "-S", d.Server)
	}
	if ou != "" {
		args = append(args, "createcomputer="+ou)
	}
	out, err := adCmd.Net(password, args...)
	if err != nil {
		log.Printf("AD: net ads join %s failed: %v: %s", d.Realm, err, strings.TrimSpace(out))
		revert()
		return errors.New(explainADError(out))
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			step(t)
		}
	}

	step("Starting the domain services")
	if err := adCmd.RestartSamba(); err != nil {
		// The computer account exists now; take it out again rather than
		// leave a half-joined system.
		_, _ = adCmd.Net(password, "ads", "leave", "-U", username)
		revert()
		return err
	}
	var trustErr error
	for i := 0; i < 20; i++ {
		if trustErr = adCmd.TrustOK(); trustErr == nil {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if trustErr != nil {
		_, _ = adCmd.Net(password, "ads", "leave", "-U", username)
		revert()
		return fmt.Errorf("the domain was joined, but the trust could not be verified (%v). The join was taken back", trustErr)
	}
	step("The domain accepts this system's account")

	if _, err := db.Exec(`UPDATE ad_domains SET domain_joined = true, domain_joined_at = NOW(), kinit_principal = $2,
		last_kinit_at = NOW(), kinit_ok = true, updated_at = NOW() WHERE name = $1`, name, username+"@"+d.Realm); err != nil {
		return fmt.Errorf("the domain was joined, but recording it failed: %w", err)
	}
	if _, err := db.Exec(`UPDATE ldap_config SET domain_joined = true, domain_joined_at = NOW(), realm = $1 WHERE id = 1`, d.Realm); err != nil {
		log.Printf("AD: %v", err)
	}
	return nil
}

// leaveADDomain removes this system from the domain registered as name.
// force: when the domain cannot be reached or refuses, leave anyway; the
// computer account then stays behind in the domain.
func leaveADDomain(db *sql.DB, name, username, password string, force bool, step func(string)) error {
	adMu.Lock()
	defer adMu.Unlock()

	d, err := loadADDomain(db, name)
	if err != nil {
		return errors.New("the domain is not registered")
	}
	if !d.Joined {
		return errors.New("this system is not a member of " + name)
	}
	if !validADAccount(username) {
		return errors.New("the user name contains characters that are not allowed (give it without the domain)")
	}
	step("Removing this system's account from " + d.Realm)
	out, err := adCmd.Net(password, "ads", "leave", "-U", username)
	if err != nil {
		log.Printf("AD: net ads leave %s failed: %v: %s", d.Realm, err, strings.TrimSpace(out))
		if !force {
			return errors.New(explainADError(out) + " (to leave anyway, without removing the computer account from the domain, choose that option)")
		}
		step("The domain did not remove the account (" + explainADError(out) + "); leaving anyway. Delete the computer account in the domain by hand.")
	}
	if _, err := db.Exec(`UPDATE ad_domains SET domain_joined = false, domain_joined_at = NULL, kinit_ok = false, updated_at = NOW() WHERE name = $1`, name); err != nil {
		return fmt.Errorf("the domain was left, but recording it failed: %w", err)
	}
	if _, err := db.Exec(`UPDATE ldap_config SET domain_joined = false, domain_joined_at = NULL WHERE id = 1`); err != nil {
		log.Printf("AD: %v", err)
	}
	step("Back to local accounts")
	if _, err := setADMode(db, nil); err != nil {
		return fmt.Errorf("the domain was left, but the Samba configuration could not be reset: %w", err)
	}
	if err := applySambaConf(); err != nil {
		return fmt.Errorf("the domain was left, but Samba could not be restarted with the new configuration: %w", err)
	}
	return nil
}

// JoinDomain: POST /api/ldap/domains/{name}/join {username, password, ou}
// Answers with a job; GET /api/jobs/{id} has the result.
func (h *LDAPHandler) JoinDomain(w http.ResponseWriter, r *http.Request) {
	h.startMembershipJob(w, r, true)
}

// LeaveDomain: POST /api/ldap/domains/{name}/leave {username, password}
func (h *LDAPHandler) LeaveDomain(w http.ResponseWriter, r *http.Request) {
	h.startMembershipJob(w, r, false)
}

func (h *LDAPHandler) startMembershipJob(w http.ResponseWriter, r *http.Request, join bool) {
	name := strings.ToUpper(strings.TrimSpace(mux.Vars(r)["name"]))
	if !domainNameRe.MatchString(name) {
		respondErrorSimple(w, "Invalid domain name", http.StatusBadRequest)
		return
	}
	var req struct {
		Username string `json:"username"` // without the domain
		Password string `json:"password"`
		OU       string `json:"ou"`    // join only: where the computer account goes
		Force    bool   `json:"force"` // leave only: even if the domain does not answer
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		respondErrorSimple(w, "username and password required", http.StatusBadRequest)
		return
	}
	if _, err := loadADDomain(h.db, name); err != nil {
		respondErrorSimple(w, "Domain not found", http.StatusNotFound)
		return
	}
	user, db := r.Header.Get("X-User"), h.db
	kind, command := "ad_leave", "AD_DOMAIN_LEAVE"
	if join {
		kind, command = "ad_join", "AD_DOMAIN_JOIN"
	}
	jobID := jobs.Start(kind, func(j *jobs.Job) {
		var err error
		if join {
			err = joinADDomain(db, name, req.Username, req.Password, strings.TrimSpace(req.OU), j.Log)
		} else {
			err = leaveADDomain(db, name, req.Username, req.Password, req.Force, j.Log)
		}
		audit.Log(audit.AuditLog{
			Level: audit.LevelInfo, Command: command, User: user, Success: err == nil,
			Metadata: map[string]any{"domain": name},
		})
		if err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done(map[string]any{"domain": name, "joined": join})
	})
	respondJSON(w, http.StatusAccepted, map[string]any{"success": true, "job_id": jobID})
}

// GetDirectoryStatus: GET /api/directory/status
// The domain this system is a member of, if any.
func (h *LDAPHandler) GetDirectoryStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"domain_joined": false}
	var providerType string
	_ = h.db.QueryRow(`SELECT provider_type FROM ldap_config WHERE id = 1`).Scan(&providerType)
	resp["provider_type"] = providerType

	var name, realm string
	var joinedAt sql.NullTime
	var trustOK bool
	var checkedAt sql.NullTime
	err := h.db.QueryRow(`SELECT name, realm, domain_joined_at, kinit_ok, last_kinit_at FROM ad_domains WHERE domain_joined LIMIT 1`).
		Scan(&name, &realm, &joinedAt, &trustOK, &checkedAt)
	switch {
	case err == nil:
		resp["domain_joined"], resp["domain"], resp["realm"] = true, name, realm
		resp["trust_ok"] = trustOK
		if joinedAt.Valid {
			resp["domain_joined_at"] = joinedAt.Time
		}
		if checkedAt.Valid {
			resp["trust_checked_at"] = checkedAt.Time
		}
	case !errors.Is(err, sql.ErrNoRows):
		writeJSON(w, 500, ldapResp{Error: "Database error: " + err.Error()})
		return
	}
	if ok, err := adCmd.HasADS(); err == nil {
		resp["supported"] = ok
	}
	writeJSON(w, 200, ldapResp{Success: true, Data: resp})
}

// StartDomainMonitor checks the membership every 15 minutes and raises an
// alert when the domain no longer accepts this system's account (the
// account was deleted or reset, or no controller is reachable): domain
// users cannot open shares then.
func StartDomainMonitor(ctx context.Context, db *sql.DB) {
	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				checkDomainMembership(db)
			}
		}
	}()
}

func checkDomainMembership(db *sql.DB) {
	var name string
	var was bool
	if err := db.QueryRow(`SELECT name, kinit_ok FROM ad_domains WHERE domain_joined LIMIT 1`).Scan(&name, &was); err != nil {
		return // not a member
	}
	if !adMu.TryLock() {
		return // a join or leave is in progress
	}
	err := adCmd.TrustOK()
	adMu.Unlock()
	ok := err == nil
	if _, dbErr := db.Exec(`UPDATE ad_domains SET last_kinit_at = NOW(), kinit_ok = $2 WHERE name = $1`, name, ok); dbErr != nil {
		log.Printf("AD: recording the membership check: %v", dbErr)
	}
	switch {
	case !ok && was:
		DispatchAlert("critical", EventDirectoryTrustLost, name,
			fmt.Sprintf("The domain %s no longer accepts this system's account (%v). Domain users cannot open shares until this is repaired: check that the domain controllers are reachable, or leave and join the domain again.", name, err))
	case ok && !was:
		DispatchAlert("info", EventDirectoryTrustRestored, name, fmt.Sprintf("The domain %s accepts this system's account again.", name))
	}
}
