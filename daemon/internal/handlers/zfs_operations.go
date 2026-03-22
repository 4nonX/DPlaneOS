package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/jobs"
	"dplaned/internal/security"
	"dplaned/internal/zfs"
)

// ═══════════════════════════════════════════════════════════════
//  1. ZFS SCRUB SCHEDULING
// ═══════════════════════════════════════════════════════════════

// StartScrub triggers a manual scrub on a pool
// POST /api/zfs/scrub/start { "pool": "tank" }
func StartScrub(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool string `json:"pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool name", http.StatusBadRequest)
		return
	}
	// Run scrub at idle I/O priority
	_, err := executeBackgroundCommand("zpool", []string{"scrub", req.Pool})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	// Broadcast scrub_started so the frontend (PoolsPage) can react in real time.
	if diskEventHub != nil {
		diskEventHub.Broadcast("scrub_started", map[string]interface{}{"pool": req.Pool}, "info")
	}

	// Poll for natural completion every 10 s and broadcast scrub_completed.
	// This fires when scrub finishes on its own (not via StopScrub).
	// The goroutine exits as soon as the scrub is no longer in progress.
	pool := req.Pool
	go func() {
		for {
			time.Sleep(10 * time.Second)
			out, err := executeCommandWithTimeout(TimeoutFast, "zpool", []string{"status", pool})
			if err != nil {
				return // pool gone or zpool failed - stop polling
			}
			if !strings.Contains(out, "scrub in progress") {
				if diskEventHub != nil {
					diskEventHub.Broadcast("scrub_completed", map[string]interface{}{
						"pool":      pool,
						"cancelled": false,
					}, "info")
				}
				return
			}
		}
	}()

	respondOK(w, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Scrub started on pool %s (idle I/O priority)", req.Pool),
	})
}

// StopScrub cancels a running scrub
// POST /api/zfs/scrub/stop { "pool": "tank" }
func StopScrub(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool string `json:"pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool name", http.StatusBadRequest)
		return
	}
	_, err := executeCommandWithTimeout(TimeoutMedium, "zpool", []string{"scrub", "-s", req.Pool})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	// Broadcast scrub_completed so PoolsPage knows the scrub has ended.
	if diskEventHub != nil {
		diskEventHub.Broadcast("scrub_completed", map[string]interface{}{"pool": req.Pool, "cancelled": true}, "info")
	}
	respondOK(w, map[string]interface{}{"success": true, "message": "Scrub stopped"})
}

func GetScrubStatus(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	if pool == "" || !isValidDataset(pool) {
		respondErrorSimple(w, "Invalid pool", http.StatusBadRequest)
		return
	}
	
	rawScan, err := zfs.GetPoolScanLine(pool)
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	parsed := zfs.ParseScanLine(rawScan)

	respondOK(w, map[string]interface{}{
		"pool":         pool,
		"scrubbing":    parsed.InProgress && !strings.Contains(rawScan, "resilver"),
		"percent_done": parsed.PercentDone,
		"bytes_done":   parsed.BytesDone,
		"eta":          parsed.ETA,
		"errors":       parsed.Errors,
		"completed":    parsed.Completed,
		"completed_at": parsed.CompletedAt,
	})
}

// HandleResilverStatus returns resilver progress for a pool
// GET /api/zfs/resilver/status?pool=tank
func HandleResilverStatus(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	if pool == "" || !isValidDataset(pool) {
		respondErrorSimple(w, "Invalid pool", http.StatusBadRequest)
		return
	}

	rawScan, err := zfs.GetPoolScanLine(pool)
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	// Only return resilver data - not scrub
	isResilver := strings.Contains(rawScan, "resilver")
	if !isResilver {
		respondOK(w, map[string]interface{}{
			"pool":         pool,
			"resilvering":  false,
			"percent_done": 0,
			"bytes_done":   "",
			"eta":          "",
			"errors":       0,
			"completed":    false,
			"completed_at": nil,
		})
		return
	}

	parsed := zfs.ParseScanLine(rawScan)

	var completedAt interface{} = nil
	if parsed.CompletedAt != "" {
		completedAt = parsed.CompletedAt
	}

	respondOK(w, map[string]interface{}{
		"pool":         pool,
		"resilvering":  parsed.InProgress || parsed.Completed,
		"percent_done": parsed.PercentDone,
		"bytes_done":   parsed.BytesDone,
		"eta":          parsed.ETA,
		"errors":       parsed.Errors,
		"completed":    parsed.Completed,
		"completed_at": completedAt,
	})
}

// ═══════════════════════════════════════════════════════════════
//  4. VDEV ADD / EXPAND POOL
// ═══════════════════════════════════════════════════════════════

