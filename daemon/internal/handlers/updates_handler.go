package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/jobs"
)

// HandleUpdatesCheck handles GET /api/system/updates/check
// Enqueues an apt-get update followed by apt list --upgradable.
// Returns a job_id immediately; poll GET /api/jobs/{id} for results.
func HandleUpdatesCheck(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	jobID := jobs.Start("apt_update_check", func(j *jobs.Job) {
		start := time.Now()

		// Step 1: refresh package index
		updateCmd := exec.Command("/usr/bin/apt-get", "update", "-qq")
		updateCmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		if out, err := updateCmd.CombinedOutput(); err != nil {
			log.Printf("[updates] apt-get update failed: %v\n%s", err, out)
			j.Fail("apt-get update failed: " + err.Error())
			return
		}

		// Step 2: list upgradable packages
		listCmd := exec.Command("/usr/bin/apt", "list", "--upgradable")
		listCmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		listOut, err := listCmd.Output()
		if err != nil {
			log.Printf("[updates] apt list --upgradable failed: %v", err)
			j.Fail("apt list --upgradable failed: " + err.Error())
			return
		}

		packages := parseAptUpgradable(string(listOut))

		audit.LogCommand(audit.LevelInfo, user, "apt_update_check", nil, true, time.Since(start), nil)

		j.Done(map[string]interface{}{
			"packages":      packages,
			"package_count": len(packages),
			"duration_ms":   time.Since(start).Milliseconds(),
		})
	})

	respondOK(w, map[string]interface{}{
		"success": true,
		"job_id":  jobID,
	})
}

// HandleUpdatesApply handles POST /api/system/updates/apply
// Enqueues a full apt-get upgrade -y (non-interactive). Returns job_id immediately.
func HandleUpdatesApply(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	jobID := jobs.Start("apt_upgrade", func(j *jobs.Job) {
		start := time.Now()

		cmd := exec.Command("/usr/bin/apt-get", "upgrade", "-y", "-q")
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		out, err := cmd.CombinedOutput()
		duration := time.Since(start)

		audit.LogCommand(audit.LevelInfo, user, "apt_upgrade", nil, err == nil, duration, err)

		if err != nil {
			log.Printf("[updates] apt-get upgrade failed: %v\n%s", err, out)
			j.Fail("apt-get upgrade failed: " + err.Error() + "\n" + string(out))
			return
		}

		j.Done(map[string]interface{}{
			"output":      string(out),
			"duration_ms": duration.Milliseconds(),
		})
	})

	respondOK(w, map[string]interface{}{
		"success": true,
		"job_id":  jobID,
	})
}

// HandleUpdatesApplySecurity handles POST /api/system/updates/apply-security
// Enqueues unattended-upgrades to apply security-only updates. Returns job_id immediately.
// Falls back to a filtered apt-get upgrade if unattended-upgrades is not available.
func HandleUpdatesApplySecurity(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	jobID := jobs.Start("apt_security_upgrade", func(j *jobs.Job) {
		start := time.Now()

		// Prefer unattended-upgrades when available
		uuPath, err := exec.LookPath("unattended-upgrades")
		if err == nil {
			cmd := exec.Command(uuPath, "--minimal-upgrade-steps", "-v")
			cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
			out, runErr := cmd.CombinedOutput()
			duration := time.Since(start)

			audit.LogCommand(audit.LevelInfo, user, "unattended_upgrades", nil, runErr == nil, duration, runErr)

			if runErr != nil {
				log.Printf("[updates] unattended-upgrades failed: %v\n%s", runErr, out)
				j.Fail("unattended-upgrades failed: " + runErr.Error() + "\n" + string(out))
				return
			}
			j.Done(map[string]interface{}{
				"output":      string(out),
				"duration_ms": duration.Milliseconds(),
				"method":      "unattended-upgrades",
			})
			return
		}

		// Fallback: collect security packages via apt list and upgrade them
		listCmd := exec.Command("/usr/bin/apt", "list", "--upgradable")
		listCmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		listOut, listErr := listCmd.Output()
		if listErr != nil {
			j.Fail("apt list --upgradable failed: " + listErr.Error())
			return
		}

		secPkgs := filterSecurityPackages(string(listOut))
		if len(secPkgs) == 0 {
			j.Done(map[string]interface{}{
				"output":      "No security packages to upgrade.",
				"duration_ms": time.Since(start).Milliseconds(),
				"method":      "apt-get-filtered",
			})
			return
		}

		args := append([]string{"upgrade", "-y", "-q", "--only-upgrade"}, secPkgs...)
		cmd := exec.Command("/usr/bin/apt-get", args...)
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		out, runErr := cmd.CombinedOutput()
		duration := time.Since(start)

		audit.LogCommand(audit.LevelInfo, user, "apt_security_upgrade", secPkgs, runErr == nil, duration, runErr)

		if runErr != nil {
			log.Printf("[updates] apt-get security upgrade failed: %v\n%s", runErr, out)
			j.Fail("apt-get security upgrade failed: " + runErr.Error() + "\n" + string(out))
			return
		}

		j.Done(map[string]interface{}{
			"output":      string(out),
			"duration_ms": duration.Milliseconds(),
			"method":      "apt-get-filtered",
			"packages":    secPkgs,
		})
	})

	respondOK(w, map[string]interface{}{
		"success": true,
		"job_id":  jobID,
	})
}

