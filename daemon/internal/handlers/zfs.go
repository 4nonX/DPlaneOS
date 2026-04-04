package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	"database/sql"

	"dplaned/internal/audit"
	"dplaned/internal/gitops"
	"dplaned/internal/security"
)

type ZFSHandler struct {
	db *sql.DB
}

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

func NewZFSHandler(db *sql.DB) *ZFSHandler {
	return &ZFSHandler{db: db}
}

func (h *ZFSHandler) HandleCommand(w http.ResponseWriter, r *http.Request) {
	if err := checkBinary("zfs"); err != nil {
		respondErrorSimple(w, "ZFS binary not found", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
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

	// GITOPS HOOK: write state back to git if it was a mutating command
	mutating := map[string]bool{
		"zfs_create": true, "zfs_destroy": true, "zfs_set_property": true,
		"zpool_create": true, "zpool_destroy": true, "zpool_add": true,
	}
	if mutating[req.Command] {
		gitops.CommitAllAsync(h.db)
	}
}

func (h *ZFSHandler) ListPools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
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
	// cap = capacity percentage. 'type' is not a valid zpool list column (it's zfs list only).
	output, err := executeCommand("zpool", []string{"list", "-H", "-o", "name,size,alloc,free,cap,health"})
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
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
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
	args := []string{"list", "-H", "-o", "name,used,avail,quota,mountpoint,compression,compressratio,refcompressratio", "-t", "filesystem"}
	pool := r.URL.Query().Get("pool")
	if pool != "" {
		args = append(args, "-r", pool)
	}
	output, err := executeCommand("zfs", args)
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
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
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
		Name            string `json:"name"`
		Mountpoint      string `json:"mountpoint"`
		Quota           string `json:"quota"`
		Compression     string `json:"compression"`
		Atime           string `json:"atime"`
		Sync            string `json:"sync"`
		Recordsize      string `json:"recordsize"`
		Xattr           string `json:"xattr"`
		Secondarycache  string `json:"secondarycache"`
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
	_, err := executeCommand("zfs", []string{"create", req.Name})
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
		kv := "compression=" + req.Compression
		if security.ValidateCommand("zfs_set_property", []string{"set", kv, req.Name}) == nil {
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
	addProp := func(key, val string) {
		if val == "" {
			return
		}
		kv := key + "=" + val
		if err2 := security.ValidateCommand("zfs_set_property", []string{"set", kv, req.Name}); err2 == nil {
			props = append(props, prop{key, val})
		}
	}
	addProp("atime", req.Atime)
	addProp("sync", req.Sync)
	addProp("recordsize", req.Recordsize)
	addProp("xattr", req.Xattr)
	addProp("secondarycache", req.Secondarycache)

	for _, p := range props {
		kv := p.key + "=" + p.val
		if err2 := security.ValidateCommand("zfs_set_property", []string{"set", kv, req.Name}); err2 == nil {
			setDur := time.Now()
			_, setErr := executeCommand("zfs", []string{"set", kv, req.Name})
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

	// GITOPS HOOK: write state back to git
	gitops.CommitAllAsync(h.db)
}

// Stderr is logged separately to prevent warning messages (e.g. "pool is DEGRADED")
// from being misinterpreted as data by the ZFS parsers.

// parseZpoolList parses `zpool list -H -o name,size,alloc,free,cap,health` output.
// Note: 'type' is not a valid zpool list property (it is a zfs list property).
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

		pool := map[string]string{
			"name":  fields[0],
			"size":  fields[1],
			"alloc": fields[2],
			"free":  fields[3],
		}
		// cap and health are the 5th and 6th columns
		if len(fields) > 4 {
			pool["capacity"] = fields[4]
		}
		if len(fields) > 5 {
			pool["health"] = fields[5]
		}
		// compression and dedup are not in `zpool list` - set empty so frontend
		// knows they're unavailable (these come from `zfs get` per-dataset, not pool level)
		pool["compression"] = ""
		pool["dedup"] = ""

		pools = append(pools, pool)
	}

	return pools
}

func isPoolNameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// parseZfsList parses `zfs list -H -o name,used,avail,quota,mountpoint,compression,compressratio,refcompressratio` output.
// Same resilience as parseZpoolList: tab-split, field validation, malformed line skip.
// Extra ratio columns require OpenZFS (refcompressratio); if zfs rejects -o, ListDatasets fails visibly.
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

		row := map[string]string{
			"name":       fields[0],
			"used":       fields[1],
			"avail":      fields[2],
			"quota":      fields[3],
			"mountpoint": fields[4],
		}
		if len(fields) > 5 {
			row["compression"] = fields[5]
		}
		if len(fields) > 6 {
			row["compressratio"] = fields[6]
		}
		if len(fields) > 7 {
			row["refcompressratio"] = fields[7]
		}
		datasets = append(datasets, row)
	}

	return datasets
}

// respondJSON and respondError are defined in helpers.go

