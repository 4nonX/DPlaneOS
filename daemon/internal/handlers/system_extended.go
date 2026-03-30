package handlers

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"context"
	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/config"
	"dplaned/internal/systemd"

	"dplaned/internal/jobs"
	"io"
	"net"
	"github.com/google/uuid"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
	"crypto/x509"
	"encoding/pem"
)

// ============================================================
// SNAPSHOT SCHEDULER
// ============================================================

type SnapshotScheduleHandler struct{}

func NewSnapshotScheduleHandler() *SnapshotScheduleHandler {
	return &SnapshotScheduleHandler{}
}

type SnapshotSchedule struct {
	Dataset       string `json:"dataset"`
	Frequency     string `json:"frequency"`      // hourly, daily, weekly, monthly
	Retention     int    `json:"retention"`      // number of snapshots to keep (count-based)
	RetentionDays int    `json:"retention_days"` // destroy snapshots older than N days (0 = disabled)
	Enabled       bool   `json:"enabled"`
	Prefix        string `json:"prefix"` // auto-hourly, auto-daily, etc.
	LastRun       string `json:"last_run,omitempty"`
}

// ConfigDir is the base directory for D-PlaneOS config files.
// Default: /etc/dplaneos (Debian), override with: /var/lib/dplaneos/config (NixOS)
var ConfigDir = "/etc/dplaneos"

// SetConfigDir allows main.go to override the config directory at startup
func SetConfigDir(dir string) {
	ConfigDir = dir
	os.MkdirAll(ConfigDir, 0755)
	os.MkdirAll(ConfigDir+"/ssl", 0755)
}

func configPath(filename string) string {
	return ConfigDir + "/" + filename
}

const scheduleFile_deprecated = "" // replaced by configPath("snapshot-schedules.json")

func (h *SnapshotScheduleHandler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(configPath("snapshot-schedules.json"))
	if err != nil {
		// No schedules yet - return empty array
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "schedules": []SnapshotSchedule{}})
		return
	}

	var schedules []SnapshotSchedule
	if err := json.Unmarshal(data, &schedules); err != nil {
		respondErrorSimple(w, "Failed to parse schedules", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "schedules": schedules})
}

func (h *SnapshotScheduleHandler) SaveSchedules(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var schedules []SnapshotSchedule
	if err := json.NewDecoder(r.Body).Decode(&schedules); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate datasets and prefixes - alphanumeric, underscores, hyphens, slashes, dots, @ (snapshots)
	datasetPattern := regexp.MustCompile(`^[a-zA-Z0-9_\-/.@]+$`)
	prefixPattern := regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)
	for _, s := range schedules {
		if !datasetPattern.MatchString(s.Dataset) {
			respondErrorSimple(w, "Invalid dataset name: "+s.Dataset, http.StatusBadRequest)
			return
		}
		if s.Prefix != "" && !prefixPattern.MatchString(s.Prefix) {
			respondErrorSimple(w, "Invalid prefix: "+s.Prefix, http.StatusBadRequest)
			return
		}
		if s.Retention < 1 || s.Retention > 1000 {
			respondErrorSimple(w, "Retention must be 1-1000", http.StatusBadRequest)
			return
		}
	}

	// Save to file
	os.MkdirAll(ConfigDir, 0755)
	data, _ := json.MarshalIndent(schedules, "", "  ")
	if err := os.WriteFile(configPath("snapshot-schedules.json"), data, 0644); err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}

	// Regenerate crontab entries
	h.regenerateCron(schedules)

	audit.LogAction("snapshot_schedule", user, "Updated snapshot schedules", true, 0)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (h *SnapshotScheduleHandler) regenerateCron(schedules []SnapshotSchedule) {
	// 1. Clear existing snapshot timers
	if err := systemd.UninstallAllWithPrefix("dplaneos-snap-"); err != nil {
		log.Printf("ERROR: failed to clear existing snapshot timers: %v", err)
	}

	// Remove legacy cron file
	os.Remove("/etc/cron.d/dplaneos-snapshots")

	for _, s := range schedules {
		if !s.Enabled {
			continue
		}
		prefix := s.Prefix
		if prefix == "" {
			prefix = "auto-" + s.Frequency
		}

		var onCalendar string
		switch s.Frequency {
		case "hourly":
			onCalendar = "*-*-* *:00:00"
		case "daily":
			onCalendar = "*-*-* 02:00:00"
		case "weekly":
			onCalendar = "Sun *-*-* 03:00:00"
		case "monthly":
			onCalendar = "*-*-01 04:00:00"
		default:
			continue
		}

		// Use the cron-hook internal endpoint. 
		// We wrap it in a shell script that can also do the standalone pruning if needed.
		// Use json.Marshal to ensure the payload is safe for insertion into a shell string
		payloadObj := map[string]interface{}{
			"dataset":        s.Dataset,
			"prefix":         prefix,
			"retention":      s.Retention,
			"retention_days": s.RetentionDays,
		}
		payloadBytes, _ := json.Marshal(payloadObj)
		payload := string(payloadBytes)
		
		// The hook handles both snapshotting and replication.
		// Note: Finding 34: mainCmd uses single quotes around the payload. 
		// We already validated prefix and dataset, but for extra safety, 
		// we escape any single quotes in the json payload (though there shouldn't be any now).
		safePayload := strings.ReplaceAll(payload, "'", "'\\''")
		mainCmd := fmt.Sprintf(
			`curl -sf -X POST http://127.0.0.1:9000/api/zfs/snapshots/cron-hook -H 'Content-Type: application/json' -H 'X-Internal-Token: dplaneos-internal-reconciliation-secret-v1' -d '%s'`,
			safePayload,
		)

		// Sanitize dataset name for unit file (no / allowed)
		safeDataset := strings.ReplaceAll(s.Dataset, "/", "-")
		unitName := fmt.Sprintf("snap-%s-%s", safeDataset, s.Frequency)

		err := systemd.InstallTimer(systemd.TimerConfig{
			Name:        unitName,
			Description: fmt.Sprintf("ZFS Auto-Snapshot for %s (%s)", s.Dataset, s.Frequency),
			Command:     fmt.Sprintf("bash -c \"%s\"", strings.ReplaceAll(mainCmd, "\"", "\\\"")),
			OnCalendar:  onCalendar,
			Persistent:  true,
			After:       []string{"zfs.target"},
		})
		if err != nil {
			log.Printf("ERROR: failed to install snapshot timer for %s: %v", s.Dataset, err)
		}
	}
}