// HandleDaemonVersion handles GET /api/system/updates/daemon-version
// Returns the current daemon version and checks GitHub for the latest release.
// Responds gracefully on network failure.
func HandleDaemonVersion(w http.ResponseWriter, r *http.Request) {
	current := DaemonVersion

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/4nonX/D-PlaneOS/releases/latest")
	if err != nil {
		log.Printf("[updates] GitHub version check failed: %v", err)
		respondOK(w, map[string]interface{}{
			"success":          false,
			"current_version":  current,
			"latest_version":   "",
			"update_available": false,
			"release_url":      "",
			"error":            "GitHub unreachable: " + err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":          false,
			"current_version":  current,
			"latest_version":   "",
			"update_available": false,
			"release_url":      "",
			"error":            "Failed to read GitHub response: " + err.Error(),
		})
		return
	}

	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		respondOK(w, map[string]interface{}{
			"success":          false,
			"current_version":  current,
			"latest_version":   "",
			"update_available": false,
			"release_url":      "",
			"error":            "Failed to parse GitHub response: " + err.Error(),
		})
		return
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	currentTrimmed := strings.TrimPrefix(current, "v")

	respondOK(w, map[string]interface{}{
		"success":          true,
		"current_version":  current,
		"latest_version":   release.TagName,
		"update_available": latest != currentTrimmed && release.TagName != "",
		"release_url":      release.HTMLURL,
	})
}

// UpgradablePackage describes a single apt package available for upgrade.
type UpgradablePackage struct {
	Name           string `json:"name"`
	NewVersion     string `json:"new_version"`
	CurrentVersion string `json:"current_version"`
	Security       bool   `json:"security"`
}

// parseAptUpgradable parses the output of `apt list --upgradable`.
// Each relevant line has the format:
//
//	packagename/focal-security 1.2.3-4 amd64 [upgradable from: 1.2.2-1]
func parseAptUpgradable(output string) []UpgradablePackage {
	var packages []UpgradablePackage

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// Skip header and blank lines
		if line == "" || strings.HasPrefix(line, "Listing...") || strings.HasPrefix(line, "WARNING") {
			continue
		}

		// Format: name/suite version arch [upgradable from: old_version]
		// Split on whitespace tokens
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		// parts[0] = "packagename/suite"
		nameAndSuite := strings.SplitN(parts[0], "/", 2)
		if len(nameAndSuite) < 1 {
			continue
		}
		pkgName := nameAndSuite[0]
		suite := ""
		if len(nameAndSuite) == 2 {
			suite = nameAndSuite[1]
		}

		newVersion := parts[1]

		// Extract current version from "[upgradable from: X]"
		currentVersion := ""
		raw := strings.Join(parts, " ")
		const fromPrefix = "upgradable from: "
		if idx := strings.Index(raw, fromPrefix); idx != -1 {
			tail := raw[idx+len(fromPrefix):]
			tail = strings.TrimRight(tail, "]")
			currentVersion = strings.TrimSpace(tail)
		}

		isSecurity := strings.Contains(strings.ToLower(suite), "security")

		packages = append(packages, UpgradablePackage{
			Name:           pkgName,
			NewVersion:     newVersion,
			CurrentVersion: currentVersion,
			Security:       isSecurity,
		})
	}

	return packages
}

// filterSecurityPackages returns only the package names that come from a
// security suite, extracted from `apt list --upgradable` output.
func filterSecurityPackages(output string) []string {
	var names []string
	for _, pkg := range parseAptUpgradable(output) {
		if pkg.Security {
			names = append(names, pkg.Name)
		}
	}
	return names
}
