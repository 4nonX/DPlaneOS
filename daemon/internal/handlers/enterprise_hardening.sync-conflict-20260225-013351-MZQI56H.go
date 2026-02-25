package handlers

import (
	"database/sql"

	"dplaned/internal/cmdutil"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ═══════════════════════════════════════════════════════════════
//  POWER-LOSS STATE LOCKS (ZFS User Properties)
//  Prevents async state between Docker and ZFS after power failure
// ═══════════════════════════════════════════════════════════════

// StateLockHandler manages ZFS user properties for crash-safe operations
type StateLockHandler struct{}

func NewStateLockHandler() *StateLockHandler {
	return &StateLockHandler{}
}

// SetLock marks a dataset as having an operation in progress
func SetOperationLock(dataset, operation string) error {
	if !isValidDataset(dataset) {
		return fmt.Errorf("invalid dataset")
	}
	_, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
		"set", fmt.Sprintf("dplane:op_in_progress=%s", operation),
		dataset,
	})
	if err != nil {
		return err
	}
	_, err = executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
		"set", fmt.Sprintf("dplane:op_started=%s", time.Now().Format(time.RFC3339)),
		dataset,
	})
	return err
}

// ClearLock removes the operation lock
func ClearOperationLock(dataset string) error {
	if !isValidDataset(dataset) {
		return fmt.Errorf("invalid dataset")
	}
	executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
		"inherit", "dplane:op_in_progress", dataset,
	})
	executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
		"inherit", "dplane:op_started", dataset,
	})
	return nil
}

// CheckStaleLocks checks all datasets for stale operation locks (from power loss)
// Called at daemon startup
// GET /api/system/stale-locks
func (h *StateLockHandler) CheckStaleLocks(w http.ResponseWriter, r *http.Request) {
	output, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zfs", []string{
		"get", "-H", "-o", "name,value", "-t", "filesystem",
		"-r", "dplane:op_in_progress",
	})

	var staleLocks []map[string]string

	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
			if line == "" {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[1] != "-" && parts[1] != "" {
				staleLocks = append(staleLocks, map[string]string{
					"dataset":   parts[0],
					"operation": parts[1],
				})
			}
		}
	}

	respondOK(w, map[string]interface{}{
		"success":     true,
		"stale_locks": staleLocks,
		"count":       len(staleLocks),
		"hint":        "These datasets had operations interrupted by power loss. Review and clear them.",
	})
}