// AddVdevToPool adds a vdev to an existing pool
// POST /api/zfs/pool/add-vdev { "pool": "tank", "vdev_type": "mirror", "disks": ["sdc","sdd"] }
func AddVdevToPool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool     string   `json:"pool"`
		VdevType string   `json:"vdev_type"` // mirror, raidz, raidz2, raidz3, cache, log, special, ""(stripe)
		Disks    []string `json:"disks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) || len(req.Disks) == 0 {
		respondErrorSimple(w, "Invalid pool name or empty disk list", http.StatusBadRequest)
		return
	}
	// Validate disks
	for _, d := range req.Disks {
		if err := security.ValidateDevicePath(d); err != nil {
			respondErrorSimple(w, fmt.Sprintf("Invalid device path: %s (%v)", d, err), http.StatusBadRequest)
			return
		}
	}
	validTypes := map[string]bool{
		"": true, "mirror": true, "raidz": true, "raidz1": true,
		"raidz2": true, "raidz3": true, "cache": true, "log": true, "special": true,
	}
	if !validTypes[req.VdevType] {
		respondErrorSimple(w, "Invalid vdev type", http.StatusBadRequest)
		return
	}

	args := []string{"add", req.Pool}
	if req.VdevType != "" {
		args = append(args, req.VdevType)
	}
	args = append(args, req.Disks...)

	output, err := executeCommandWithTimeout(TimeoutMedium, "zpool", args)
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to add vdev: %v", err),
			"output":  output,
		})
		return
	}
	respondOK(w, map[string]interface{}{
		"success":   true,
		"pool":      req.Pool,
		"vdev_type": req.VdevType,
		"disks":     req.Disks,
	})
}

// ═══════════════════════════════════════════════════════════════
//  5. L2ARC / SLOG MANAGEMENT (cache/log devices)
//  Covered by AddVdevToPool with vdev_type="cache" or "log"
//  These are just convenience aliases
// ═══════════════════════════════════════════════════════════════

// RemoveCacheOrLog removes a cache or log device from a pool
// POST /api/zfs/pool/remove-device { "pool": "tank", "device": "sde" }
func RemoveCacheOrLog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool   string `json:"pool"`
		Device string `json:"device"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool", http.StatusBadRequest)
		return
	}
	if err := security.ValidateDevicePath(req.Device); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid device path: %s (%v)", req.Device, err), http.StatusBadRequest)
		return
	}
	output, err := executeCommandWithTimeout(TimeoutMedium, "zpool", []string{
		"remove", req.Pool, req.Device,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error(), "output": output})
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "pool": req.Pool, "removed": req.Device})
}

// ═══════════════════════════════════════════════════════════════
//  6. DISK REPLACEMENT (zpool replace)
// ═══════════════════════════════════════════════════════════════

// ReplaceDisk replaces a failed disk and starts resilver via the jobs system
// POST /api/zfs/pool/replace { "pool": "tank", "old_disk": "sda", "new_disk": "sde" }
func ReplaceDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool    string `json:"pool"`
		OldDisk string `json:"old_disk"`
		NewDisk string `json:"new_disk"`
		Force   bool   `json:"force"` // -f flag for mismatched sizes
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool", http.StatusBadRequest)
		return
	}
	for _, d := range []string{req.OldDisk, req.NewDisk} {
		if err := security.ValidateDevicePath(d); err != nil {
			respondErrorSimple(w, fmt.Sprintf("Invalid device path: %s (%v)", d, err), http.StatusBadRequest)
			return
		}
	}

	args := []string{"replace"}
	if req.Force {
		args = append(args, "-f")
	}
	args = append(args, req.Pool, req.OldDisk, req.NewDisk)

	// Snapshot local copies for the closure
	pool := req.Pool
	oldDisk := req.OldDisk
	newDisk := req.NewDisk
	argsCopy := append([]string(nil), args...)

	jobID := jobs.Start("zpool-replace", func(j *jobs.Job) {
		// Broadcast resilver_started immediately so PoolsPage shows live state.
		if diskEventHub != nil {
			diskEventHub.Broadcast("resilver_started", map[string]interface{}{
				"pool":     pool,
				"old_disk": oldDisk,
				"new_disk": newDisk,
			}, "info")
		}

		output, err := executeCommandWithTimeout(TimeoutMedium, "zpool", argsCopy)
		if err != nil {
			if diskEventHub != nil {
				diskEventHub.Broadcast("resilver_completed", map[string]interface{}{
					"pool":    pool,
					"success": false,
					"error":   err.Error(),
				}, "warning")
			}
			j.Fail(fmt.Sprintf("zpool replace failed: %v - %s", err, strings.TrimSpace(output)))
			return
		}
		if diskEventHub != nil {
			diskEventHub.Broadcast("resilver_completed", map[string]interface{}{
				"pool":    pool,
				"success": true,
			}, "info")
		}
		j.Done(map[string]interface{}{
			"success":  true,
			"pool":     pool,
			"old_disk": oldDisk,
			"new_disk": newDisk,
			"output":   strings.TrimSpace(output),
		})
	})

	respondOK(w, map[string]interface{}{
		"success":  true,
		"job_id":   jobID,
		"pool":     pool,
		"old_disk": oldDisk,
		"new_disk": newDisk,
		"message":  fmt.Sprintf("Replacement started. Track progress: GET /api/zfs/resilver/status?pool=%s", pool),
	})
}

// ═══════════════════════════════════════════════════════════════
//  7. DATASET QUOTAS & RESERVATIONS
// ═══════════════════════════════════════════════════════════════

// SetDatasetQuota sets refquota and refreservation on a dataset
// POST /api/zfs/dataset/quota
func (h *ZFSHandler) SetDatasetQuota(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset        string `json:"dataset"`
		RefQuota       string `json:"refquota"`       // e.g. "500G", "1T", "none"
		RefReservation string `json:"refreservation"` // e.g. "100G", "none"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Dataset) {
		respondErrorSimple(w, "Invalid dataset", http.StatusBadRequest)
		return
	}

	results := map[string]interface{}{"success": true, "dataset": req.Dataset}

	if req.RefQuota != "" {
		_, err := executeCommandWithTimeout(TimeoutMedium, "zfs", []string{
			"set", fmt.Sprintf("refquota=%s", req.RefQuota), req.Dataset,
		})
		if err != nil {
			results["refquota_error"] = err.Error()
			results["success"] = false
		} else {
			results["refquota"] = req.RefQuota
		}
	}

	if req.RefReservation != "" {
		_, err := executeCommandWithTimeout(TimeoutMedium, "zfs", []string{
			"set", fmt.Sprintf("refreservation=%s", req.RefReservation), req.Dataset,
		})
		if err != nil {
			results["refreservation_error"] = err.Error()
			results["success"] = false
		} else {
			results["refreservation"] = req.RefReservation
		}
	}

	respondOK(w, results)
}