func (h *SnapshotScheduleHandler) RunNow(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var req struct {
		Dataset string `json:"dataset"`
		Prefix  string `json:"prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	datasetPattern := regexp.MustCompile(`^[a-zA-Z0-9_\-/.@]+$`)
	if !datasetPattern.MatchString(req.Dataset) {
		respondErrorSimple(w, "Invalid dataset name", http.StatusBadRequest)
		return
	}

	snapName := fmt.Sprintf("%s-%s", req.Prefix, time.Now().Format("20060102-1504"))
	start := time.Now()
	output, err := cmdutil.RunZFS("zfs", "snapshot", req.Dataset+"@"+snapName)
	duration := time.Since(start)

	if err != nil {
		audit.LogAction("snapshot_run_now", user, fmt.Sprintf("Failed: %s@%s: %s", req.Dataset, snapName, string(output)), false, duration)
		respondErrorSimple(w, "Snapshot failed", http.StatusInternalServerError)
		return
	}

	audit.LogAction("snapshot_run_now", user, fmt.Sprintf("Created %s@%s", req.Dataset, snapName), true, duration)

	// Fire post-snapshot replication for matching schedules
	go TriggerPostSnapshotReplication(req.Dataset)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "snapshot": req.Dataset + "@" + snapName})
}

// RunCronHook is called by the cron job instead of running `zfs snapshot` directly.
// It creates the snapshot, prunes old ones, and fires post-snapshot replication.
// POST /api/zfs/snapshots/cron-hook
func (h *SnapshotScheduleHandler) RunCronHook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset   string `json:"dataset"`
		Prefix    string `json:"prefix"`
		Retention int    `json:"retention"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	datasetPattern := regexp.MustCompile(`^[a-zA-Z0-9_\-/.@]+$`)
	if !datasetPattern.MatchString(req.Dataset) {
		respondErrorSimple(w, "Invalid dataset name", http.StatusBadRequest)
		return
	}
	if req.Prefix == "" {
		req.Prefix = "auto"
	}
	if req.Retention < 1 {
		req.Retention = 7
	}

	snapName := fmt.Sprintf("%s-%s", req.Prefix, time.Now().Format("20060102-1504"))
	fullName := req.Dataset + "@" + snapName

	// Create snapshot
	start := time.Now()
	output, err := cmdutil.RunZFS("zfs", "snapshot", fullName)
	duration := time.Since(start)
	if err != nil {
		audit.LogAction("snapshot_cron", "cron", fmt.Sprintf("Failed: %s: %s", fullName, string(output)), false, duration)
		respondErrorSimple(w, "Snapshot failed", http.StatusInternalServerError)
		return
	}
	audit.LogAction("snapshot_cron", "cron", fmt.Sprintf("Created %s", fullName), true, duration)

	// Prune old snapshots beyond retention count (best-effort, non-fatal)
	pruneOldSnapshots(req.Dataset, req.Prefix, req.Retention)

	// Fire post-snapshot replication asynchronously
	go TriggerPostSnapshotReplication(req.Dataset)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "snapshot": fullName})
}

// pruneOldSnapshots destroys snapshots beyond the retention count for a dataset+prefix.
func pruneOldSnapshots(dataset, prefix string, retention int) {
	output, err := cmdutil.RunZFS("zfs",
		"list", "-t", "snapshot", "-o", "name", "-s", "creation", "-H", dataset)
	if err != nil {
		return
	}
	var matching []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Only consider snapshots with our prefix
		atIdx := strings.Index(line, "@")
		if atIdx < 0 {
			continue
		}
		snapPart := line[atIdx+1:]
		if strings.HasPrefix(snapPart, prefix+"-") {
			matching = append(matching, line)
		}
	}

	// matching is sorted oldest-first (zfs list -s creation)
	if len(matching) > retention {
		toDelete := matching[:len(matching)-retention]
		for _, snap := range toDelete {
			if _, err := cmdutil.RunZFS("zfs", "destroy", snap); err != nil {
				log.Printf("WARN: pruneOldSnapshots: failed to destroy %s: %v", snap, err)
			}
		}
	}
}

// ============================================================
// ACL MANAGEMENT
// ============================================================

type ACLHandler struct{}

func NewACLHandler() *ACLHandler { return &ACLHandler{} }

func (h *ACLHandler) GetACL(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" || !strings.HasPrefix(path, "/mnt/") {
		respondErrorSimple(w, "Path must start with /mnt/", http.StatusBadRequest)
		return
	}

	// Get POSIX ACL
	log.Printf("ACL: GetACL for path: %s", path)
	output, err := cmdutil.RunFast("getfacl", "-p", path)
	if err != nil {
		log.Printf("ACL: GetACL failed for %s: %v, output: %s", path, err, string(output))
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("getfacl failed: %v, output: %s", err, string(output)),
		})
		return
	}

	// Get basic stat info
	statOut, _ := cmdutil.RunFast("stat", "-c", "%U %G %a %F", path)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"path":    path,
		"acl":     string(output),
		"stat":    strings.TrimSpace(string(statOut)),
	})
}