// ClearStaleLock clears a specific stale lock
// POST /api/system/stale-locks/clear { "dataset": "tank/docker" }
func (h *StateLockHandler) ClearStaleLock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset string `json:"dataset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := ClearOperationLock(req.Dataset); err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Lock cleared on %s", req.Dataset),
	})
}

// ═══════════════════════════════════════════════════════════════
//  FILE BROWSER IGNORE LIST
// ═══════════════════════════════════════════════════════════════

// DefaultIgnorePatterns are hidden by default in file listings
var DefaultIgnorePatterns = []string{
	".DS_Store", ".Thumbs.db", "desktop.ini", ".Spotlight-V100",
	".fseventsd", ".Trashes", ".TemporaryItems", "@eaDir",
	".synology_working", "#recycle", ".stversions",
}

// ShouldIgnoreFile checks if a filename matches ignore patterns
func ShouldIgnoreFile(name string, customPatterns []string) bool {
	patterns := append(DefaultIgnorePatterns, customPatterns...)
	for _, p := range patterns {
		if strings.EqualFold(name, p) {
			return true
		}
		// Simple glob: *.ext
		if strings.HasPrefix(p, "*.") {
			ext := strings.TrimPrefix(p, "*")
			if strings.HasSuffix(strings.ToLower(name), strings.ToLower(ext)) {
				return true
			}
		}
	}
	return false
}

// ═══════════════════════════════════════════════════════════════
//  NIXOS GENERATION DIFF
// ═══════════════════════════════════════════════════════════════

// DiffGenerations shows what changed between two NixOS generations
// GET /api/nixos/diff?from=42&to=43
func (h *NixOSGuardHandler) DiffGenerations(w http.ResponseWriter, r *http.Request) {
	if !isNixOS() {
		respondErrorSimple(w, "Not a NixOS system", http.StatusBadRequest)
		return
	}

	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")

	// Validate numeric
	for _, g := range []string{from, to} {
		for _, c := range g {
			if c < '0' || c > '9' {
				respondErrorSimple(w, "Generation IDs must be numeric", http.StatusBadRequest)
				return
			}
		}
	}

	if to == "" {
		to = "current"
	}

	// Use nix store diff-closures
	fromPath := fmt.Sprintf("/nix/var/nix/profiles/system-%s-link", from)
	toPath := "/run/current-system"
	if to != "current" {
		toPath = fmt.Sprintf("/nix/var/nix/profiles/system-%s-link", to)
	}

	output, err := executeCommandWithTimeout(TimeoutMedium, "/run/current-system/sw/bin/nix", []string{
		"store", "diff-closures", fromPath, toPath,
	})
	if err != nil {
		// Fallback: simple package list diff
		fromPkgs, _ := executeCommandWithTimeout(TimeoutFast, "/bin/ls", []string{fromPath + "/sw/bin/"})
		toPkgs, _ := executeCommandWithTimeout(TimeoutFast, "/bin/ls", []string{toPath + "/sw/bin/"})

		respondOK(w, map[string]interface{}{
			"success":      true,
			"method":       "package-list",
			"from_packages": strings.Fields(fromPkgs),
			"to_packages":   strings.Fields(toPkgs),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"method":  "nix-diff",
		"from":    from,
		"to":      to,
		"diff":    strings.TrimSpace(output),
	})
}
// ═══════════════════════════════════════════════════════════════════════════════
//  PRE-UPGRADE ZFS SNAPSHOT
//  Called before every nixos-rebuild switch. Best-effort: failure is logged
//  but never blocks the apply.
// ═══════════════════════════════════════════════════════════════════════════════

// snapshotAllPoolsPreUpgrade creates a named ZFS snapshot on every ONLINE pool
// immediately before a NixOS config apply. Results are persisted to DB so they
// are visible in the UI and can be used for data recovery.
//
// Returns (snapshotNames, errorMessages). Partial success is normal.
func snapshotAllPoolsPreUpgrade(db *sql.DB, applyTarget string) ([]string, []string) {
	ts := time.Now().UTC().Format("20060102T150405Z")

	poolOut, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zpool", []string{
		"list", "-H", "-o", "name",
	})
	if err != nil {
		return nil, []string{fmt.Sprintf("zpool list failed: %v", err)}
	}

	var snapshots, errs []string
	for _, pool := range strings.Split(strings.TrimSpace(poolOut), "\n") {
		pool = strings.TrimSpace(pool)
		if pool == "" {
			continue
		}
		snapName := fmt.Sprintf("%s@pre-upgrade-%s", pool, ts)

		_, snapErr := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
			"snapshot", snapName,
		})

		success := 1
		errMsg := ""
		if snapErr != nil {
			success = 0
			errMsg = snapErr.Error()
			errs = append(errs, fmt.Sprintf("pool %s: %v", pool, snapErr))
			log.Printf("pre-upgrade snapshot WARN pool=%s err=%v", pool, snapErr)
		} else {
			snapshots = append(snapshots, snapName)
			log.Printf("pre-upgrade snapshot OK: %s", snapName)
		}

		if db != nil {
			if _, dbErr := db.Exec(
				`INSERT INTO pre_upgrade_snapshots (snapshot, pool, nixos_apply, success, error) VALUES (?, ?, ?, ?, ?)`,
				snapName, pool, applyTarget, success, errMsg,
			); dbErr != nil {
				log.Printf("pre-upgrade snapshot DB insert error: %v", dbErr)
			}
		}
	}
	return snapshots, errs
}


// ═══════════════════════════════════════════════════════════════
//  NIXOS BOOT WATCHDOG
//  After nixos-rebuild switch, starts a timer.
//  If not confirmed within deadline, auto-rollback.
// ═══════════════════════════════════════════════════════════════

var (
	watchdogMu       sync.Mutex
	watchdogTimer    *time.Timer
	watchdogActive   bool
	watchdogDeadline time.Time
)

// ApplyWithWatchdog applies a NixOS config with auto-rollback safety
// POST /api/nixos/apply { "flake_path": "/etc/nixos", "timeout_seconds": 120 }
func (h *NixOSGuardHandler) ApplyWithWatchdog(w http.ResponseWriter, r *http.Request) {
	if !isNixOS() {
		respondErrorSimple(w, "Not a NixOS system", http.StatusBadRequest)
		return
	}

	var req struct {
		FlakePath      string `json:"flake_path"`
		TimeoutSeconds int    `json:"timeout_seconds"` // default 120
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 120
	}
	if req.TimeoutSeconds < 30 || req.TimeoutSeconds > 600 {
		respondErrorSimple(w, "Timeout must be 30-600 seconds", http.StatusBadRequest)
		return
	}

	flakePath := req.FlakePath
	if flakePath == "" {
		flakePath = "/etc/nixos"
	}
	if strings.ContainsAny(flakePath, ";|&$`\\\"'") {
		respondErrorSimple(w, "Invalid path", http.StatusBadRequest)
		return
	}

	// ── Pre-upgrade ZFS snapshots (best-effort, never blocks apply) ──────
	applyTarget := flakePath
	if _, statErr := os.Stat(flakePath + "/flake.nix"); statErr != nil {
		applyTarget = "traditional"
	}
	preSnapshots, preSnapErrs := snapshotAllPoolsPreUpgrade(h.db, applyTarget)
	if len(preSnapErrs) > 0 {
		log.Printf("pre-upgrade snapshot warnings (non-fatal): %v", preSnapErrs)
	}

	// Apply the new config
	var output string
	var err error

	flakeNix := flakePath + "/flake.nix"
	if _, statErr := os.Stat(flakeNix); statErr == nil {
		output, err = executeCommand("/run/current-system/sw/bin/nixos-rebuild", []string{
			"switch", "--flake", flakePath + "#dplaneos",
		})
	} else {
		output, err = executeCommand("/run/current-system/sw/bin/nixos-rebuild", []string{
			"switch",
		})
	}

	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":          false,
			"error":            fmt.Sprintf("Apply failed: %v", err),
			"output":           output,
			"pre_snapshots":    preSnapshots,
			"snapshot_errors":  preSnapErrs,
		})
		return
	}

	// Start watchdog timer
	watchdogMu.Lock()
	if watchdogTimer != nil {
		watchdogTimer.Stop()
	}
	deadline := time.Duration(req.TimeoutSeconds) * time.Second
	watchdogDeadline = time.Now().Add(deadline)
	watchdogActive = true
	watchdogTimer = time.AfterFunc(deadline, func() {
		watchdogMu.Lock()
		defer watchdogMu.Unlock()
		if watchdogActive {
			// Auto-rollback — nobody confirmed
			if _, err := cmdutil.RunSlow("/run/current-system/sw/bin/nixos-rebuild", "switch", "--rollback"); err != nil {
			log.Printf("ERROR: nixos rollback failed: %v", err)
		}
			watchdogActive = false
		}
	})
	watchdogMu.Unlock()

	respondOK(w, map[string]interface{}{
		"success":          true,
		"output":           output,
		"watchdog_active":  true,
		"confirm_before":   watchdogDeadline.Format(time.RFC3339),
		"timeout_seconds":  req.TimeoutSeconds,
		"pre_snapshots":    preSnapshots,
		"snapshot_errors":  preSnapErrs,
		"message":          fmt.Sprintf("Config applied. Confirm within %d seconds or auto-rollback.", req.TimeoutSeconds),
	})
}