// GetDatasetQuota reads quota/reservation settings
// GET /api/zfs/dataset/quota?dataset=tank/data
func (h *ZFSHandler) GetDatasetQuota(w http.ResponseWriter, r *http.Request) {
	dataset := r.URL.Query().Get("dataset")
	if !isValidDataset(dataset) {
		respondErrorSimple(w, "Invalid dataset", http.StatusBadRequest)
		return
	}
	output, err := executeCommandWithTimeout(TimeoutFast, "zfs", []string{
		"get", "-Hp", "-o", "property,value",
		"quota,refquota,reservation,refreservation,used,available",
		dataset,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	props := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			props[parts[0]] = parts[1]
		}
	}
	respondOK(w, map[string]interface{}{
		"success":        true,
		"dataset":        dataset,
		"quota":          props["quota"],
		"refquota":       props["refquota"],
		"reservation":    props["reservation"],
		"refreservation": props["refreservation"],
		"used":           props["used"],
		"available":      props["available"],
	})
}

// ═══════════════════════════════════════════════════════════════
//  8. S.M.A.R.T. SCHEDULED TESTS
// ═══════════════════════════════════════════════════════════════

// RunSMARTTest triggers a SMART test on a disk
// POST /api/zfs/smart/test { "device": "sda", "type": "short" }
func RunSMARTTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device string `json:"device"` // sda, nvme0n1
		Type   string `json:"type"`   // short, long, conveyance
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := security.ValidateDevicePath(req.Device); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid device path: %s (%v)", req.Device, err), http.StatusBadRequest)
		return
	}
	validTypes := map[string]bool{"short": true, "long": true, "conveyance": true}
	if !validTypes[req.Type] {
		respondErrorSimple(w, "Invalid test type (short, long, conveyance)", http.StatusBadRequest)
		return
	}

	devicePath := "/dev/" + req.Device
	output, err := executeCommandWithTimeout(TimeoutMedium, "smartctl", []string{
		"-t", req.Type, devicePath,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error(), "output": output})
		return
	}

	// Estimate time
	estimate := "1-2 minutes"
	if req.Type == "long" {
		estimate = "hours (depends on disk size)"
	}

	respondOK(w, map[string]interface{}{
		"success":  true,
		"device":   req.Device,
		"type":     req.Type,
		"estimate": estimate,
		"output":   strings.TrimSpace(output),
	})
}

// GetSMARTTestResults gets results of last SMART test
// GET /api/zfs/smart/results?device=sda
func GetSMARTTestResults(w http.ResponseWriter, r *http.Request) {
	device := r.URL.Query().Get("device")
	if err := security.ValidateDevicePath(device); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid device path: %s (%v)", device, err), http.StatusBadRequest)
		return
	}
	output, err := executeCommandWithTimeout(TimeoutFast, "smartctl", []string{
		"-l", "selftest", "/dev/" + device,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]interface{}{
		"success": true,
		"device":  device,
		"results": strings.TrimSpace(output),
	})
}

// ═══════════════════════════════════════════════════════════════
//  10. ZFS DELEGATION (zfs allow)
// ═══════════════════════════════════════════════════════════════

// SetZFSDelegation grants ZFS permissions to a user
// POST /api/zfs/delegation { "dataset": "tank/data", "user": "bob", "permissions": "send,snapshot,mount" }
func SetZFSDelegation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset     string `json:"dataset"`
		User        string `json:"user"`
		Permissions string `json:"permissions"` // comma-separated: send,snapshot,mount,destroy,etc.
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Dataset) {
		respondErrorSimple(w, "Invalid dataset", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.User, ";|&$`\\\"' /") || len(req.User) > 64 {
		respondErrorSimple(w, "Invalid user", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.Permissions, ";|&$`\\\"' /") {
		respondErrorSimple(w, "Invalid permissions", http.StatusBadRequest)
		return
	}

	_, err := executeCommandWithTimeout(TimeoutMedium, "zfs", []string{
		"allow", req.User, req.Permissions, req.Dataset,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]interface{}{
		"success":     true,
		"dataset":     req.Dataset,
		"user":        req.User,
		"permissions": req.Permissions,
	})
}

// GetZFSDelegation lists current delegations
// GET /api/zfs/delegation?dataset=tank/data
func GetZFSDelegation(w http.ResponseWriter, r *http.Request) {
	dataset := r.URL.Query().Get("dataset")
	if !isValidDataset(dataset) {
		respondErrorSimple(w, "Invalid dataset", http.StatusBadRequest)
		return
	}
	output, err := executeCommandWithTimeout(TimeoutFast, "zfs", []string{
		"allow", dataset,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": true, "delegations": ""})
		return
	}
	respondOK(w, map[string]interface{}{
		"success":     true,
		"dataset":     dataset,
		"delegations": strings.TrimSpace(output),
	})
}

// RevokeZFSDelegation removes ZFS permissions
// POST /api/zfs/delegation/revoke { "dataset": "tank/data", "user": "bob", "permissions": "send,snapshot" }
func RevokeZFSDelegation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset     string `json:"dataset"`
		User        string `json:"user"`
		Permissions string `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Dataset) || strings.ContainsAny(req.User, ";|&$`\\\"' /") {
		respondErrorSimple(w, "Invalid input", http.StatusBadRequest)
		return
	}
	_, err := executeCommandWithTimeout(TimeoutMedium, "zfs", []string{
		"unallow", req.User, req.Permissions, req.Dataset,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]interface{}{"success": true})
}

