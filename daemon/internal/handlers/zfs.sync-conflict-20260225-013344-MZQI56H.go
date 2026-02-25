package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/security"
)

type ZFSHandler struct{}

type CommandRequest struct {
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	SessionID string   `json:"session_id"`
	User      string   `json:"user"`
}

type CommandResponse struct {
	Success  bool        `json:"success"`
	Output   string      `json:"output,omitempty"`
	Error    string      `json:"error,omitempty"`
	Duration int64       `json:"duration_ms"`
	Data     interface{} `json:"data,omitempty"`
}

func NewZFSHandler() *ZFSHandler {
	return &ZFSHandler{}
}

func (h *ZFSHandler) HandleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Validate session token format
	if !security.IsValidSessionToken(req.SessionID) {
		audit.LogSecurityEvent("Invalid session token format", req.User, r.RemoteAddr)
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Validate command is whitelisted
	if err := security.ValidateCommand(req.Command, req.Args); err != nil {
		audit.LogSecurityEvent(fmt.Sprintf("Command validation failed: %v", err), req.User, r.RemoteAddr)
		respondErrorSimple(w, err.Error(), http.StatusForbidden)
		return
	}

	// Get command from whitelist
	cmd, exists := security.CommandWhitelist[req.Command]
	if !exists {
		respondErrorSimple(w, "Command not found", http.StatusNotFound)
		return
	}

	// Execute command
	start := time.Now()
	output, err := executeCommand(cmd.Path, req.Args)
	duration := time.Since(start)

	// Log the execution
	audit.LogCommand(
		audit.LevelInfo,
		req.User,
		req.Command,
		req.Args,
		err == nil,
		duration,
		err,
	)

	if err != nil {
		respondOK(w, CommandResponse{
			Success:  false,
			Error:    err.Error(),
			Duration: duration.Milliseconds(),
		})
		return
	}

	// Sanitize output
	output = security.SanitizeOutput(output)

	respondOK(w, CommandResponse{
		Success:  true,
		Output:   output,
		Duration: duration.Milliseconds(),
	})
}

func (h *ZFSHandler) ListPools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if !security.IsValidSessionToken(sessionID) {
		audit.LogSecurityEvent("Invalid session token", user, r.RemoteAddr)
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	start := time.Now()
	output, err := executeCommand("/usr/sbin/zpool", []string{"list", "-H", "-o", "name,size,alloc,free,health"})
	duration := time.Since(start)

	audit.LogCommand(audit.LevelInfo, user, "zpool_list", nil, err == nil, duration, err)

	if err != nil {
		respondOK(w, CommandResponse{
			Success:  false,
			Error:    err.Error(),
			Duration: duration.Milliseconds(),
		})
		return
	}

	pools := parseZpoolList(output)

	respondOK(w, CommandResponse{
		Success:  true,
		Data:     pools,
		Duration: duration.Milliseconds(),
	})
}

func (h *ZFSHandler) ListDatasets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if !security.IsValidSessionToken(sessionID) {
		audit.LogSecurityEvent("Invalid session token", user, r.RemoteAddr)
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	start := time.Now()
	output, err := executeCommand("/usr/sbin/zfs", []string{"list", "-H", "-o", "name,used,avail,refer,mountpoint", "-t", "filesystem"})
	duration := time.Since(start)

	audit.LogCommand(audit.LevelInfo, user, "zfs_list", nil, err == nil, duration, err)

	if err != nil {
		respondOK(w, CommandResponse{
			Success:  false,
			Error:    err.Error(),
			Duration: duration.Milliseconds(),
		})
		return
	}

	datasets := parseZfsList(output)

	respondOK(w, CommandResponse{
		Success:  true,
		Data:     datasets,
		Duration: duration.Milliseconds(),
	})
}

