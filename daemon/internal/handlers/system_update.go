package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/jobs"
)

// Updates of an installed NixOS system.
//
// The "A/B OTA" path could not work: it needed boot entries named "DPlaneOS
// (Slot A/B)" that nothing created, a root filesystem per slot (the system
// mounts system-a), and update bundles that nothing built; the Updates page
// ran apt-get. NixOS generations do the same job: an update points the
// system flake at the new release, builds it as the next boot entry and
// reboots. After the reboot dplaneos-update-health (ota-module.nix) checks
// the system; when the check fails it makes the previous generation the boot
// default again, restores the flake and reboots.

var (
	updatePendingPath = "/var/lib/dplaneos/update-pending.json"
	updateResultPath  = "/var/lib/dplaneos/update-result.json"
	githubReleaseURL  = "https://api.github.com/repos/4nonX/DPlaneOS/releases/latest"
)

// updatePending is what the post-boot check needs to judge and to go back.
type updatePending struct {
	PreviousSystem string    `json:"previous_system"` // /nix/store/...-nixos-system-...
	PreviousFlake  string    `json:"previous_flake"`
	PreviousLock   string    `json:"previous_lock"`
	FromVersion    string    `json:"from_version"`
	ToVersion      string    `json:"to_version"`
	Pools          []string  `json:"pools"`      // imported before: must be imported after
	SMBShares      int       `json:"smb_shares"` // > 0: Samba must run
	StartedAt      time.Time `json:"started_at"`
}

type updateResult struct {
	Status      string    `json:"status"` // ok | rolled_back
	FromVersion string    `json:"from_version"`
	ToVersion   string    `json:"to_version"`
	Reason      string    `json:"reason,omitempty"`
	At          time.Time `json:"at"`
	Reported    bool      `json:"reported,omitempty"`
}

func latestRelease() (tag, url string, err error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(githubReleaseURL)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var r struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return "", "", err
	}
	return r.TagName, r.HTMLURL, nil
}