func (h *ACLHandler) SetACL(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var req struct {
		Path      string `json:"path"`
		Entry     string `json:"entry"` // single entry (CI/legacy)
		ACL       string `json:"acl"`   // multi-line full ACL (frontend)
		Recursive bool   `json:"recursive"`
		Remove    bool   `json:"remove"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(req.Path, "/mnt/") {
		respondErrorSimple(w, "Path must start with /mnt/", http.StatusBadRequest)
		return
	}

	// Validate path exists
	if _, err := os.Stat(req.Path); os.IsNotExist(err) {
		respondErrorSimple(w, "Path does not exist", http.StatusBadRequest)
		return
	}

	args := []string{}
	if req.Recursive {
		args = append(args, "-R")
	}

	// Case 1: Full ACL list (frontend)
	if req.ACL != "" {
		// Filter out comments and empty lines
		lines := strings.Split(req.ACL, "\n")
		var validEntries []string
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			validEntries = append(validEntries, line)
		}
		// Apply all at once using --set (replaces entire ACL)
		combined := strings.Join(validEntries, ",")
		args = append(args, "--set", combined, req.Path)
	} else if req.Entry != "" {
		// Case 2: Single entry (CI/legacy)
		aclPattern := regexp.MustCompile(`^(u|g|o|m)(:[a-zA-Z0-9_.\-]*)?:[rwx\-]{0,3}$`)
		if !aclPattern.MatchString(req.Entry) {
			respondErrorSimple(w, "Invalid ACL entry format. Use: u:user:rwx, g:group:rx, etc.", http.StatusBadRequest)
			return
		}

		// Fix #4 (LDAP UID Showstopper): Validate user/group exists BEFORE applying ACL.
		// If LDAP is temporarily down, getent won't resolve the name → we reject early
		// instead of silently applying ACL to a numeric UID that becomes orphaned.
		entryParts := strings.SplitN(req.Entry, ":", 3)
		if len(entryParts) >= 2 && entryParts[1] != "" {
			entryType := entryParts[0]
			entryName := entryParts[1]

			switch entryType {
			case "u":
				// Validate user exists via NSS (covers local + LDAP + SSSD)
				if out, err := cmdutil.RunFast("getent", "passwd", entryName); err != nil {
					audit.LogAction("acl_validate", user, fmt.Sprintf("User '%s' not found in NSS (LDAP down?): %s", entryName, string(out)), false, 0)
					respondErrorSimple(w, fmt.Sprintf("User '%s' not found. If using LDAP, check directory service connectivity.", entryName), http.StatusBadRequest)
					return
				}
			case "g":
				// Validate group exists via NSS
				if out, err := cmdutil.RunFast("getent", "group", entryName); err != nil {
					audit.LogAction("acl_validate", user, fmt.Sprintf("Group '%s' not found in NSS (LDAP down?): %s", entryName, string(out)), false, 0)
					respondErrorSimple(w, fmt.Sprintf("Group '%s' not found. If using LDAP, check directory service connectivity.", entryName), http.StatusBadRequest)
					return
				}
			}
		}

		if req.Remove {
			args = append(args, "-x", req.Entry, req.Path)
		} else {
			args = append(args, "-m", req.Entry, req.Path)
		}
	} else {
		respondErrorSimple(w, "Neither 'acl' nor 'entry' provided", http.StatusBadRequest)
		return
	}

	log.Printf("ACL: SetACL for path: %s (args=%v)", req.Path, args)
	start := time.Now()
	output, err := cmdutil.RunFast("setfacl", args...)
	duration := time.Since(start)

	action := "set"
	if req.Remove {
		action = "remove"
	}

	if err != nil {
		log.Printf("ACL: SetACL failed for %s: %v, output: %s", req.Path, err, string(output))
		audit.LogAction("acl_"+action, user, fmt.Sprintf("Failed on %s: %s", req.Path, string(output)), false, duration)
		respondErrorSimple(w, fmt.Sprintf("setfacl failed: %v, output: %s", err, string(output)), http.StatusInternalServerError)
		return
	}
	log.Printf("ACL: SetACL success for %s", req.Path)

	audit.LogAction("acl_"+action, user, fmt.Sprintf("ACL %s on %s: %s", action, req.Path, req.Entry), true, duration)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// ============================================================
// METRICS / REPORTING
// ============================================================

type MetricsHandler struct{}

func NewMetricsHandler() *MetricsHandler { return &MetricsHandler{} }

const metricsDir = config.MetricsDir

func (h *MetricsHandler) GetCurrentMetrics(w http.ResponseWriter, r *http.Request) {
	metrics := map[string]interface{}{}

	// CPU usage
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		metrics["cpu_raw"] = strings.Split(string(data), "\n")[0]
	}

	// Memory
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		lines := strings.Split(string(data), "\n")
		mem := map[string]string{}
		for _, line := range lines[:6] {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				mem[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}
		metrics["memory"] = mem
	}

	// Disk I/O from /proc/diskstats
	if data, err := os.ReadFile("/proc/diskstats"); err == nil {
		metrics["diskstats"] = string(data)
	}

	// Network from /proc/net/dev
	if data, err := os.ReadFile("/proc/net/dev"); err == nil {
		metrics["netdev"] = string(data)
	}

	// Load average
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		metrics["loadavg"] = strings.TrimSpace(string(data))
	}

	// ZFS ARC stats
	if data, err := os.ReadFile("/proc/spl/kstat/zfs/arcstats"); err == nil {
		metrics["arcstats"] = string(data)
	}

	// Pool usage (zpool list)
	if cmd, err := cmdutil.RunZFS("zpool", "list", "-Hp"); err == nil {
		metrics["zpools"] = string(cmd)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "metrics": metrics, "timestamp": time.Now().Unix()})
}

func (h *MetricsHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period") // hour, day, week
	if period == "" {
		period = "day"
	}

	filename := filepath.Join(metricsDir, period+".json")
	data, err := os.ReadFile(filename)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "history": []interface{}{}, "period": period})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	var history []interface{}
	json.Unmarshal(data, &history)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"period":  period,
		"history": history,
	})
}

// CollectMetrics is called by the background monitor every 60 seconds
func (h *MetricsHandler) CollectAndStore() {
	os.MkdirAll(metricsDir, 0755)

	point := map[string]interface{}{"ts": time.Now().Unix()}

	// CPU
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) >= 3 {
			point["load1"] = parts[0]
			point["load5"] = parts[1]
			point["load15"] = parts[2]
		}
	}

	// Memory
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				point["mem_total"] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(line), " kB"), "MemTotal:"))
			} else if strings.HasPrefix(line, "MemAvailable:") {
				point["mem_avail"] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(line), " kB"), "MemAvailable:"))
			}
		}
	}

	// Pool usage
	if out, err := cmdutil.RunZFS("zpool", "list", "-Hp", "-o", "name,size,alloc,free,health"); err == nil {
		point["zpools"] = strings.TrimSpace(string(out))
	}

	// Append to day file (ring buffer of 1440 = 24h at 1-minute intervals)
	h.appendToHistory("day", point, 1440)
	// Every 5 minutes, also store to week file (2016 = 7 days at 5-min intervals)
	if time.Now().Minute()%5 == 0 {
		h.appendToHistory("week", point, 2016)
	}
}

func (h *MetricsHandler) appendToHistory(period string, point map[string]interface{}, maxPoints int) {
	filename := filepath.Join(metricsDir, period+".json")
	var history []map[string]interface{}

	if data, err := os.ReadFile(filename); err == nil {
		json.Unmarshal(data, &history)
	}

	history = append(history, point)
	if len(history) > maxPoints {
		history = history[len(history)-maxPoints:]
	}

	data, err := json.Marshal(history)
	if err != nil {
		log.Printf("WARN: failed to marshal metrics: %v", err)
		return
	}
	if err := os.WriteFile(filename, data, 0644); err != nil {
		log.Printf("WARN: failed to write metrics file %s: %v", filename, err)
	}
}

// ============================================================
// FIREWALL
// ============================================================

type FirewallHandler struct{}

func NewFirewallHandler() *FirewallHandler { return &FirewallHandler{} }

func (h *FirewallHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if NixWriter != nil && NixWriter.IsNixOS() {
		h.GetStatusNixOS(w, r)
		return
	}
	output, err := cmdutil.RunFast("ufw", "status", "numbered")
	status := "inactive"
	rawOutput := ""
	if err == nil {
		rawOutput = string(output)
		if strings.Contains(rawOutput, "Status: active") {
			status = "active"
		}
	}

	// Parse `ufw status numbered` lines into structured rules.
	// Each line looks like:  [ 1] 80/tcp                     ALLOW IN    Anywhere
	rules := parseUFWRules(rawOutput)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"status":    status,
		"rules":     rules,     // always a []map - never a raw string
		"rules_raw": rawOutput, // raw text for debugging
	})
}

// GetStatusNixOS provides a ufw-compatible status response from NixWriter state.
func (h *FirewallHandler) GetStatusNixOS(w http.ResponseWriter, r *http.Request) {
	state := NixWriter.State()
	tcp := state.FirewallTCP
	udp := state.FirewallUDP

	var raw strings.Builder
	raw.WriteString("Status: active\n\n     To                         Action      From\n     --                         ------      ----\n")

	rules := []map[string]interface{}{}
	id := 1
	for _, p := range tcp {
		portStr := fmt.Sprintf("%d/tcp", p)
		raw.WriteString(fmt.Sprintf("[%2d] %-25s ALLOW IN    Anywhere\n", id, portStr))
		rules = append(rules, map[string]interface{}{
			"id": id, "action": "allow", "port": fmt.Sprintf("%d", p), "proto": "tcp", "from": "Anywhere",
		})
		id++
	}
	for _, p := range udp {
		portStr := fmt.Sprintf("%d/udp", p)
		raw.WriteString(fmt.Sprintf("[%2d] %-25s ALLOW IN    Anywhere\n", id, portStr))
		rules = append(rules, map[string]interface{}{
			"id": id, "action": "allow", "port": fmt.Sprintf("%d", p), "proto": "udp", "from": "Anywhere",
		})
		id++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"status":    "active",
		"rules":     rules,
		"rules_raw": raw.String(),
	})
}

// parseUFWRules converts `ufw status numbered` output into a slice of rule maps.
func parseUFWRules(output string) []map[string]interface{} {
	rules := []map[string]interface{}{}
	ruleRe := regexp.MustCompile(`^\[\s*(\d+)\]\s+(\S+)\s+(ALLOW|DENY|REJECT|LIMIT)\s+(IN|OUT|FWD)?\s*(.*)`)
	for _, line := range strings.Split(output, "\n") {
		m := ruleRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		num, _ := strconv.Atoi(m[1])
		portProto := m[2]
		action := strings.ToLower(m[3])
		from := strings.TrimSpace(m[5])

		proto := "tcp"
		port := portProto
		if idx := strings.Index(portProto, "/"); idx >= 0 {
			port = portProto[:idx]
			proto = portProto[idx+1:]
		}

		rules = append(rules, map[string]interface{}{
			"id":     num,
			"action": action,
			"port":   port,
			"proto":  proto,
			"from":   from,
		})
	}
	return rules
}

func (h *FirewallHandler) SetRule(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var req struct {
		Action  string `json:"action"`   // allow, deny, delete, enable, disable, reset
		Port    string `json:"port"`     // e.g. "80/tcp", "443", "22/tcp"
		From    string `json:"from"`     // source IP/CIDR (optional)
		RuleNum int    `json:"rule_num"` // for delete
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	var args []string
	switch req.Action {
	case "enable":
		args = []string{"--force", "enable"}
	case "disable":
		args = []string{"disable"}
	case "allow", "deny":
		if req.Port == "" {
			respondErrorSimple(w, "Port is required", http.StatusBadRequest)
			return
		}
		portPattern := regexp.MustCompile(`^[0-9]+(/tcp|/udp)?$`)
		if !portPattern.MatchString(req.Port) {
			respondErrorSimple(w, "Invalid port format", http.StatusBadRequest)
			return
		}
		if req.From != "" {
			ipPattern := regexp.MustCompile(`^[0-9a-fA-F./:]+$`)
			if !ipPattern.MatchString(req.From) {
				respondErrorSimple(w, "Invalid source IP", http.StatusBadRequest)
				return
			}
			args = []string{req.Action, "from", req.From, "to", "any", "port", strings.Split(req.Port, "/")[0]}
			if strings.Contains(req.Port, "/") {
				args = append(args, "proto", strings.Split(req.Port, "/")[1])
			}
		} else {
			args = []string{req.Action, req.Port}
		}
	case "delete":
		if req.RuleNum < 1 {
			respondErrorSimple(w, "Rule number required", http.StatusBadRequest)
			return
		}
		args = []string{"--force", "delete", fmt.Sprintf("%d", req.RuleNum)}
	default:
		respondErrorSimple(w, "Invalid action", http.StatusBadRequest)
		return
	}

	start := time.Now()
	var (
		output []byte
		err    error
	)
	if NixWriter != nil && NixWriter.IsNixOS() {
		// On NixOS we skip the real ufw command; persistFirewallFromRequest below
		// will handle the state update.
		output = []byte("Rule updated (NixOS native)")
		err = nil
	} else {
		output, err = cmdutil.RunMedium("ufw", args...)
	}
	duration := time.Since(start)

	if err != nil {
		audit.LogAction("firewall", user, fmt.Sprintf("Failed: ufw %s: %s", strings.Join(args, " "), string(output)), false, duration)
		respondErrorSimple(w, "Operation failed", http.StatusInternalServerError)
		return
	}

	audit.LogAction("firewall", user, fmt.Sprintf("ufw %s", strings.Join(args, " ")), true, duration)

	// On NixOS: persist firewall state to dplane-generated.nix
	// ufw is not used on NixOS; we translate allow/deny rules to NixOS port lists
	if NixWriter != nil && NixWriter.IsNixOS() {
		persistFirewallFromRequest(req.Action, req.Port)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "output": string(output)})
}

// ============================================================
// SSL/TLS CERTIFICATES
// ============================================================

type CertHandler struct{}

func NewCertHandler() *CertHandler { return &CertHandler{} }

// certDir is computed dynamically via configPath("ssl")

func (h *CertHandler) ListCerts(w http.ResponseWriter, r *http.Request) {
	os.MkdirAll(configPath("ssl"), 0700)

	entries, err := os.ReadDir(configPath("ssl"))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "certs": []interface{}{}})
		return
	}

	var certs []map[string]string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".crt") {
			name := strings.TrimSuffix(e.Name(), ".crt")
			certFile := filepath.Join(configPath("ssl"), e.Name())
			// Get cert info
			out, _ := cmdutil.RunFast("openssl", "x509", "-in", certFile, "-noout", "-subject", "-enddate", "-issuer")
			info := map[string]string{"name": name, "file": e.Name(), "details": strings.TrimSpace(string(out))}
			// Check if key exists
			keyFile := filepath.Join(configPath("ssl"), name+".key")
			if _, err := os.Stat(keyFile); err == nil {
				info["has_key"] = "true"
			}
			certs = append(certs, info)
		}
	}

	// Also check nginx current cert
	nginxCert := ""
	if data, err := os.ReadFile("/etc/nginx/sites-enabled/dplaneos"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "ssl_certificate ") && !strings.Contains(line, "ssl_certificate_key") {
				nginxCert = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "ssl_certificate "), ";"))
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "certs": certs, "active_cert": nginxCert})
}

// DeleteCert removes an SSL certificate and its key
// POST /api/system/certs/delete { "name": "mycert" }
func (h *CertHandler) DeleteCert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := r.Header.Get("X-User")
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	namePattern := regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)
	if !namePattern.MatchString(req.Name) {
		respondErrorSimple(w, "Invalid name", http.StatusBadRequest)
		return
	}

	// 1. Guard: check if active in nginx
	if data, err := os.ReadFile("/etc/nginx/sites-enabled/dplaneos"); err == nil {
		content := string(data)
		if strings.Contains(content, req.Name+".crt") || strings.Contains(content, req.Name+".key") {
			respondErrorSimple(w, "Cannot delete certificate currently in use by Nginx", http.StatusForbidden)
			return
		}
	}

	// 2. Delete files
	certFile := filepath.Join(configPath("ssl"), req.Name+".crt")
	keyFile := filepath.Join(configPath("ssl"), req.Name+".key")
	
	_ = os.Remove(certFile)
	_ = os.Remove(keyFile)

	audit.LogActivity(user, "cert_delete", map[string]interface{}{"name": req.Name})
	respondOK(w, map[string]interface{}{"success": true, "message": "Certificate deleted"})
}

func (h *CertHandler) GenerateSelfSigned(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var req struct {
		Name string `json:"name"`
		CN   string `json:"cn"` // Common Name
		Days int    `json:"days"`
		SANs string `json:"sans"` // comma-separated
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	namePattern := regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)
	if !namePattern.MatchString(req.Name) {
		respondErrorSimple(w, "Invalid certificate name", http.StatusBadRequest)
		return
	}
	if req.Days < 1 || req.Days > 3650 {
		req.Days = 365
	}
	if req.CN == "" {
		req.CN = "dplaneos.local"
	}

	os.MkdirAll(configPath("ssl"), 0700)
	keyFile := filepath.Join(configPath("ssl"), req.Name+".key")
	certFile := filepath.Join(configPath("ssl"), req.Name+".crt")

	// Build SAN extension
	sanExt := fmt.Sprintf("subjectAltName=DNS:%s", req.CN)
	if req.SANs != "" {
		for _, san := range strings.Split(req.SANs, ",") {
			san = strings.TrimSpace(san)
			if san != "" {
				if strings.Contains(san, ".") && !strings.Contains(san, ":") {
					sanExt += ",DNS:" + san
				} else {
					sanExt += ",IP:" + san
				}
			}
		}
	}

	start := time.Now()
	output, err := cmdutil.RunMedium("openssl", "req", "-x509", "-newkey", "rsa:2048",
		"-keyout", keyFile, "-out", certFile,
		"-days", fmt.Sprintf("%d", req.Days),
		"-nodes", "-subj", fmt.Sprintf("/CN=%s", req.CN),
		"-addext", sanExt)
	duration := time.Since(start)

	if err != nil {
		audit.LogAction("cert_generate", user, fmt.Sprintf("Failed: %s", string(output)), false, duration)
		respondErrorSimple(w, "Operation failed", http.StatusInternalServerError)
		return
	}

	os.Chmod(keyFile, 0600)
	audit.LogAction("cert_generate", user, fmt.Sprintf("Generated self-signed cert: %s (%d days)", req.Name, req.Days), true, duration)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "cert": certFile, "key": keyFile})
}

// ActivateCert updates Nginx to use the specified certificate
// POST /api/system/certs/activate { "name": "mycert" }
func (h *CertHandler) ActivateCert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := r.Header.Get("X-User")

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	certFile := filepath.Join(configPath("ssl"), req.Name+".crt")
	keyFile := filepath.Join(configPath("ssl"), req.Name+".key")

	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		respondErrorSimple(w, "Certificate not found", http.StatusNotFound)
		return
	}
	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		respondErrorSimple(w, "Key file not found", http.StatusNotFound)
		return
	}

	// Update nginx config
	nginxConf := "/etc/nginx/sites-enabled/dplaneos"
	data, err := os.ReadFile(nginxConf)
	if err != nil {
		respondErrorSimple(w, "Cannot read nginx config", http.StatusInternalServerError)
		return
	}

	content := string(data)
	// Replace ssl_certificate and ssl_certificate_key lines
	certLine := regexp.MustCompile(`ssl_certificate\s+[^;]+;`)
	keyLine := regexp.MustCompile(`ssl_certificate_key\s+[^;]+;`)
	content = certLine.ReplaceAllString(content, "ssl_certificate "+certFile+";")
	content = keyLine.ReplaceAllString(content, "ssl_certificate_key "+keyFile+";")

	if err := os.WriteFile(nginxConf, []byte(content), 0644); err != nil {
		respondErrorSimple(w, "Cannot write nginx config", http.StatusInternalServerError)
		return
	}

	// Test nginx config
	testOut, testErr := cmdutil.RunFast("nginx", "-t")
	if testErr != nil {
		audit.LogAction("cert_activate", user, fmt.Sprintf("nginx test failed: %s", string(testOut)), false, 0)
		respondErrorSimple(w, "nginx config test failed", http.StatusInternalServerError)
		return
	}

	// Reload nginx
	cmdutil.RunFast("nginx", "-s", "reload")

	audit.LogAction("cert_activate", user, fmt.Sprintf("Activated cert: %s", req.Name), true, 0)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// ImportCert allows uploading an existing certificate and private key.
// POST /api/system/certs/import
func (h *CertHandler) ImportCert(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	var req struct {
		Name string `json:"name"`
		Cert string `json:"cert"` // PEM format
		Key  string `json:"key"`  // PEM format
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	namePattern := regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)
	if !namePattern.MatchString(req.Name) {
		respondErrorSimple(w, "Invalid name", http.StatusBadRequest)
		return
	}

	// Basic validation: ensure they look like PEM
	if !strings.Contains(req.Cert, "BEGIN CERTIFICATE") || !strings.Contains(req.Key, "BEGIN") {
		respondErrorSimple(w, "Invalid certificate or key format (PEM expected)", http.StatusBadRequest)
		return
	}

	os.MkdirAll(configPath("ssl"), 0700)
	certFile := filepath.Join(configPath("ssl"), req.Name+".crt")
	keyFile := filepath.Join(configPath("ssl"), req.Name+".key")

	if err := os.WriteFile(certFile, []byte(req.Cert), 0644); err != nil {
		respondErrorSimple(w, "Failed to save certificate", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(keyFile, []byte(req.Key), 0600); err != nil {
		respondErrorSimple(w, "Failed to save key", http.StatusInternalServerError)
		return
	}

	audit.LogAction("cert_import", user, "Imported certificate: "+req.Name, true, 0)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// LegoUser satisfies the lego.User interface
type LegoUser struct {
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *LegoUser) GetEmail() string                        { return u.Email }
func (u *LegoUser) GetRegistration() *registration.Resource { return u.Registration }
func (u *LegoUser) GetPrivateKey() crypto.PrivateKey         { return u.key }

// getACMEAccountKey loads the account key from disk or generates a new one.
func getACMEAccountKey() (crypto.PrivateKey, error) {
	keyPath := ConfigDir + "/acme_account.key"
	if data, err := os.ReadFile(keyPath); err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("failed to decode PEM block in acme_account.key")
		}
		return x509.ParseECPrivateKey(block.Bytes)
	}

	// Generate new key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	pemData := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	if err := os.WriteFile(keyPath, pemData, 0600); err != nil {
		return nil, err
	}
	return privateKey, nil
}

// RequestACME requests a certificate from Let's Encrypt using HTTP-01.
// POST /api/certs/acme
func (h *CertHandler) RequestACME(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	var req struct {
		Name    string `json:"name"`
		Domain  string `json:"domain"`
		Email   string `json:"email"`
		Staging bool   `json:"staging"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate inputs
	if req.Domain == "" || req.Email == "" || req.Name == "" {
		respondErrorSimple(w, "Name, Domain, and Email are required", http.StatusBadRequest)
		return
	}

	jobId := jobs.Start("acme_request", func(j *jobs.Job) {
		h.obtainCertificate(req.Domain, req.Name, req.Email, req.Staging, user, j)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "jobId": jobId})
}

// obtainCertificate is the shared logic for initial issuance and renewal.
func (h *CertHandler) obtainCertificate(domain, name, email string, staging bool, user string, j *jobs.Job) {
	j.Log("Starting ACME request for " + domain)

	// Ensure Nginx proxy is present (non-NixOS only)
	if !IsNixOS() {
		j.Progress(map[string]string{"status": "configuring_nginx", "message": "Ensuring Nginx challenge proxy is configured..."})
		if err := h.ensureACMEProxy(); err != nil {
			j.Log("WARN: Nginx auto-proxy configuration failed (continuing anyway): " + err.Error())
		}
	}

	j.Progress(map[string]string{"status": "loading_account", "message": "Loading ACME account key..."})
	privateKey, err := getACMEAccountKey()
	if err != nil {
		j.Fail("Failed to load/generate ACME account key: " + err.Error())
		return
	}

	myUser := LegoUser{
		Email: email,
		key:   privateKey,
	}

	config := lego.NewConfig(&myUser)
	if staging {
		config.CADirURL = "https://acme-staging-v02.api.letsencrypt.org/directory"
	} else {
		config.CADirURL = lego.LEDirectoryProduction
	}
	config.Certificate.KeyType = certcrypto.RSA2048

	client, err := lego.NewClient(config)
	if err != nil {
		j.Fail("Failed to create ACME client: " + err.Error())
		return
	}

	j.Progress(map[string]string{"status": "setting_provider", "message": "Setting up HTTP-01 challenge server on port 8080..."})
	err = client.Challenge.SetHTTP01Provider(http01.NewProviderServer("", "8080"))
	if err != nil {
		j.Fail("Failed to set challenge provider: " + err.Error())
		return
	}

	j.Progress(map[string]string{"status": "registering", "message": "Registering account with Let's Encrypt..."})
	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		j.Log("Note: Registration might already exist: " + err.Error())
	}
	myUser.Registration = reg

	j.Progress(map[string]string{"status": "obtaining", "message": "Obtaining certificate (this may take up to 30s)..."})
	request := certificate.ObtainRequest{
		Domains: []string{domain},
		Bundle:  true,
	}
	certificates, err := client.Certificate.Obtain(request)
	if err != nil {
		j.Fail("Failed to obtain certificate: " + err.Error())
		return
	}

	j.Progress(map[string]string{"status": "saving", "message": "Saving certificates to /etc/dplaneos/ssl..."})
	os.MkdirAll(configPath("ssl"), 0700)
	certFile := ConfigDir + "/ssl/" + name + ".pem"
	keyFile := ConfigDir + "/ssl/" + name + ".key"

	if err := os.WriteFile(certFile, certificates.Certificate, 0644); err != nil {
		j.Fail("Failed to save certificate: " + err.Error())
		return
	}
	if err := os.WriteFile(keyFile, certificates.PrivateKey, 0600); err != nil {
		j.Fail("Failed to save key: " + err.Error())
		return
	}

	// Save metadata for renewal
	meta := struct {
		Email   string `json:"email"`
		Staging bool   `json:"staging"`
	}{Email: email, Staging: staging}
	if metaBytes, err := json.Marshal(meta); err == nil {
		os.WriteFile(certFile+".meta", metaBytes, 0644)
	}

	audit.LogAction("cert_acme", user, fmt.Sprintf("Obtained ACME cert for %s (name: %s)", domain, name), true, 0)
	j.Done(map[string]interface{}{"name": name, "domain": domain})
}