// CreateDataset handles POST /api/zfs/datasets
// Body: { "name": "tank/photos", "mountpoint": "/tank/photos", "quota": "100G", "compression": "lz4" }
func (h *ZFSHandler) CreateDataset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if !security.IsValidSessionToken(sessionID) {
		audit.LogSecurityEvent("Invalid session token", user, r.RemoteAddr)
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Name        string `json:"name"`        // full ZFS path: pool/child or pool/parent/child
		Mountpoint  string `json:"mountpoint"`  // optional: /tank/photos
		Quota       string `json:"quota"`       // optional: 100G, 1T
		Compression string `json:"compression"` // optional: lz4, zstd, gzip, off
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate dataset name: must be pool/name or pool/parent/child
	namePattern := regexp.MustCompile(`^[a-zA-Z0-9_\-]+(/[a-zA-Z0-9_\-]+)+$`)
	if !namePattern.MatchString(req.Name) {
		respondErrorSimple(w, "Invalid dataset name: use pool/name format, alphanumeric and - _ only", http.StatusBadRequest)
		return
	}

	start := time.Now()

	// Step 1: zfs create <name>
	if err := security.ValidateCommand("zfs_create", []string{"create", req.Name}); err != nil {
		respondErrorSimple(w, "Dataset name not allowed: "+err.Error(), http.StatusBadRequest)
		return
	}
	_, err := executeCommand("/usr/sbin/zfs", []string{"create", req.Name})
	duration := time.Since(start)
	audit.LogCommand(audit.LevelInfo, user, "zfs_create", []string{req.Name}, err == nil, duration, err)
	if err != nil {
		respondOK(w, CommandResponse{Success: false, Error: err.Error(), Duration: duration.Milliseconds()})
		return
	}

	// Step 2: set optional properties
	type prop struct{ key, val string }
	var props []prop

	if req.Compression != "" && req.Compression != "inherit" {
		allowed := map[string]bool{"lz4": true, "zstd": true, "gzip": true, "off": true}
		if allowed[req.Compression] {
			props = append(props, prop{"compression", req.Compression})
		}
	}
	if req.Quota != "" {
		quotaPattern := regexp.MustCompile(`^[0-9]+[KMGTP]?$`)
		if quotaPattern.MatchString(req.Quota) {
			props = append(props, prop{"quota", req.Quota})
		}
	}
	if req.Mountpoint != "" {
		mpPattern := regexp.MustCompile(`^/[a-zA-Z0-9_\-/]+$`)
		if mpPattern.MatchString(req.Mountpoint) {
			props = append(props, prop{"mountpoint", req.Mountpoint})
		}
	}

	for _, p := range props {
		kv := p.key + "=" + p.val
		if err2 := security.ValidateCommand("zfs_set_property", []string{"set", kv, req.Name}); err2 == nil {
			setDur := time.Now()
			_, setErr := executeCommand("/usr/sbin/zfs", []string{"set", kv, req.Name})
			audit.LogCommand(audit.LevelInfo, user, "zfs_set_property",
				[]string{kv, req.Name}, setErr == nil, time.Since(setDur), setErr)
			// Non-fatal: log but continue
		}
	}

	respondOK(w, CommandResponse{
		Success:  true,
		Output:   "Dataset " + req.Name + " created",
		Duration: time.Since(start).Milliseconds(),
	})
}


// Stderr is logged separately to prevent warning messages (e.g. "pool is DEGRADED")
// from being misinterpreted as data by the ZFS parsers.
func executeCommand(path string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		log.Printf("ZFS TIMEOUT [%s %v] after 30s", path, args)
		return stdout.String(), fmt.Errorf("command timed out after 30s: %s %v", path, args)
	}
	if stderrStr := strings.TrimSpace(stderr.String()); stderrStr != "" {
		log.Printf("ZFS stderr [%s %v]: %s", path, args, stderrStr)
	}
	return stdout.String(), err
}

// parseZpoolList parses `zpool list -H -o name,size,alloc,free,health` output.
// Uses tab-delimited split (ZFS -H flag outputs tabs) and validates field count
// to prevent partial or malformed lines from producing garbage data.
func parseZpoolList(output string) []map[string]string {
	var pools []map[string]string
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// -H output is tab-delimited
		fields := strings.Split(line, "\t")
		if len(fields) < 5 {
			// Fallback: try whitespace split for robustness
			fields = strings.Fields(line)
		}
		if len(fields) < 5 {
			log.Printf("ZFS parse warning: skipping malformed zpool line (%d fields): %q", len(fields), line)
			continue
		}

		// Validate pool name: must start with alphanumeric (skip stray warnings)
		if len(fields[0]) == 0 || !isPoolNameChar(fields[0][0]) {
			log.Printf("ZFS parse warning: skipping non-pool line: %q", line)
			continue
		}

		pools = append(pools, map[string]string{
			"name":   fields[0],
			"size":   fields[1],
			"alloc":  fields[2],
			"free":   fields[3],
			"health": fields[4],
		})
	}

	return pools
}

func isPoolNameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// parseZfsList parses `zfs list -H -o name,used,avail,refer,mountpoint` output.
// Same resilience as parseZpoolList: tab-split, field validation, malformed line skip.
func parseZfsList(output string) []map[string]string {
	var datasets []map[string]string
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) < 5 {
			fields = strings.Fields(line)
		}
		if len(fields) < 5 {
			log.Printf("ZFS parse warning: skipping malformed zfs line (%d fields): %q", len(fields), line)
			continue
		}

		if len(fields[0]) == 0 || !isPoolNameChar(fields[0][0]) {
			continue
		}

		datasets = append(datasets, map[string]string{
			"name":       fields[0],
			"used":       fields[1],
			"avail":      fields[2],
			"refer":      fields[3],
			"mountpoint": fields[4],
		})
	}

	return datasets
}

// respondJSON and respondError are defined in helpers.go