// ConfirmApply confirms the NixOS config change, cancelling the watchdog
// POST /api/nixos/confirm
func (h *NixOSGuardHandler) ConfirmApply(w http.ResponseWriter, r *http.Request) {
	watchdogMu.Lock()
	defer watchdogMu.Unlock()

	if !watchdogActive {
		respondOK(w, map[string]interface{}{
			"success": true,
			"message": "No pending watchdog to confirm",
		})
		return
	}

	watchdogTimer.Stop()
	watchdogActive = false

	respondOK(w, map[string]interface{}{
		"success": true,
		"message": "Config change confirmed. Watchdog cancelled.",
	})
}

// WatchdogStatus returns current watchdog state
// GET /api/nixos/watchdog
func (h *NixOSGuardHandler) WatchdogStatus(w http.ResponseWriter, r *http.Request) {
	watchdogMu.Lock()
	defer watchdogMu.Unlock()

	remaining := 0
	deadline := ""
	if watchdogActive {
		remaining = int(time.Until(watchdogDeadline).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		deadline = watchdogDeadline.Format(time.RFC3339)
	}

	respondOK(w, map[string]interface{}{
		"success":         true,
		"watchdog_active": watchdogActive,
		"deadline":        deadline,
		"remaining_sec":   remaining,
	})
}

// ═══════════════════════════════════════════════════════════════
//  DOCKER PRE-FLIGHT CHECK
//  Verify ZFS backend before Docker starts
// ═══════════════════════════════════════════════════════════════

// DockerPreFlight verifies Docker and ZFS are in sync
// GET /api/docker/preflight
func (h *DockerHandler) PreFlightCheck(w http.ResponseWriter, r *http.Request) {
	checks := []map[string]interface{}{}

	// Check 1: Docker is running
	_, err := executeCommandWithTimeout(TimeoutFast, "/usr/bin/docker", []string{"info", "--format", "{{.Driver}}"})
	if err != nil {
		checks = append(checks, map[string]interface{}{"check": "docker_running", "pass": false, "error": "Docker daemon not responding"})
	} else {
		checks = append(checks, map[string]interface{}{"check": "docker_running", "pass": true})
	}

	// Check 2: Docker storage driver is ZFS
	driverOut, _ := executeCommandWithTimeout(TimeoutFast, "/usr/bin/docker", []string{"info", "--format", "{{.Driver}}"})
	driver := strings.TrimSpace(driverOut)
	isZFS := driver == "zfs"
	checks = append(checks, map[string]interface{}{
		"check":  "storage_driver_zfs",
		"pass":   isZFS,
		"driver": driver,
	})

	// Check 3: ZFS pools are imported
	poolOut, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zpool", []string{"list", "-H", "-o", "name,health"})
	if err != nil {
		checks = append(checks, map[string]interface{}{"check": "zfs_pools", "pass": false, "error": "No ZFS pools found"})
	} else {
		healthy := !strings.Contains(poolOut, "DEGRADED") && !strings.Contains(poolOut, "FAULTED")
		checks = append(checks, map[string]interface{}{
			"check":   "zfs_pools",
			"pass":    healthy,
			"pools":   strings.TrimSpace(poolOut),
		})
	}

	// Check 4: No stale operation locks
	lockOut, _ := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zfs", []string{
		"get", "-H", "-o", "name,value", "-r", "dplane:op_in_progress",
	})
	hasStale := false
	if lockOut != "" {
		for _, line := range strings.Split(lockOut, "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[1] != "-" && parts[1] != "" {
				hasStale = true
				break
			}
		}
	}
	checks = append(checks, map[string]interface{}{
		"check": "no_stale_locks",
		"pass":  !hasStale,
	})

	allPass := true
	for _, c := range checks {
		if p, ok := c["pass"].(bool); ok && !p {
			allPass = false
		}
	}

	respondOK(w, map[string]interface{}{
		"success":   true,
		"all_pass":  allPass,
		"checks":    checks,
	})
}