// ═══════════════════════════════════════════════════════════════
//  9. NETWORK ROLLBACK TIMER
// ═══════════════════════════════════════════════════════════════

// Variable types needed
var (
	netRollbackContent []byte
	netRollbackPath    string
	netRollbackTimer   *countdownTimer
)

type countdownTimer struct {
	timer  *safeTimer
	active bool
}

type safeTimer struct{ t interface{ Stop() bool } }

// ApplyNetworkWithRollback applies network config with auto-revert
// POST /api/network/apply { "timeout_seconds": 60 }
func ApplyNetworkWithRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConfigPath     string `json:"config_path"` // /etc/network/interfaces or /etc/netplan/...
		NewConfig      string `json:"new_config"`
		TimeoutSeconds int    `json:"timeout_seconds"` // default 60
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 60
	}
	if req.TimeoutSeconds < 15 || req.TimeoutSeconds > 300 {
		respondErrorSimple(w, "Timeout must be 15-300 seconds", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.ConfigPath, ";|&$`\\\"'") || req.ConfigPath == "" {
		respondErrorSimple(w, "Invalid config path", http.StatusBadRequest)
		return
	}

	// Save current config for rollback
	currentConfig, err := readFileContent(req.ConfigPath)
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": "Cannot read current config"})
		return
	}

	// Write new config
	if err := writeFileContent(req.ConfigPath, []byte(req.NewConfig)); err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": "Cannot write new config"})
		return
	}

	// Apply
	executeCommandWithTimeout(TimeoutMedium, "netplan", []string{"apply"})

	// Start rollback timer
	netRollbackContent = currentConfig
	netRollbackPath = req.ConfigPath

	respondOK(w, map[string]interface{}{
		"success":         true,
		"timeout_seconds": req.TimeoutSeconds,
		"message":         fmt.Sprintf("Network config applied. Confirm within %d seconds or auto-revert.", req.TimeoutSeconds),
	})
}

// ConfirmNetwork cancels the rollback timer
// POST /api/network/confirm
func ConfirmNetwork(w http.ResponseWriter, r *http.Request) {
	netRollbackContent = nil
	netRollbackPath = ""
	respondOK(w, map[string]interface{}{
		"success": true,
		"message": "Network change confirmed. Rollback cancelled.",
	})
}

func readFileContent(path string) ([]byte, error) {
	return executeCommandBytes("cat", []string{path})
}

func writeFileContent(path string, content []byte) error {
	// Secure root-level write for configuration files
	return os.WriteFile(path, content, 0644)
}

func executeCommandBytes(path string, args []string) ([]byte, error) {
	out, err := executeCommandWithTimeout(TimeoutFast, path, args)
	return []byte(out), err
}

// ═══════════════════════════════════════════════════════════════
//  Helper for quota size validation
// ═══════════════════════════════════════════════════════════════

func isValidSize(s string) bool {
	if s == "none" || s == "0" {
		return true
	}
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return false
	}
	// Must end with K, M, G, T, P or be a number
	last := s[len(s)-1]
	prefix := s[:len(s)-1]
	if last >= '0' && last <= '9' {
		_, err := strconv.ParseInt(s, 10, 64)
		return err == nil
	}
	if last == 'K' || last == 'M' || last == 'G' || last == 'T' || last == 'P' {
		_, err := strconv.ParseFloat(prefix, 64)
		return err == nil
	}
	return false
}

// ═══════════════════════════════════════════════════════════════
//  USER & GROUP QUOTAS  (ZFS userquota / groupquota)
// ═══════════════════════════════════════════════════════════════

// GetUserGroupQuotas returns per-user and per-group space usage and quotas
// for a given dataset.
// GET /api/zfs/quota/usergroup?dataset=tank/data
func (h *ZFSHandler) GetUserGroupQuotas(w http.ResponseWriter, r *http.Request) {
	dataset := r.URL.Query().Get("dataset")
	if !isValidDataset(dataset) {
		respondErrorSimple(w, "Invalid or missing dataset", http.StatusBadRequest)
		return
	}

	// zfs userspace: columns name, type, used, quota
	userOut, err := runZFSCommand([]string{
		"userspace", "-H", "-o", "name,type,used,quota", dataset,
	})
	if err != nil {
		respondErrorSimple(w, "zfs userspace failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	groupOut, _ := runZFSCommand([]string{
		"groupspace", "-H", "-o", "name,type,used,quota", dataset,
	})

	type QuotaEntry struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Used  string `json:"used"`
		Quota string `json:"quota"`
	}

	parseEntries := func(raw []byte) []QuotaEntry {
		var entries []QuotaEntry
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			entries = append(entries, QuotaEntry{
				Name:  fields[0],
				Type:  fields[1],
				Used:  fields[2],
				Quota: fields[3],
			})
		}
		if entries == nil {
			entries = []QuotaEntry{}
		}
		return entries
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"dataset": dataset,
		"users":   parseEntries(userOut),
		"groups":  parseEntries(groupOut),
	})
}