// ensureACMEProxy injects the /.well-known/acme-challenge/ block into Nginx on non-NixOS.
func (h *CertHandler) ensureACMEProxy() error {
	vhostPath := "/etc/nginx/sites-enabled/dplaneos"
	if _, err := os.Stat(vhostPath); os.IsNotExist(err) {
		return fmt.Errorf("nginx vhost dplaneos not found in sites-enabled")
	}

	content, err := os.ReadFile(vhostPath)
	if err != nil {
		return err
	}

	proxyBlock := `    location /.well-known/acme-challenge/ {
        proxy_pass http://127.0.0.1:8080;
    }`

	if strings.Contains(string(content), "/.well-known/acme-challenge/") {
		return nil // Already present
	}

	// Inject before the last closing brace (assuming it's the server block)
	newContent := strings.Replace(string(content), "server {", "server {\n"+proxyBlock, 1)

	tmpPath := vhostPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(newContent), 0644); err != nil {
		return err
	}

	// Test config
	if _, err := cmdutil.RunNoTimeout("nginx", "-t"); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("nginx config test failed: %v", err)
	}

	if err := os.Rename(tmpPath, vhostPath); err != nil {
		return err
	}

	cmdutil.RunFast("nginx", "-s", "reload")
	return nil
}

// RenewAllHandler checks all certificates and renews those expiring in < 30 days.
// POST /api/certs/acme/renew-all
func (h *CertHandler) RenewAllHandler(w http.ResponseWriter, r *http.Request) {
	jobId := jobs.Start("acme_renew_all", func(j *jobs.Job) {
		j.Log("Starting ACME auto-renewal check")
		sslDir := ConfigDir + "/ssl"
		entries, err := os.ReadDir(sslDir)
		if err != nil {
			j.Fail("Failed to read SSL directory: " + err.Error())
			return
		}

		renewCount := 0
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".pem") {
				continue
			}
			certName := strings.TrimSuffix(e.Name(), ".pem")
			certPath := filepath.Join(sslDir, e.Name())
			data, err := os.ReadFile(certPath)
			if err != nil {
				j.Log("WARN: failed to read cert " + e.Name() + ": " + err.Error())
				continue
			}

			block, _ := pem.Decode(data)
			if block == nil {
				j.Log("WARN: failed to decode PEM for " + e.Name())
				continue
			}

			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				j.Log("WARN: failed to parse certificate " + e.Name() + ": " + err.Error())
				continue
			}

			daysRemaining := int(time.Until(cert.NotAfter).Hours() / 24)
			j.Log(fmt.Sprintf("Cert %s expires in %d days", certName, daysRemaining))

			if daysRemaining < 30 {
				j.Log("Renewing " + certName + "...")
				domain := cert.Subject.CommonName
				if domain == "" && len(cert.DNSNames) > 0 {
					domain = cert.DNSNames[0]
				}

				if domain == "" {
					j.Log("ERR: could not determine domain for " + certName)
					continue
				}

				// We assume email is known or we use a fallback if not stored.
				// In a full implementation, we might want to store the email in a YAML next to the cert.
				// For now, we'll try to find a default or require it. 
				// Actually, the lego client needs a user. 
				// Let's assume for now we use the email from the most recent request or a global setting.
				// PRO TIP: In v6.2.0 we'll read it from a .meta file next to the cert if it exists.
				email := ""
				metaPath := certPath + ".meta"
				if metaData, err := os.ReadFile(metaPath); err == nil {
					var meta struct {
						Email   string `json:"email"`
						Staging bool   `json:"staging"`
					}
					if err := json.Unmarshal(metaData, &meta); err == nil {
						email = meta.Email
						staging := meta.Staging
						renewCount++
						// We run it synchronously inside this job
						h.obtainCertificate(domain, certName, email, staging, "system-renew", j)
					}
				} else {
					j.Log("WARN: missing metadata for " + certName + " (skipping renewal)")
				}
			}
		}
		j.Log(fmt.Sprintf("Renewal check complete. %d renewals attempt.", renewCount))
		j.Done(map[string]interface{}{"renewed": renewCount})
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "jobId": jobId})
}