// ═══════════════════════════════════════════════════════════════
//  AUDIT LOG ROTATION
// ═══════════════════════════════════════════════════════════════

// AuditRotationHandler manages log rotation
type AuditRotationHandler struct{}

func NewAuditRotationHandler() *AuditRotationHandler {
	return &AuditRotationHandler{}
}

// RotateAuditLogs rotates the audit log in the database
// POST /api/system/audit/rotate { "keep_days": 90 }
func (h *AuditRotationHandler) RotateAuditLogs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeepDays int `json:"keep_days"` // default 90
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if req.KeepDays == 0 {
		req.KeepDays = 90
	}
	if req.KeepDays < 1 {
		respondErrorSimple(w, "keep_days must be >= 1", http.StatusBadRequest)
		return
	}

	// Count before
	countBefore := "unknown"
	if out, err := executeCommandWithTimeout(TimeoutFast, "/usr/bin/sqlite3", []string{
		"/var/lib/dplaneos/dplaneos.db",
		"SELECT COUNT(*) FROM audit_logs;",
	}); err == nil {
		countBefore = strings.TrimSpace(out)
	}

	// Delete old entries
	cutoff := time.Now().AddDate(0, 0, -req.KeepDays).Format("2006-01-02 15:04:05")
	rotDB, dbErr := sql.Open("sqlite3", "/var/lib/dplaneos/dplaneos.db?_journal_mode=WAL&_busy_timeout=30000&cache=shared&_synchronous=FULL")
	var err error
	if dbErr == nil {
		defer rotDB.Close()
		_, err = rotDB.Exec("DELETE FROM audit_logs WHERE timestamp < ?", cutoff)
	} else {
		err = dbErr
	}
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Rotation failed: %v", err),
		})
		return
	}

	// Count after
	countAfter := "unknown"
	if out, err := executeCommandWithTimeout(TimeoutFast, "/usr/bin/sqlite3", []string{
		"/var/lib/dplaneos/dplaneos.db",
		"SELECT COUNT(*) FROM audit_logs;",
	}); err == nil {
		countAfter = strings.TrimSpace(out)
	}

	respondOK(w, map[string]interface{}{
		"success":      true,
		"keep_days":    req.KeepDays,
		"cutoff":       cutoff,
		"before_count": countBefore,
		"after_count":  countAfter,
	})
}

