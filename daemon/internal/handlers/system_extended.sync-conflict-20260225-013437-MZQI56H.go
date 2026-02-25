package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
)

// ============================================================
// SNAPSHOT SCHEDULER
// ============================================================

type SnapshotScheduleHandler struct{}

func NewSnapshotScheduleHandler() *SnapshotScheduleHandler {
	return &SnapshotScheduleHandler{}
}

type SnapshotSchedule struct {
	Dataset    string `json:"dataset"`
	Frequency  string `json:"frequency"` // hourly, daily, weekly, monthly
	Retention  int    `json:"retention"` // number of snapshots to keep
	Enabled    bool   `json:"enabled"`
	Prefix     string `json:"prefix"` // auto-hourly, auto-daily, etc.
	LastRun    string `json:"last_run,omitempty"`
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
		// No schedules yet — return empty array
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "schedules": []SnapshotSchedule{}})
		return
	}

	var schedules []SnapshotSchedule
	if err := json.Unmarshal(data, &schedules); err != nil {
		http.Error(w, "Failed to parse schedules", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "schedules": schedules})
}

func (h *SnapshotScheduleHandler) SaveSchedules(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var schedules []SnapshotSchedule
	if err := json.NewDecoder(r.Body).Decode(&schedules); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate datasets — allow alphanumeric, underscores, hyphens, slashes, dots, @ (snapshots)
	datasetPattern := regexp.MustCompile(`^[a-zA-Z0-9_\-/.@]+$`)
	for _, s := range schedules {
		if !datasetPattern.MatchString(s.Dataset) {
			http.Error(w, "Invalid dataset name: "+s.Dataset, http.StatusBadRequest)
			return
		}
		if s.Retention < 1 || s.Retention > 1000 {
			http.Error(w, "Retention must be 1-1000", http.StatusBadRequest)
			return
		}
	}

	// Save to file
	os.MkdirAll(ConfigDir, 0755)
	data, _ := json.MarshalIndent(schedules, "", "  ")
	if err := os.WriteFile(configPath("snapshot-schedules.json"), data, 0644); err != nil {
		http.Error(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}

	// Regenerate crontab entries
	h.regenerateCron(schedules)

	audit.LogAction("snapshot_schedule", user, "Updated snapshot schedules", true, 0)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (h *SnapshotScheduleHandler) regenerateCron(schedules []SnapshotSchedule) {
	cronFile := configPath("cron-snapshots")
	var lines []string
	lines = append(lines, "# D-PlaneOS Automatic Snapshot Schedules")
	lines = append(lines, "# Auto-generated — do not edit manually")
	lines = append(lines, "SHELL=/bin/bash")
	lines = append(lines, "PATH=/usr/sbin:/usr/bin:/sbin:/bin")
	lines = append(lines, "")

	for _, s := range schedules {
		if !s.Enabled {
			continue
		}
		prefix := s.Prefix
		if prefix == "" {
			prefix = "auto-" + s.Frequency
		}
		snapName := fmt.Sprintf("%s-%s-$(date +%%Y%%m%%d-%%H%%M)", prefix, s.Dataset)
		// Sanitize for cron
		snapName = strings.ReplaceAll(snapName, "/", "-")

		var cronExpr string
		switch s.Frequency {
		case "hourly":
			cronExpr = "0 * * * *"
		case "daily":
			cronExpr = "0 2 * * *"
		case "weekly":
			cronExpr = "0 3 * * 0"
		case "monthly":
			cronExpr = "0 4 1 * *"
		default:
			continue
		}

		// Snapshot + prune old ones
		cmd := fmt.Sprintf(
			`/usr/sbin/zfs snapshot %s@%s && /usr/sbin/zfs list -t snapshot -o name -s creation -H %s 2>/dev/null | grep '@%s-' | head -n -%d | xargs -r -n1 /usr/sbin/zfs destroy`,
			s.Dataset, snapName, s.Dataset, prefix, s.Retention,
		)
		lines = append(lines, fmt.Sprintf("%s root %s", cronExpr, cmd))
	}

	if err := os.WriteFile(cronFile, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		log.Printf("ERROR: failed to write cron file %s: %v", cronFile, err)
	}
}

func (h *SnapshotScheduleHandler) RunNow(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var req struct {
		Dataset string `json:"dataset"`
		Prefix  string `json:"prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	datasetPattern := regexp.MustCompile(`^[a-zA-Z0-9_\-/.@]+$`)
	if !datasetPattern.MatchString(req.Dataset) {
		http.Error(w, "Invalid dataset name", http.StatusBadRequest)
		return
	}

	snapName := fmt.Sprintf("%s-%s", req.Prefix, time.Now().Format("20060102-1504"))
	start := time.Now()
	output, err := cmdutil.RunZFS("/usr/sbin/zfs", "snapshot", req.Dataset+"@"+snapName)
	duration := time.Since(start)

	if err != nil {
		audit.LogAction("snapshot_run_now", user, fmt.Sprintf("Failed: %s@%s: %s", req.Dataset, snapName, string(output)), false, duration)
		http.Error(w, "Snapshot failed: "+string(output), http.StatusInternalServerError)
		return
	}

	audit.LogAction("snapshot_run_now", user, fmt.Sprintf("Created %s@%s", req.Dataset, snapName), true, duration)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "snapshot": req.Dataset + "@" + snapName})
}

// ============================================================
// ACL MANAGEMENT
// ============================================================

type ACLHandler struct{}

func NewACLHandler() *ACLHandler { return &ACLHandler{} }

func (h *ACLHandler) GetACL(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" || !strings.HasPrefix(path, "/mnt/") {
		http.Error(w, "Path must start with /mnt/", http.StatusBadRequest)
		return
	}

	// Get POSIX ACL
	output, err := cmdutil.RunFast("/usr/bin/getfacl", "-p", path)
	if err != nil {
		http.Error(w, "getfacl failed: "+string(output), http.StatusInternalServerError)
		return
	}

	// Get basic stat info
	statOut, _ := cmdutil.RunFast("/usr/bin/stat", "-c", "%U %G %a %F", path)

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
		Entry     string `json:"entry"` // e.g. "u:john:rwx" or "g:media:r-x"
		Recursive bool   `json:"recursive"`
		Remove    bool   `json:"remove"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(req.Path, "/mnt/") {
		http.Error(w, "Path must start with /mnt/", http.StatusBadRequest)
		return
	}

	// Validate ACL entry format: type:name:perms
	aclPattern := regexp.MustCompile(`^(u|g|o|m)(:[a-zA-Z0-9_.\-]*)?:[rwx\-]{0,3}$`)
	if !aclPattern.MatchString(req.Entry) {
		http.Error(w, "Invalid ACL entry format. Use: u:user:rwx, g:group:rx, etc.", http.StatusBadRequest)
		return
	}

	// Fix #4 (LDAP UID Showstopper): Validate user/group exists BEFORE applying ACL.
	// If LDAP is temporarily down, getent won't resolve the name → we reject early
	// instead of silently applying ACL to a numeric UID that becomes orphaned.
	entryParts := strings.SplitN(req.Entry, ":", 3)
	if len(entryParts) >= 2 && entryParts[1] != "" {
		entryType := entryParts[0]
		entryName := entryParts[1]

		if entryType == "u" {
			// Validate user exists via NSS (covers local + LDAP + SSSD)
			if out, err := cmdutil.RunFast("/usr/bin/getent", "passwd", entryName); err != nil {
				audit.LogAction("acl_validate", user, fmt.Sprintf("User '%s' not found in NSS (LDAP down?): %s", entryName, string(out)), false, 0)
				http.Error(w, fmt.Sprintf("User '%s' not found. If using LDAP, check directory service connectivity.", entryName), http.StatusBadRequest)
				return
			}
		} else if entryType == "g" {
			// Validate group exists via NSS
			if out, err := cmdutil.RunFast("/usr/bin/getent", "group", entryName); err != nil {
				audit.LogAction("acl_validate", user, fmt.Sprintf("Group '%s' not found in NSS (LDAP down?): %s", entryName, string(out)), false, 0)
				http.Error(w, fmt.Sprintf("Group '%s' not found. If using LDAP, check directory service connectivity.", entryName), http.StatusBadRequest)
				return
			}
		}
	}

	args := []string{}
	if req.Recursive {
		args = append(args, "-R")
	}
	if req.Remove {
		args = append(args, "-x", req.Entry, req.Path)
	} else {
		args = append(args, "-m", req.Entry, req.Path)
	}

	start := time.Now()
	output, err := cmdutil.RunMedium("/usr/bin/setfacl", args...)
	duration := time.Since(start)

	action := "set"
	if req.Remove {
		action = "remove"
	}

	if err != nil {
		audit.LogAction("acl_"+action, user, fmt.Sprintf("Failed on %s: %s", req.Path, string(output)), false, duration)
		http.Error(w, "setfacl failed: "+string(output), http.StatusInternalServerError)
		return
	}

	audit.LogAction("acl_"+action, user, fmt.Sprintf("ACL %s on %s: %s", action, req.Path, req.Entry), true, duration)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// ============================================================
// METRICS / REPORTING
// ============================================================

type MetricsHandler struct{}

func NewMetricsHandler() *MetricsHandler { return &MetricsHandler{} }

const metricsDir = "/var/lib/dplaneos/metrics"

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
	if cmd, err := cmdutil.RunZFS("/usr/sbin/zpool", "list", "-Hp"); err == nil {
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
	w.Write([]byte(`{"success":true,"period":"` + period + `","history":` + string(data) + `}`))
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
	if out, err := cmdutil.RunZFS("/usr/sbin/zpool", "list", "-Hp", "-o", "name,size,alloc,free,health"); err == nil {
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
	// Get ufw status
	output, err := cmdutil.RunFast("/usr/sbin/ufw", "status", "numbered")
	status := "inactive"
	if err == nil && strings.Contains(string(output), "Status: active") {
		status = "active"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"status":  status,
		"rules":   string(output),
	})
}

func (h *FirewallHandler) SetRule(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var req struct {
		Action    string `json:"action"`    // allow, deny, delete, enable, disable, reset
		Port      string `json:"port"`      // e.g. "80/tcp", "443", "22/tcp"
		From      string `json:"from"`      // source IP/CIDR (optional)
		RuleNum   int    `json:"rule_num"`  // for delete
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
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
			http.Error(w, "Port is required", http.StatusBadRequest)
			return
		}
		portPattern := regexp.MustCompile(`^[0-9]+(/tcp|/udp)?$`)
		if !portPattern.MatchString(req.Port) {
			http.Error(w, "Invalid port format", http.StatusBadRequest)
			return
		}
		if req.From != "" {
			ipPattern := regexp.MustCompile(`^[0-9a-fA-F./:]+$`)
			if !ipPattern.MatchString(req.From) {
				http.Error(w, "Invalid source IP", http.StatusBadRequest)
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
			http.Error(w, "Rule number required", http.StatusBadRequest)
			return
		}
		args = []string{"--force", "delete", fmt.Sprintf("%d", req.RuleNum)}
	default:
		http.Error(w, "Invalid action", http.StatusBadRequest)
		return
	}

	start := time.Now()
	output, err := cmdutil.RunMedium("/usr/sbin/ufw", args...)
	duration := time.Since(start)

	if err != nil {
		audit.LogAction("firewall", user, fmt.Sprintf("Failed: ufw %s: %s", strings.Join(args, " "), string(output)), false, duration)
		http.Error(w, "ufw failed: "+string(output), http.StatusInternalServerError)
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
			out, _ := cmdutil.RunFast("/usr/bin/openssl", "x509", "-in", certFile, "-noout", "-subject", "-enddate", "-issuer")
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

func (h *CertHandler) GenerateSelfSigned(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var req struct {
		Name    string `json:"name"`
		CN      string `json:"cn"` // Common Name
		Days    int    `json:"days"`
		SANs    string `json:"sans"` // comma-separated
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	namePattern := regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)
	if !namePattern.MatchString(req.Name) {
		http.Error(w, "Invalid certificate name", http.StatusBadRequest)
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
	output, err := cmdutil.RunMedium("/usr/bin/openssl", "req", "-x509", "-newkey", "rsa:2048",
		"-keyout", keyFile, "-out", certFile,
		"-days", fmt.Sprintf("%d", req.Days),
		"-nodes", "-subj", fmt.Sprintf("/CN=%s", req.CN),
		"-addext", sanExt)
	duration := time.Since(start)

	if err != nil {
		audit.LogAction("cert_generate", user, fmt.Sprintf("Failed: %s", string(output)), false, duration)
		http.Error(w, "Certificate generation failed: "+string(output), http.StatusInternalServerError)
		return
	}

	os.Chmod(keyFile, 0600)
	audit.LogAction("cert_generate", user, fmt.Sprintf("Generated self-signed cert: %s (%d days)", req.Name, req.Days), true, duration)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "cert": certFile, "key": keyFile})
}

func (h *CertHandler) ActivateCert(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	certFile := filepath.Join(configPath("ssl"), req.Name+".crt")
	keyFile := filepath.Join(configPath("ssl"), req.Name+".key")

	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		http.Error(w, "Certificate not found", http.StatusNotFound)
		return
	}
	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		http.Error(w, "Key file not found", http.StatusNotFound)
		return
	}

	// Update nginx config
	nginxConf := "/etc/nginx/sites-enabled/dplaneos"
	data, err := os.ReadFile(nginxConf)
	if err != nil {
		http.Error(w, "Cannot read nginx config", http.StatusInternalServerError)
		return
	}

	content := string(data)
	// Replace ssl_certificate and ssl_certificate_key lines
	certLine := regexp.MustCompile(`ssl_certificate\s+[^;]+;`)
	keyLine := regexp.MustCompile(`ssl_certificate_key\s+[^;]+;`)
	content = certLine.ReplaceAllString(content, "ssl_certificate "+certFile+";")
	content = keyLine.ReplaceAllString(content, "ssl_certificate_key "+keyFile+";")

	if err := os.WriteFile(nginxConf, []byte(content), 0644); err != nil {
		http.Error(w, "Cannot write nginx config", http.StatusInternalServerError)
		return
	}

	// Test nginx config
	testOut, testErr := cmdutil.RunFast("/usr/sbin/nginx", "-t")
	if testErr != nil {
		audit.LogAction("cert_activate", user, fmt.Sprintf("nginx test failed: %s", string(testOut)), false, 0)
		http.Error(w, "nginx config test failed: "+string(testOut), http.StatusInternalServerError)
		return
	}

	// Reload nginx
	cmdutil.RunFast("/usr/sbin/nginx", "-s", "reload")

	audit.LogAction("cert_activate", user, fmt.Sprintf("Activated cert: %s", req.Name), true, 0)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
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
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(req.Path, "/mnt/") {
		http.Error(w, "Can only trash files under /mnt/", http.StatusBadRequest)
		return
	}

	if _, err := os.Stat(req.Path); os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)
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
		if _, mvErr := cmdutil.RunNoTimeout("/bin/mv", req.Path, trashPath); mvErr != nil {
			audit.LogAction("trash", user, fmt.Sprintf("Failed to trash %s: %v", req.Path, mvErr), false, duration)
			http.Error(w, "Failed to move to trash: "+mvErr.Error(), http.StatusInternalServerError)
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
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	trashPath := filepath.Join(trashBase, req.Name)
	metaPath := trashPath + ".meta"

	if _, err := os.Stat(trashPath); os.IsNotExist(err) {
		http.Error(w, "Item not found in trash", http.StatusNotFound)
		return
	}

	// Read original path
	originalPath := ""
	if meta, err := os.ReadFile(metaPath); err == nil {
		originalPath = string(meta)
	}

	if originalPath == "" {
		http.Error(w, "Cannot determine original path", http.StatusInternalServerError)
		return
	}

	// Ensure parent directory exists
	os.MkdirAll(filepath.Dir(originalPath), 0755)

	// Check if target already exists
	if _, err := os.Stat(originalPath); err == nil {
		http.Error(w, "Target path already exists: "+originalPath, http.StatusConflict)
		return
	}

	err := os.Rename(trashPath, originalPath)
	if err != nil {
		if _, mvErr := cmdutil.RunNoTimeout("/bin/mv", trashPath, originalPath); mvErr != nil {
			http.Error(w, "Failed to restore: "+mvErr.Error(), http.StatusInternalServerError)
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
		http.Error(w, "Failed to empty trash: "+err.Error(), http.StatusInternalServerError)
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
	output, err := cmdutil.RunFast("/usr/bin/lsblk", "-dpno", "NAME,SIZE,MODEL,ROTA,TRAN,STATE")
	if err != nil {
		http.Error(w, "lsblk failed: "+string(output), http.StatusInternalServerError)
		return
	}

	var disks []map[string]string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		disk := map[string]string{
			"device": fields[0],
			"size":   fields[1],
			"model":  strings.Join(fields[2:len(fields)-3], " "),
		}
		if len(fields) >= 4 {
			disk["rotational"] = fields[len(fields)-3]
		}
		if len(fields) >= 5 {
			disk["transport"] = fields[len(fields)-2]
		}
		if len(fields) >= 6 {
			disk["state"] = fields[len(fields)-1]
		}

		// Get hdparm standby status
		hdOut, hdErr := cmdutil.RunFast("/usr/sbin/hdparm", "-C", fields[0])
		if hdErr == nil {
			if strings.Contains(string(hdOut), "standby") {
				disk["power_state"] = "standby"
			} else if strings.Contains(string(hdOut), "active") {
				disk["power_state"] = "active"
			}
		}

		// Get current spindown setting
		sdOut, _ := cmdutil.RunFast("/usr/sbin/hdparm", "-B", fields[0])
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
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	devicePattern := regexp.MustCompile(`^/dev/(sd[a-z]+|nvme[0-9]+n[0-9]+(p[0-9]+)?)$`)
	if !devicePattern.MatchString(req.Device) {
		http.Error(w, "Invalid device path", http.StatusBadRequest)
		return
	}
	if req.Timeout < 0 || req.Timeout > 251 {
		http.Error(w, "Timeout must be 0-251", http.StatusBadRequest)
		return
	}

	start := time.Now()
	output, err := cmdutil.RunFast("/usr/sbin/hdparm", "-S", fmt.Sprintf("%d", req.Timeout), req.Device)
	duration := time.Since(start)

	if err != nil {
		audit.LogAction("power_spindown", user, fmt.Sprintf("Failed: %s on %s: %s", req.Device, fmt.Sprintf("%d", req.Timeout), string(output)), false, duration)
		http.Error(w, "hdparm failed: "+string(output), http.StatusInternalServerError)
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
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	devicePattern := regexp.MustCompile(`^/dev/(sd[a-z]+|nvme[0-9]+n[0-9]+(p[0-9]+)?)$`)
	if !devicePattern.MatchString(req.Device) {
		http.Error(w, "Invalid device path", http.StatusBadRequest)
		return
	}

	output, err := cmdutil.RunFast("/usr/sbin/hdparm", "-y", req.Device)
	if err != nil {
		http.Error(w, "hdparm failed: "+string(output), http.StatusInternalServerError)
		return
	}

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
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if NixWriter == nil || !NixWriter.IsNixOS() {
		respondOK(w, map[string]interface{}{
			"success": true,
			"message": "Not on NixOS — sync is a no-op",
		})
		return
	}

	// Accept explicit port list from request body (preferred — UI knows the desired state)
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
		"message":   "Firewall ports written to dplane-generated.nix — run nixos-rebuild switch to apply",
	})
}