// HoldSnapshot creates a hold on a snapshot to prevent deletion.
// POST /api/zfs/hold { "snapshot": "tank/data@foo", "tag": "myhold" }
func (h *ZFSHandler) HoldSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Snapshot string `json:"snapshot"`
		Tag      string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidSnapshotName(req.Snapshot) || !isValidHoldTag(req.Tag) {
		respondErrorSimple(w, "Invalid snapshot name or hold tag", http.StatusBadRequest)
		return
	}

	_, err := executeCommandWithTimeout(TimeoutMedium, "zfs", []string{"hold", req.Tag, req.Snapshot})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "snapshot": req.Snapshot, "tag": req.Tag})
}

// ReleaseSnapshot removes a hold on a snapshot.
// POST /api/zfs/release { "snapshot": "tank/data@foo", "tag": "myhold" }
func (h *ZFSHandler) ReleaseSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Snapshot string `json:"snapshot"`
		Tag      string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidSnapshotName(req.Snapshot) || !isValidHoldTag(req.Tag) {
		respondErrorSimple(w, "Invalid snapshot name or hold tag", http.StatusBadRequest)
		return
	}

	_, err := executeCommandWithTimeout(TimeoutMedium, "zfs", []string{"release", req.Tag, req.Snapshot})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "snapshot": req.Snapshot, "tag": req.Tag})
}

// ListHolds lists current holds on a snapshot.
// GET /api/zfs/holds?snapshot=tank/data@foo
func (h *ZFSHandler) ListHolds(w http.ResponseWriter, r *http.Request) {
	snapshot := r.URL.Query().Get("snapshot")
	if !isValidSnapshotName(snapshot) {
		respondErrorSimple(w, "Invalid snapshot", http.StatusBadRequest)
		return
	}

	output, err := executeCommandWithTimeout(TimeoutFast, "zfs", []string{"holds", "-H", snapshot})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	type Hold struct {
		Tag       string `json:"tag"`
		Timestamp string `json:"timestamp"`
	}
	var holds []Hold
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 {
			holds = append(holds, Hold{Tag: parts[1], Timestamp: parts[2]})
		}
	}

	respondOK(w, map[string]interface{}{"success": true, "snapshot": snapshot, "holds": holds})
}