// VerifyACMEProxy checks if /.well-known/acme-challenge/ is correctly proxied to port 8080.
// GET /api/system/certs/acme/check?domain=...
func (h *CertHandler) VerifyACMEProxy(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		respondErrorSimple(w, "Domain is required", http.StatusBadRequest)
		return
	}

	// Start a temporary listener on 8080 just for this check
	ln, err := net.Listen("tcp", ":8080")
	if err != nil {
		respondErrorSimple(w, "Port 8080 is already in use", http.StatusConflict)
		return
	}
	
	magicToken := uuid.New().String()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/dplaneos-check") {
				fmt.Fprint(w, magicToken)
			}
		}),
	}
	go srv.Serve(ln)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		ln.Close()
	}()

	// Try to reach it via public DNS/HTTP
	checkURL := fmt.Sprintf("http://%s/.well-known/acme-challenge/dplaneos-check", domain)
	client := &http.Client{Timeout: 5 * time.Second}
	
	resp, err := client.Get(checkURL)
	if err != nil {
		respondErrorSimple(w, "Proxy check failed: "+err.Error(), http.StatusFailedDependency)
		return
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		respondErrorSimple(w, "Failed to read response body", http.StatusInternalServerError)
		return
	}

	if string(bodyBytes) != magicToken {
		respondErrorSimple(w, "Proxy verification failed: magic token mismatch. Ensure Nginx proxies /.well-known/acme-challenge/ to http://127.0.0.1:8080/", http.StatusFailedDependency)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Proxy verified successfully"})
}