// GetAuditStats returns audit log statistics
// GET /api/system/audit/stats
func (h *AuditRotationHandler) GetAuditStats(w http.ResponseWriter, r *http.Request) {
	dbPath := "/var/lib/dplaneos/dplaneos.db"

	count := "0"
	if out, err := executeCommandWithTimeout(TimeoutFast, "/usr/bin/sqlite3", []string{
		dbPath, "SELECT COUNT(*) FROM audit_logs;",
	}); err == nil {
		count = strings.TrimSpace(out)
	}

	oldest := "N/A"
	if out, err := executeCommandWithTimeout(TimeoutFast, "/usr/bin/sqlite3", []string{
		dbPath, "SELECT MIN(timestamp) FROM audit_logs;",
	}); err == nil {
		oldest = strings.TrimSpace(out)
	}

	newest := "N/A"
	if out, err := executeCommandWithTimeout(TimeoutFast, "/usr/bin/sqlite3", []string{
		dbPath, "SELECT MAX(timestamp) FROM audit_logs;",
	}); err == nil {
		newest = strings.TrimSpace(out)
	}

	dbSize := "unknown"
	if fi, err := os.Stat(dbPath); err == nil {
		dbSize = humanizeBytes(fi.Size())
	}

	respondOK(w, map[string]interface{}{
		"success":       true,
		"total_entries": count,
		"oldest_entry":  oldest,
		"newest_entry":  newest,
		"db_size":       dbSize,
	})
}

// ═══════════════════════════════════════════════════════════════
//  ZOMBIE DISK WATCHER
//  Monitors ZFS command latency, flags slow-dying disks
// ═══════════════════════════════════════════════════════════════

// ZombieWatcherHandler detects slow-responding disks
type ZombieWatcherHandler struct{}