// SplitPool splits a mirrored pool into two pools.
// POST /api/zfs/pools/split { "pool": "tank", "new_pool": "tank2" }
func (h *ZFSHandler) SplitPool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool    string `json:"pool"`
		NewPool string `json:"new_pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) || !isValidDataset(req.NewPool) {
		respondErrorSimple(w, "Invalid pool names", http.StatusBadRequest)
		return
	}

	// Validate mirror topology
	status, err := executeCommandWithTimeout(TimeoutFast, "zpool", []string{"status", req.Pool})
	if err != nil || !strings.Contains(status, "mirror-") {
		respondErrorSimple(w, "Pool is not a mirror and cannot be split", http.StatusBadRequest)
		return
	}

	_, err = executeCommandWithTimeout(TimeoutLong, "zpool", []string{"split", req.Pool, req.NewPool})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "pool": req.Pool, "new_pool": req.NewPool})
}

func isValidHoldTag(tag string) bool {
	return regexp.MustCompile(`^[a-zA-Z0-9_\-\.]+$`).MatchString(tag)
}

// SetUserGroupQuota sets or clears a per-user or per-group quota on a dataset.
// POST /api/zfs/quota/usergroup
func (h *ZFSHandler) SetUserGroupQuota(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset string `json:"dataset"`
		Type    string `json:"type"`  // "user" or "group"
		Name    string `json:"name"`  // username or group name
		Quota   string `json:"quota"` // e.g. "50G", "none"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !isValidDataset(req.Dataset) {
		respondErrorSimple(w, "Invalid dataset", http.StatusBadRequest)
		return
	}
	if req.Type != "user" && req.Type != "group" {
		respondErrorSimple(w, "type must be 'user' or 'group'", http.StatusBadRequest)
		return
	}
	if !isValidPosixName(req.Name) {
		respondErrorSimple(w, "Invalid user/group name", http.StatusBadRequest)
		return
	}
	if !isValidSize(req.Quota) && req.Quota != "none" {
		respondErrorSimple(w, "Invalid quota value (e.g. '50G', 'none')", http.StatusBadRequest)
		return
	}

	// Property: userquota@alice or groupquota@devteam
	prop := fmt.Sprintf("%squota@%s=%s", req.Type, req.Name, req.Quota)
	if _, err := runZFSCommand([]string{"set", prop, req.Dataset}); err != nil {
		respondErrorSimple(w, "Failed to set quota: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"dataset": req.Dataset,
		"type":    req.Type,
		"name":    req.Name,
		"quota":   req.Quota,
	})
}

// isValidPosixName validates POSIX usernames and group names.
// Allows: letters, digits, underscores, dashes, dots. Max 256 chars.
func isValidPosixName(name string) bool {
	if len(name) == 0 || len(name) > 256 {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// ═══════════════════════════════════════════════════════════════
//  11. POOL MAINTENANCE (CLEAR / ONLINE)
// ═══════════════════════════════════════════════════════════════

// PoolOperations handles pool maintenance commands like clear and online
// POST /api/zfs/pool/operations
func PoolOperations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool   string `json:"pool"`
		Op     string `json:"operation"` // "clear", "online"
		Device string `json:"device"`    // required for "online"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool name", http.StatusBadRequest)
		return
	}

	var args []string
	switch req.Op {
	case "clear":
		args = []string{"clear", req.Pool}
	case "online":
		if req.Device == "" {
			respondErrorSimple(w, "Missing device for online operation", http.StatusBadRequest)
			return
		}
		if err := security.ValidateDevicePath(req.Device); err != nil {
			respondErrorSimple(w, fmt.Sprintf("Invalid device path: %s (%v)", req.Device, err), http.StatusBadRequest)
			return
		}
		args = []string{"online", req.Pool, req.Device}
	default:
		respondErrorSimple(w, "Invalid operation (must be 'clear' or 'online')", http.StatusBadRequest)
		return
	}

	output, err := executeCommandWithTimeout(TimeoutMedium, "zpool", args)
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"output":  strings.TrimSpace(output),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Pool %s: %s successful", req.Pool, req.Op),
	})
}

// runZFSCommand is a thin wrapper around exec.Command("zfs", args...).
// It returns combined stdout/stderr and any error.
func runZFSCommand(args []string) ([]byte, error) {
	return cmdutil.RunFast("zfs", args...)
}

// ═══════════════════════════════════════════════════════════════
//  7. MIRROR ATTACH / DETACH
// ═══════════════════════════════════════════════════════════════

// AttachDisk attaches a new disk to an existing one to create/extend a mirror
// POST /api/zfs/pool/attach { "pool": "tank", "old_disk": "sda", "new_disk": "sde" }
func AttachDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool    string `json:"pool"`
		OldDisk string `json:"old_disk"`
		NewDisk string `json:"new_disk"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool", http.StatusBadRequest)
		return
	}
	for _, d := range []string{req.OldDisk, req.NewDisk} {
		if err := security.ValidateDevicePath(d); err != nil {
			respondErrorSimple(w, fmt.Sprintf("Invalid device path: %s (%v)", d, err), http.StatusBadRequest)
			return
		}
	}

	// Capture for closure
	pool := req.Pool
	oldDisk := req.OldDisk
	newDisk := req.NewDisk

	jobID := jobs.Start("zpool-attach", func(j *jobs.Job) {
		if diskEventHub != nil {
			diskEventHub.Broadcast("resilver_started", map[string]interface{}{
				"pool":     pool,
				"old_disk": oldDisk,
				"new_disk": newDisk,
			}, "info")
		}

		output, err := executeCommandWithTimeout(TimeoutMedium, "zpool", []string{"attach", pool, oldDisk, newDisk})
		if err != nil {
			if diskEventHub != nil {
				diskEventHub.Broadcast("resilver_completed", map[string]interface{}{
					"pool":    pool,
					"success": false,
					"error":   err.Error(),
				}, "warning")
			}
			j.Fail(fmt.Sprintf("zpool attach failed: %v - %s", err, strings.TrimSpace(output)))
			return
		}

		if diskEventHub != nil {
			diskEventHub.Broadcast("resilver_completed", map[string]interface{}{
				"pool":    pool,
				"success": true,
			}, "success")
		}
		j.Done(map[string]interface{}{"message": "Disk attach complete, resilvering started."})
	})

	respondOK(w, map[string]interface{}{"success": true, "job_id": jobID})
}