// ============================================================
// TRASH / RECYCLE BIN
// ============================================================

type TrashHandler struct{}

func NewTrashHandler() *TrashHandler { return &TrashHandler{} }

const trashBase = "/mnt/.dplaneos-trash"

func (h *TrashHandler) MoveToTrash(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(req.Path, "/mnt/") {
		respondErrorSimple(w, "Can only trash files under /mnt/", http.StatusBadRequest)
		return
	}

	if _, err := os.Stat(req.Path); os.IsNotExist(err) {
		respondErrorSimple(w, "File not found", http.StatusNotFound)
		return
	}

	// Create trash directory structure: /mnt/.dplaneos-trash/YYYYMMDD-HHMMSS_filename
	os.MkdirAll(trashBase, 0755)
	baseName := filepath.Base(req.Path)
	trashName := fmt.Sprintf("%s_%s", time.Now().Format("20060102-150405"), baseName)
	trashPath := filepath.Join(trashBase, trashName)

	// Store original path for restore
	metaPath := trashPath + ".meta"
	if err := os.WriteFile(metaPath, []byte(req.Path), 0644); err != nil {
		log.Printf("WARN: failed to write trash metadata: %v", err)
	}

	start := time.Now()
	err := os.Rename(req.Path, trashPath)
	duration := time.Since(start)

	if err != nil {
		// Cross-device? Try mv command
		if _, mvErr := cmdutil.RunNoTimeout("mv", req.Path, trashPath); mvErr != nil {
			audit.LogAction("trash", user, fmt.Sprintf("Failed to trash %s: %v", req.Path, mvErr), false, duration)
			respondErrorSimple(w, "Failed to move to trash", http.StatusInternalServerError)
			return
		}
	}

	audit.LogAction("trash", user, fmt.Sprintf("Moved to trash: %s", req.Path), true, duration)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "trash_path": trashPath})
}