func NewZombieWatcherHandler() *ZombieWatcherHandler {
	return &ZombieWatcherHandler{}
}

// CheckDiskLatency measures response time of each disk in the pool
// GET /api/zfs/disk-latency
func (h *ZombieWatcherHandler) CheckDiskLatency(w http.ResponseWriter, r *http.Request) {
	// Get list of pool disks
	zpoolOutput, _ := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zpool", []string{"status"})
	disks := extractDiskDevices(zpoolOutput)

	type DiskLatency struct {
		Device  string `json:"device"`
		Latency int64  `json:"latency_ms"`
		State   string `json:"state"` // ok, slow, zombie
	}

	var results []DiskLatency
	for _, disk := range disks {
		devicePath := "/dev/" + disk
		start := time.Now()
		// Simple read test: hdparm -t (1 second timed read)
		_, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/hdparm", []string{
			"-t", "--direct", devicePath,
		})
		latency := time.Since(start).Milliseconds()

		state := "ok"
		if err != nil {
			state = "zombie"
		} else if latency > 5000 {
			state = "zombie"
		} else if latency > 2000 {
			state = "slow"
		}

		results = append(results, DiskLatency{
			Device:  disk,
			Latency: latency,
			State:   state,
		})
	}

	// Determine overall status
	hasZombie := false
	hasSlow := false
	for _, d := range results {
		if d.State == "zombie" {
			hasZombie = true
		}
		if d.State == "slow" {
			hasSlow = true
		}
	}

	overall := "ok"
	if hasZombie {
		overall = "zombie"
	} else if hasSlow {
		overall = "slow"
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"overall": overall,
		"disks":   results,
		"count":   len(results),
	})
}

// ═══════════════════════════════════════════════════════════════
//  S.M.A.R.T. VENDOR ATTRIBUTE TRANSLATION
// ═══════════════════════════════════════════════════════════════

// CommonSMARTWarnings maps well-known attribute IDs to human-readable warnings
var CommonSMARTWarnings = map[int]string{
	5:   "Reallocated Sectors — disk is remapping bad sectors (early failure sign)",
	10:  "Spin Retry Count — disk motor struggling (mechanical wear)",
	184: "End-to-End Error — data path integrity failure",
	187: "Reported Uncorrectable Errors — unrecoverable read errors",
	188: "Command Timeout — disk not responding to commands in time",
	196: "Reallocation Event Count — how often sectors were remapped",
	197: "Current Pending Sector — sectors waiting to be remapped (bad sign)",
	198: "Offline Uncorrectable — sectors that failed offline testing",
	199: "UDMA CRC Error Count — cable or controller issues",
	201: "Soft Read Error Rate — read retries needed (pre-failure)",
}

// TranslateSMARTAttribute returns a human-readable warning for a SMART attribute
func TranslateSMARTAttribute(id int, rawValue int64) (string, string) {
	if warning, ok := CommonSMARTWarnings[id]; ok {
		severity := "info"
		if rawValue > 0 {
			severity = "warning"
			if id == 5 || id == 197 || id == 198 {
				if rawValue > 10 {
					severity = "critical"
				}
			}
		}
		return warning, severity
	}
	return "", ""
}

// ═══════════════════════════════════════════════════════════════
//  HELPER: ignore patterns for file listings
// ═══════════════════════════════════════════════════════════════

// FilterIgnoredFiles removes ignored files from a directory listing
func FilterIgnoredFiles(entries []SnapshotEntry, customIgnore []string) []SnapshotEntry {
	var filtered []SnapshotEntry
	for _, e := range entries {
		if !ShouldIgnoreFile(e.Name, customIgnore) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// FilterDirEntries removes ignored files from os.ReadDir results
func FilterDirEntries(entries []os.DirEntry, customIgnore []string) []os.DirEntry {
	var filtered []os.DirEntry
	for _, e := range entries {
		if !ShouldIgnoreFile(e.Name(), customIgnore) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// Ensure filepath is imported
var _ = filepath.Clean