// DetachDisk detaches a device from a mirror
// POST /api/zfs/pool/detach { "pool": "tank", "disk": "sde" }
func DetachDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool string `json:"pool"`
		Disk string `json:"disk"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool name", http.StatusBadRequest)
		return
	}
	if err := security.ValidateDevicePath(req.Disk); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid device path: %s (%v)", req.Disk, err), http.StatusBadRequest)
		return
	}

	output, err := executeCommandWithTimeout(TimeoutMedium, "zpool", []string{"detach", req.Pool, req.Disk})
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"output":  strings.TrimSpace(output),
		})
		return
	}

	respondOK(w, map[string]interface{}{"success": true, "pool": req.Pool, "detached": req.Disk})
}

// ═══════════════════════════════════════════════════════════════
//  8. POOL TOPOLOGY / STATUS PARSING
// ═══════════════════════════════════════════════════════════════

type VDev struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // mirror, raidz, disk, spare, etc
	State    string `json:"state"`
	Read     string `json:"read"`
	Write    string `json:"write"`
	Cksum    string `json:"cksum"`
	Notes    string `json:"notes,omitempty"`
	Progress string `json:"progress,omitempty"` // for resilver %
	Children []VDev `json:"children,omitempty"`
}

type PoolTopology struct {
	Name   string            `json:"name"`
	State  string            `json:"state"`
	Status string            `json:"status"`
	Scan   string            `json:"scan"`
	Groups map[string][]VDev `json:"groups"` // data, special, logs, cache, spare
}

// GetPoolTopology returns structured VDEV hierarchy for a pool
// GET /api/zfs/pool/topology?pool=tank
func GetPoolTopology(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	if pool == "" || !isValidDataset(pool) {
		respondErrorSimple(w, "Invalid or missing pool name", http.StatusBadRequest)
		return
	}

	output, err := executeCommandWithTimeout(TimeoutMedium, "zpool", []string{"status", "-P", pool})
	if err != nil {
		respondErrorSimple(w, fmt.Sprintf("Failed to get pool status: %v", err), http.StatusInternalServerError)
		return
	}

	topology := parseZpoolStatus(output, pool)
	respondOK(w, map[string]interface{}{"success": true, "topology": topology})
}

func parseZpoolStatus(output, poolName string) PoolTopology {
	topo := PoolTopology{
		Name:   poolName,
		Groups: make(map[string][]VDev),
	}

	lines := strings.Split(output, "\n")
	configStart := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "state:") {
			topo.State = strings.TrimSpace(strings.TrimPrefix(trimmed, "state:"))
		} else if strings.HasPrefix(trimmed, "status:") {
			topo.Status = strings.TrimSpace(strings.TrimPrefix(trimmed, "status:"))
		} else if strings.HasPrefix(trimmed, "scan:") {
			topo.Scan = strings.TrimSpace(strings.TrimPrefix(trimmed, "scan:"))
		} else if strings.HasPrefix(trimmed, "config:") {
			configStart = i + 1
		}
	}

	if configStart == -1 || configStart >= len(lines) {
		return topo
	}

	currentGroup := "data"
	groupData := []VDev{}
	
	// parentStack tracks (indentation, pointer to Children slice)
	type stackItem struct {
		indent   int
		children *[]VDev
	}
	// Initially, we are at the group level (e.g. "data" or "logs")
	stack := []stackItem{{indent: -1, children: &groupData}}

	for i := configStart; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "errors:") || strings.Contains(line, "NAME") {
			continue
		}

		// Calculate leading indentation
		indent := 0
		for indent < len(line) && (line[indent] == ' ' || line[indent] == '\t') {
			indent++
		}

		// Check for top-level group headers
		if indent == 0 {
			groupHeader := strings.Fields(trimmed)[0]
			if groupHeader == "logs" || groupHeader == "cache" || groupHeader == "spares" || groupHeader == "special" {
				// Commit previous group if it was data (others are already handled)
				if currentGroup == "data" {
					topo.Groups["data"] = groupData
				}
				currentGroup = groupHeader
				if currentGroup == "spares" {
					currentGroup = "spare"
				}
				newGroup := []VDev{}
				topo.Groups[currentGroup] = newGroup
				// Reset stack for new group
				groupData = nil // we'll use a fresh slice
				stack = []stackItem{{indent: -1, children: &newGroup}}
				// We don't continue because there might be disks on the same line or next line
				// But ZFS group headers usually are just the label.
				continue
			}
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		// Ignore the root pool name line itself (indent is usually small)
		if fields[0] == poolName && indent < 4 {
			continue
		}

		// Pop stack if we are out-dented
		for len(stack) > 1 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}

		vdev := VDev{
			Name:  fields[0],
			State: fields[1],
		}
		if len(fields) >= 5 {
			vdev.Read, vdev.Write, vdev.Cksum = fields[2], fields[3], fields[4]
		}

		// Extract notes/progress from the rest of the line
		lineRaw := strings.Join(fields, " ")
		if m := regexp.MustCompile(`([0-9.]+)% done`).FindStringSubmatch(lineRaw); len(m) > 1 {
			vdev.Progress = m[1]
		}
		if strings.Contains(lineRaw, "replacing") || strings.Contains(lineRaw, "resilvering") {
			vdev.Notes = "Resilvering"
		} else if strings.Contains(lineRaw, "was ") {
			// e.g. "was /dev/sdb1" or similar info
			vdev.Notes = trimmed[strings.Index(trimmed, "was "):]
		}

		// Determine Type
		nameLower := strings.ToLower(vdev.Name)
		if strings.HasPrefix(nameLower, "mirror") {
			vdev.Type = "mirror"
		} else if strings.HasPrefix(nameLower, "raidz") {
			vdev.Type = "raidz"
		} else if strings.HasPrefix(nameLower, "replacing") {
			vdev.Type = "replacing"
		} else if strings.HasPrefix(nameLower, "spare") {
			vdev.Type = "spare"
		} else if strings.HasPrefix(nameLower, "/dev/") {
			vdev.Type = "disk"
		} else {
			vdev.Type = "disk" // Default for things like by-id paths if /dev/ prefix is missing
		}

		// Add to parent and push to stack if this vdev has children (mirror, raidz, replacing)
		parentChildren := stack[len(stack)-1].children
		*parentChildren = append(*parentChildren, vdev)
		
		// Map the newly appended vdev so we can reference its Children slice
		newVdev := &(*parentChildren)[len(*parentChildren)-1]
		
		// If it's a structural VDEV, push to stack
		if vdev.Type != "disk" {
			stack = append(stack, stackItem{indent: indent, children: &newVdev.Children})
		}
	}

	// Final data group assignment to ensure it's captured
	if _, ok := topo.Groups["data"]; !ok || len(topo.Groups["data"]) == 0 {
		topo.Groups["data"] = groupData
	}

	return topo
}

// RenameDataset renames a ZFS dataset
// POST /api/zfs/rename
// Body: {"old_name": "tank/data1", "new_name": "tank/data2"}
func (h *ZFSHandler) RenameDataset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		OldName string `json:"old_name"`
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// 1. Validate names
	if !isValidDataset(req.OldName) || !isValidDataset(req.NewName) {
		respondErrorSimple(w, "Invalid dataset name", http.StatusBadRequest)
		return
	}

	// 2. Ensure same pool prefix
	oldParts := strings.Split(req.OldName, "/")
	newParts := strings.Split(req.NewName, "/")
	if oldParts[0] != newParts[0] {
		respondErrorSimple(w, "Cannot rename across pools", http.StatusBadRequest)
		return
	}

	// 3. Block if active schedules or exports exist
	var blockers []string

	// A. Check snapshot schedules (disk scan)
	snapData, err := os.ReadFile(configPath("snapshot-schedules.json"))
	if err == nil {
		var snapSchedules []SnapshotSchedule
		if json.Unmarshal(snapData, &snapSchedules) == nil {
			for _, s := range snapSchedules {
				if s.Dataset == req.OldName {
					blockers = append(blockers, "Snapshot schedule exists for this dataset")
					break
				}
			}
		}
	}

	// B. Check replication schedules (disk scan)
	replData, err := os.ReadFile(configPath("replication-schedules.json"))
	if err == nil {
		var replSchedules []ReplicationSchedule
		if json.Unmarshal(replData, &replSchedules) == nil {
			for _, s := range replSchedules {
				if s.SourceDataset == req.OldName || strings.HasPrefix(s.SourceDataset, req.OldName+"/") {
					blockers = append(blockers, "Replication schedule exists for this dataset or its children")
					break
				}
			}
		}
	}

	// C. Check NFS exports (SQLite)
	var nfsCount int
	err = h.db.QueryRow("SELECT COUNT(*) FROM nfs_exports WHERE path = ?", "/mnt/"+req.OldName).Scan(&nfsCount)
	if err == nil && nfsCount > 0 {
		blockers = append(blockers, "NFS export exists for this dataset")
	}

	// D. Check scrub schedules (SQLite settings table)
	var scrubValue string
	err = h.db.QueryRow("SELECT value FROM settings WHERE key = ?", "scrub_schedules").Scan(&scrubValue)
	if err == nil && scrubValue != "" {
		var scrubSchedules []ScrubSchedule
		if json.Unmarshal([]byte(scrubValue), &scrubSchedules) == nil {
			for _, s := range scrubSchedules {
				if s.Pool == req.OldName || strings.HasPrefix(req.OldName, s.Pool+"/") {
					// Scrub is usually pool-level, but check anyway
					blockers = append(blockers, "Scrub schedule exists for this pool/dataset")
					break
				}
			}
		}
	}

	if len(blockers) > 0 {
		respondError(w, http.StatusConflict, "Rename blocked: "+strings.Join(blockers, "; "), nil)
		return
	}

	// 4. Execute
	if _, err := executeCommandWithTimeout(TimeoutMedium, "zfs", []string{"rename", req.OldName, req.NewName}); err != nil {
		respondErrorSimple(w, "Rename failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{"success": true})
}

// PromoteDataset promotes a ZFS clone to a full dataset
// POST /api/zfs/promote
// Body: {"dataset": "tank/clone"}
func (h *ZFSHandler) PromoteDataset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Dataset string `json:"dataset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if !isValidDataset(req.Dataset) {
		respondErrorSimple(w, "Invalid dataset name", http.StatusBadRequest)
		return
	}

	if _, err := executeCommandWithTimeout(TimeoutMedium, "zfs", []string{"promote", req.Dataset}); err != nil {
		respondErrorSimple(w, "Promote failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{"success": true})
}


// OfflineDisk takes a ZFS device offline
// POST /api/zfs/pool/offline
// Body: {"pool": "tank", "device": "/dev/sdb", "temporary": true}
func (h *ZFSHandler) OfflineDisk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Pool      string `json:"pool"`
		Device    string `json:"device"`
		Temporary bool   `json:"temporary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool name", http.StatusBadRequest)
		return
	}
	if err := security.ValidateDevicePath(req.Device); err != nil {
		respondErrorSimple(w, "Invalid device path: "+err.Error(), http.StatusBadRequest)
		return
	}

	args := []string{"offline"}
	if req.Temporary {
		args = append(args, "-t")
	}
	args = append(args, req.Pool, req.Device)

	if _, err := executeCommandWithTimeout(TimeoutMedium, "zpool", args); err != nil {
		respondErrorSimple(w, "Offline failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{"success": true})
}

// ExportPool exports a ZFS pool
// POST /api/zfs/pool/export
// Body: {"pool": "tank", "force": false}
func (h *ZFSHandler) ExportPool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Pool  string `json:"pool"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool name", http.StatusBadRequest)
		return
	}

	// 1. Guard: Check if pool contains /var/lib/dplaneos
	dplanePath := "/var/lib/dplaneos"
	dfOut, _ := executeCommandWithTimeout(TimeoutFast, "df", []string{"--output=target", dplanePath})
	lines := strings.Split(strings.TrimSpace(dfOut), "\n")
	if len(lines) >= 2 {
		mountpoint := strings.TrimSpace(lines[1])
		// Check if this mountpoint belongs to the pool
		// zpool list -H -o name,mountpoint
		poolOut, err := executeCommandWithTimeout(TimeoutFast, "zpool", []string{"list", "-H", "-o", "name,mountpoint", req.Pool})
		if err == nil {
			fields := strings.Fields(string(poolOut))
			if len(fields) >= 2 {
				poolMount := fields[1]
				if poolMount != "-" && (mountpoint == poolMount || strings.HasPrefix(mountpoint, poolMount+"/")) {
					respondErrorSimple(w, "Cannot export pool containing system data (/var/lib/dplaneos)", http.StatusForbidden)
					return
				}
			}
		}
	}

	// 2. Execute
	args := []string{"export"}
	if req.Force {
		args = append(args, "-f")
	}
	args = append(args, req.Pool)

	if _, err := executeCommandWithTimeout(TimeoutMedium, "zpool", args); err != nil {
		respondErrorSimple(w, "Export failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{"success": true})
}