func readUpdateResult() *updateResult {
	b, err := os.ReadFile(updateResultPath)
	if err != nil {
		return nil
	}
	var r updateResult
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// GetSystemUpdate: GET /api/system/updates/nixos
func GetSystemUpdate(w http.ResponseWriter, r *http.Request) {
	exists, ref, custom := systemFlakeState()
	resp := map[string]any{"success": true, "nixos": IsNixOS(), "flake": exists, "custom": custom}
	if ref != "" {
		resp["ref"] = ref
	}
	if src, err := readSystemSource(); err == nil {
		resp["version"], resp["live"] = src.Version, src.Live
	}
	if _, err := os.Stat(updatePendingPath); err == nil {
		resp["pending"] = true
	}
	if res := readUpdateResult(); res != nil {
		resp["last_result"] = res
	}
	if r.URL.Query().Get("check") == "1" {
		tag, url, err := latestRelease()
		if err != nil {
			resp["check_error"] = "GitHub unreachable: " + err.Error()
		} else {
			cur, _ := resp["version"].(string)
			resp["latest_version"], resp["release_url"] = tag, url
			resp["update_available"] = tag != "" && strings.TrimPrefix(tag, "v") != strings.TrimPrefix(cur, "v")
		}
	}
	respondOK(w, resp)
}

// ApplySystemUpdate: POST /api/system/updates/nixos/apply { "version": "v15.2.0" }
// (empty: the latest release). The machine reboots when the build is done.
func ApplySystemUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
	src, err := readSystemSource()
	switch {
	case err != nil:
		respondErrorSimple(w, "This system has no DPlaneOS source record ("+systemSourcePath+"); it cannot be updated from here", http.StatusConflict)
		return
	case src.Live:
		respondErrorSimple(w, "A live system is not updated; boot a newer live image or install DPlaneOS", http.StatusConflict)
		return
	}
	if _, err := systemFlakeRef(); err != nil {
		respondErrorSimple(w, "The system flake (/etc/nixos/flake.nix) is missing or not managed by DPlaneOS: "+err.Error(), http.StatusConflict)
		return
	}
	if _, err := os.Stat(updatePendingPath); err == nil {
		respondErrorSimple(w, "An update is already waiting for its check after the reboot", http.StatusConflict)
		return
	}
	tag := req.Version
	if tag == "" {
		if tag, _, err = latestRelease(); err != nil {
			respondErrorSimple(w, "Cannot find the latest release: "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	if tag, err = normalizeReleaseTag(tag); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	user := r.Header.Get("X-User")
	jobID := jobs.Start("system_update", func(j *jobs.Job) {
		if err := runSystemUpdate(j, src, tag, user); err != nil {
			j.Fail(err.Error())
		}
	})
	respondOK(w, map[string]any{"success": true, "job_id": jobID, "version": tag})
}

// rebootAfterUpdate is replaced in tests.
var rebootAfterUpdate = func() {
	time.Sleep(10 * time.Second)
	exec.Command("systemctl", "reboot").Run() //nolint:errcheck
}

func runSystemUpdate(j *jobs.Job, src *systemSource, tag, user string) error {
	flakePath := filepath.Join(systemFlakeDir, "flake.nix")
	lockPath := filepath.Join(systemFlakeDir, "flake.lock")
	prevFlake, err := os.ReadFile(flakePath)
	if err != nil {
		return err
	}
	prevLock, _ := os.ReadFile(lockPath)
	prevSystem, err := filepath.EvalSymlinks("/run/current-system")
	if err != nil {
		return fmt.Errorf("current system: %w", err)
	}
	p := updatePending{
		PreviousSystem: prevSystem, PreviousFlake: string(prevFlake), PreviousLock: string(prevLock),
		FromVersion: src.Version, ToVersion: strings.TrimPrefix(tag, "v"), StartedAt: time.Now().UTC(),
	}
	if out, err := exec.Command("zpool", "list", "-H", "-o", "name").Output(); err == nil {
		p.Pools = strings.Fields(string(out))
	}
	if alertingHandler != nil {
		alertingHandler.db.QueryRow(`SELECT COUNT(*) FROM smb_shares WHERE enabled = 1`).Scan(&p.SMBShares) //nolint:errcheck
	}
	text, err := systemFlakeText(src.Flake+"/"+tag, src.Configuration)
	if err != nil {
		return err
	}
	restore := func() {
		os.WriteFile(flakePath, prevFlake, 0o644) //nolint:errcheck
		if prevLock != nil {
			os.WriteFile(lockPath, prevLock, 0o644) //nolint:errcheck
		}
		os.Remove(updatePendingPath)
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	if err := os.WriteFile(updatePendingPath, b, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", updatePendingPath, err)
	}
	if err := os.WriteFile(flakePath, []byte(text), 0o644); err != nil {
		restore()
		return err
	}
	j.Log(fmt.Sprintf("Building DPlaneOS %s as the next boot entry (this downloads and builds; it can take a while)...", tag))
	out, err := rebuildSystem("boot", "--recreate-lock-file")
	if err != nil {
		restore()
		audit.LogAction("system_update", user, "update to "+tag+" failed: "+err.Error(), false, 0)
		return fmt.Errorf("building %s failed, nothing changed: %v\n%s", tag, err, lastLines(out, 20))
	}
	audit.LogAction("system_update", user, "update to "+tag+" built; rebooting", true, 0)
	j.Log("Built. Rebooting in 10 seconds; the system is checked after the reboot and goes back to " + src.Version + " if the check fails.")
	j.Done(map[string]any{"version": tag, "rebooting": true})
	go rebootAfterUpdate()
	return nil
}

// ReportUpdateResult alerts once about the outcome of the last update
// (written by dplaneos-update-health after the reboot).
func ReportUpdateResult() {
	res := readUpdateResult()
	if res == nil || res.Reported {
		return
	}
	switch res.Status {
	case "ok":
		DispatchAlert("info", EventUpgradeApplied, "system",
			fmt.Sprintf("Updated from DPlaneOS %s to %s.", res.FromVersion, res.ToVersion))
	case "rolled_back":
		DispatchAlert("critical", "upgrade.rolled_back", "system",
			fmt.Sprintf("The update to DPlaneOS %s failed its check after the reboot (%s); the system went back to %s.", res.ToVersion, res.Reason, res.FromVersion))
	}
	res.Reported = true
	if b, err := json.MarshalIndent(res, "", "  "); err == nil {
		if err := os.WriteFile(updateResultPath, b, 0o600); err != nil {
			log.Printf("UPDATE: %v", err)
		}
	}
}