func (h *TrashHandler) ListTrash(w http.ResponseWriter, r *http.Request) {
	os.MkdirAll(trashBase, 0755)

	entries, err := os.ReadDir(trashBase)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "items": []interface{}{}})
		return
	}

	var items []map[string]interface{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta") {
			continue
		}
		info, _ := e.Info()
		item := map[string]interface{}{
			"name":       e.Name(),
			"size":       info.Size(),
			"trashed_at": info.ModTime().Format(time.RFC3339),
			"is_dir":     e.IsDir(),
		}
		// Read original path
		if meta, err := os.ReadFile(filepath.Join(trashBase, e.Name()+".meta")); err == nil {
			item["original_path"] = string(meta)
		}
		items = append(items, item)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "items": items})
}

func (h *TrashHandler) RestoreFromTrash(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	trashPath := filepath.Join(trashBase, req.Name)
	metaPath := trashPath + ".meta"

	if _, err := os.Stat(trashPath); os.IsNotExist(err) {
		respondErrorSimple(w, "Item not found in trash", http.StatusNotFound)
		return
	}

	// Read original path
	originalPath := ""
	if meta, err := os.ReadFile(metaPath); err == nil {
		originalPath = string(meta)
	}

	if originalPath == "" {
		respondErrorSimple(w, "Cannot determine original path", http.StatusInternalServerError)
		return
	}

	// Ensure parent directory exists
	os.MkdirAll(filepath.Dir(originalPath), 0755)

	// Check if target already exists
	if _, err := os.Stat(originalPath); err == nil {
		respondErrorSimple(w, "Target path already exists: "+originalPath, http.StatusConflict)
		return
	}

	err := os.Rename(trashPath, originalPath)
	if err != nil {
		if _, mvErr := cmdutil.RunNoTimeout("mv", trashPath, originalPath); mvErr != nil {
			respondErrorSimple(w, "Failed to restore", http.StatusInternalServerError)
			return
		}
	}

	os.Remove(metaPath)
	audit.LogAction("trash_restore", user, fmt.Sprintf("Restored %s to %s", req.Name, originalPath), true, 0)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "restored_to": originalPath})
}

