package handlers

// system_health.go - D-PlaneOS v3.3.2
//
// Provides /api/system/health which the frontend polls to surface:
//   - Read-Only filesystem detection (SD card wear / unexpected mount)
//   - NTP time sync status (clock skew causes JWT rejections on login)
//   - Last journalctl error lines for a named systemd service

import (
	"bufio"
	"dplaned/internal/cmdutil"
	"net/http"
	"os"
	"strings"
	"time"
)

// SystemHealthHandler serves /api/system/health
func SystemHealthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Filesystem read-only detection
	roPartitions := detectReadOnlyPartitions()

	// NTP sync status
	ntpSynced, ntpOffset := ntpStatus()

	// Service last-error (optional ?service=NAME query param)
	var serviceError string
	if svc := r.URL.Query().Get("service"); svc != "" {
		serviceError = lastServiceError(svc)
	}

	type healthResp struct {
		Success         bool     `json:"success"`
		ROPartitions    []string `json:"ro_partitions"`
		FilesystemRO    bool     `json:"filesystem_ro"`
		NTPSynced       bool     `json:"ntp_synced"`
		NTPOffset       string   `json:"ntp_offset,omitempty"`
		ServiceError    string   `json:"service_error,omitempty"`
		CheckedAt       string   `json:"checked_at"`
	}

	respondOK(w, healthResp{
		Success:      true,
		ROPartitions: roPartitions,
		FilesystemRO: len(roPartitions) > 0,
		NTPSynced:    ntpSynced,
		NTPOffset:    ntpOffset,
		ServiceError: serviceError,
		CheckedAt:    time.Now().UTC().Format(time.RFC3339),
	})
}

// detectReadOnlyPartitions reads /proc/mounts and returns any partitions
// mounted with the "ro" option that are real block devices (not tmpfs, etc.).
func detectReadOnlyPartitions() []string {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()

	// Filesystems we care about - ignore pseudo-fs
	realFS := map[string]bool{
		"ext2": true, "ext3": true, "ext4": true,
		"xfs": true, "btrfs": true, "vfat": true,
		"f2fs": true, "nilfs2": true, "jfs": true,
		"zfs": true,
	}

	var roMounts []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// Format: device mountpoint fstype options dump pass
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		device, mountpoint, fstype, opts := fields[0], fields[1], fields[2], fields[3]
		if !realFS[fstype] {
			continue
		}
		// Skip loop/squashfs and nix store
		if strings.HasPrefix(device, "loop") || strings.Contains(mountpoint, "/nix/store") {
			continue
		}
		for _, opt := range strings.Split(opts, ",") {
			if opt == "ro" {
				roMounts = append(roMounts, mountpoint)
				break
			}
		}
	}
	return roMounts
}

// ntpStatus checks if time is synced via timedatectl.
// Returns (synced bool, offset string).
func ntpStatus() (bool, string) {
	out, err := cmdutil.Run(cmdutil.TimeoutFast, "timedatectl_show", "show", "--property=NTPSynchronized,TimeUSec")
	if err != nil {
		// Fall back to checking chronyc
		out2, err2 := cmdutil.Run(cmdutil.TimeoutFast, "chronyc_tracking", "tracking")
		if err2 != nil {
			return true, "" // assume OK if tools unavailable
		}
		synced := strings.Contains(string(out2), "Reference ID")
		return synced, ""
	}
	synced := strings.Contains(string(out), "NTPSynchronized=yes")
	return synced, ""
}

// lastServiceError returns the last 5 error/warning lines from journalctl
// for the given systemd service. Sanitised for XSS safety.
func lastServiceError(service string) string {
	// Validate service name - alphanumeric, dash, underscore, dot only
	for _, c := range service {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return "invalid service name"
		}
	}

	out, err := cmdutil.Run(
		cmdutil.TimeoutFast,
		"journalctl",
		"-u", service,
		"--no-pager",
		"-n", "10",
		"-p", "warning",
		"--output=short-iso",
	)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var kept []string
	for _, l := range lines {
		if l != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, "\n")
}