func (h *TrashHandler) EmptyTrash(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	start := time.Now()
	err := os.RemoveAll(trashBase)
	duration := time.Since(start)

	if err != nil {
		audit.LogAction("trash_empty", user, fmt.Sprintf("Failed: %v", err), false, duration)
		respondErrorSimple(w, "Failed to empty trash", http.StatusInternalServerError)
		return
	}

	os.MkdirAll(trashBase, 0755)
	audit.LogAction("trash_empty", user, "Trash emptied", true, duration)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// ============================================================
// POWER MANAGEMENT
// ============================================================

type PowerMgmtHandler struct{}

func NewPowerMgmtHandler() *PowerMgmtHandler { return &PowerMgmtHandler{} }

func (h *PowerMgmtHandler) GetDiskStatus(w http.ResponseWriter, r *http.Request) {
	// List all block devices
	output, err := cmdutil.RunFast("lsblk", "-dpno", "NAME,SIZE,MODEL,ROTA,TRAN,STATE")
	if err != nil {
		respondErrorSimple(w, "Operation failed", http.StatusInternalServerError)
		return
	}

	var disks []map[string]string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		disk := map[string]string{
			"device": fields[0],
			"size":   fields[1],
		}

		// lsblk -dpno NAME,SIZE,MODEL,ROTA,TRAN,STATE
		// fields[0]: NAME
		// fields[1]: SIZE
		// middle: MODEL (can be multiple fields)
		// fields[N-3]: ROTA
		// fields[N-2]: TRAN
		// fields[N-1]: STATE

		if len(fields) >= 6 {
			disk["model"] = strings.Join(fields[2:len(fields)-3], " ")
			disk["rotational"] = fields[len(fields)-3]
			disk["transport"] = fields[len(fields)-2]
			disk["state"] = fields[len(fields)-1]
		} else {
			disk["model"] = "Unknown"
		}

		// Get hdparm standby status
		hdOut, hdErr := cmdutil.RunFast("hdparm", "-C", fields[0])
		if hdErr == nil {
			if strings.Contains(string(hdOut), "standby") {
				disk["power_state"] = "standby"
			} else if strings.Contains(string(hdOut), "active") {
				disk["power_state"] = "active"
			}
		}

		// Get current spindown setting
		sdOut, _ := cmdutil.RunFast("hdparm", "-B", fields[0])
		for _, l := range strings.Split(string(sdOut), "\n") {
			if strings.Contains(l, "APM_level") {
				parts := strings.Split(l, "=")
				if len(parts) == 2 {
					disk["apm_level"] = strings.TrimSpace(parts[1])
				}
			}
		}

		disks = append(disks, disk)
	}

	// Read saved spindown config
	spindownConf := map[string]int{}
	if data, err := os.ReadFile(configPath("power-management.json")); err == nil {
		json.Unmarshal(data, &spindownConf)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"disks":    disks,
		"spindown": spindownConf,
	})
}

func (h *PowerMgmtHandler) SetSpindown(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var req struct {
		Device  string `json:"device"`
		Timeout int    `json:"timeout"` // 0=disabled, 1-240 (values*5 seconds), 241-251 (30min increments)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	devicePattern := regexp.MustCompile(`^/dev/(sd[a-z]+|nvme[0-9]+n[0-9]+(p[0-9]+)?)$`)
	if !devicePattern.MatchString(req.Device) {
		respondErrorSimple(w, "Invalid device path", http.StatusBadRequest)
		return
	}
	if req.Timeout < 0 || req.Timeout > 251 {
		respondErrorSimple(w, "Timeout must be 0-251", http.StatusBadRequest)
		return
	}

	start := time.Now()
	output, err := cmdutil.RunFast("hdparm", "-S", fmt.Sprintf("%d", req.Timeout), req.Device)
	duration := time.Since(start)

	if err != nil {
		audit.LogAction("power_spindown", user, fmt.Sprintf("Failed: %s on %s: %s", req.Device, fmt.Sprintf("%d", req.Timeout), string(output)), false, duration)
		respondErrorSimple(w, "Operation failed", http.StatusInternalServerError)
		return
	}

	// Save config for persistence across reboots
	spindownConf := map[string]int{}
	if data, err := os.ReadFile(configPath("power-management.json")); err == nil {
		json.Unmarshal(data, &spindownConf)
	}
	spindownConf[req.Device] = req.Timeout
	os.MkdirAll(ConfigDir, 0755)
	data, err := json.MarshalIndent(spindownConf, "", "  ")
	if err != nil {
		log.Printf("WARN: failed to marshal power config: %v", err)
	} else if err := os.WriteFile(configPath("power-management.json"), data, 0644); err != nil {
		log.Printf("WARN: failed to write power config: %v", err)
	}

	audit.LogAction("power_spindown", user, fmt.Sprintf("Set spindown %d on %s", req.Timeout, req.Device), true, duration)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "output": string(output)})
}

func (h *PowerMgmtHandler) SpindownNow(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var req struct {
		Device string `json:"device"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	devicePattern := regexp.MustCompile(`^/dev/(sd[a-z]+|nvme[0-9]+n[0-9]+(p[0-9]+)?)$`)
	if !devicePattern.MatchString(req.Device) {
		respondErrorSimple(w, "Invalid device path", http.StatusBadRequest)
		return
	}

	output, err := cmdutil.RunFast("hdparm", "-y", req.Device)
	if err != nil {
		respondErrorSimple(w, "Operation failed", http.StatusInternalServerError)
		return
	}
	_ = output // hdparm -y output not needed

	audit.LogAction("power_spindown_now", user, fmt.Sprintf("Spindown %s", req.Device), true, 0)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// SyncFirewallToNix reads current ufw rules, extracts simple allow rules,
// and writes the port list to dplane-generated.nix.
// POST /api/network/firewall/sync
// Called by UI after any firewall change to keep NixOS in sync.
func (h *FirewallHandler) SyncFirewallToNix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if NixWriter == nil || !NixWriter.IsNixOS() {
		respondOK(w, map[string]interface{}{
			"success": true,
			"message": "Not on NixOS - sync is a no-op",
		})
		return
	}

	// Accept explicit port list from request body (preferred - UI knows the desired state)
	var req struct {
		TCPPorts []int `json:"tcp_ports"` // complete desired TCP allow list
		UDPPorts []int `json:"udp_ports"` // complete desired UDP allow list
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Validate port numbers
	for _, p := range append(req.TCPPorts, req.UDPPorts...) {
		if p < 1 || p > 65535 {
			respondErrorSimple(w, fmt.Sprintf("Invalid port number: %d", p), http.StatusBadRequest)
			return
		}
	}

	if err := NixWriter.SetFirewallPorts(req.TCPPorts, req.UDPPorts); err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   "Failed to write Nix firewall fragment: " + err.Error(),
		})
		return
	}

	log.Printf("[nixwriter] firewall synced: tcp=%v udp=%v", req.TCPPorts, req.UDPPorts)
	respondOK(w, map[string]interface{}{
		"success":   true,
		"tcp_ports": req.TCPPorts,
		"udp_ports": req.UDPPorts,
		"message":   "Firewall ports written to dplane-generated.nix - run nixos-rebuild switch to apply",
	})
}

