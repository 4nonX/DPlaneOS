package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dplaned/internal/cmdutil"
)

// GitSyncHandler manages Git-based Docker stack deployment
type GitSyncHandler struct {
	db      *sql.DB
	syncMu  sync.Mutex
	syncing bool
}

func NewGitSyncHandler(db *sql.DB) *GitSyncHandler {
	return &GitSyncHandler{db: db}
}

type gitSyncConfig struct {
	RepoURL      string `json:"repo_url"`
	Branch       string `json:"branch"`
	LocalPath    string `json:"local_path"`
	SyncInterval int    `json:"sync_interval"`
	AutoDeploy   bool   `json:"auto_deploy"`
	AuthType     string `json:"auth_type"`      // "none", "token", "ssh"
	AuthToken    string `json:"auth_token"`      // PAT for HTTPS
	SSHKeyPath   string `json:"ssh_key_path"`    // path to SSH private key
	HostKeyMode  string `json:"host_key_mode"`   // "accept", "strict", "skip"
	CommitName   string `json:"commit_name"`
	CommitEmail  string `json:"commit_email"`
	LastSyncAt   string `json:"last_sync_at,omitempty"`
	LastCommit   string `json:"last_commit,omitempty"`
	LastError    string `json:"last_error,omitempty"`
}

// ═══════════════════════════════════════════════════════════════
//  GET /api/git-sync/config
// ═══════════════════════════════════════════════════════════════

func (h *GitSyncHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadConfig()
	if err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Failed to load config"})
		return
	}

	// Mask token for frontend display
	maskedToken := ""
	if cfg.AuthToken != "" {
		if len(cfg.AuthToken) > 8 {
			maskedToken = cfg.AuthToken[:4] + "****" + cfg.AuthToken[len(cfg.AuthToken)-4:]
		} else {
			maskedToken = "****"
		}
	}

	respondJSON(w, 200, map[string]interface{}{
		"success": true,
		"config": map[string]interface{}{
			"repo_url":      cfg.RepoURL,
			"branch":        cfg.Branch,
			"local_path":    cfg.LocalPath,
			"sync_interval": cfg.SyncInterval,
			"auto_deploy":   cfg.AutoDeploy,
			"auth_type":     cfg.AuthType,
			"has_token":     cfg.AuthToken != "",
			"masked_token":  maskedToken,
			"ssh_key_path":  cfg.SSHKeyPath,
			"host_key_mode": cfg.HostKeyMode,
			"commit_name":   cfg.CommitName,
			"commit_email":  cfg.CommitEmail,
			"last_sync_at":  cfg.LastSyncAt,
			"last_commit":   cfg.LastCommit,
			"last_error":    cfg.LastError,
		},
		"syncing": h.syncing,
	})
}

// ═══════════════════════════════════════════════════════════════
//  POST /api/git-sync/config
// ═══════════════════════════════════════════════════════════════

func (h *GitSyncHandler) SaveConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoURL      string `json:"repo_url"`
		Branch       string `json:"branch"`
		SyncInterval int    `json:"sync_interval"`
		AutoDeploy   bool   `json:"auto_deploy"`
		AuthType     string `json:"auth_type"`
		AuthToken    string `json:"auth_token"`
		SSHKeyPath   string `json:"ssh_key_path"`
		HostKeyMode  string `json:"host_key_mode"`
		CommitName   string `json:"commit_name"`
		CommitEmail  string `json:"commit_email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}

	if req.RepoURL != "" && !strings.HasPrefix(req.RepoURL, "http") && !strings.HasPrefix(req.RepoURL, "git@") {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "Invalid repo URL — use https:// or git@"})
		return
	}
	if req.Branch == "" {
		req.Branch = "main"
	}
	if req.CommitName == "" {
		req.CommitName = "D-PlaneOS"
	}
	if req.CommitEmail == "" {
		req.CommitEmail = "dplaneos@localhost"
	}
	if req.AuthType == "" {
		req.AuthType = "none"
	}

	autoDeploy := 0
	if req.AutoDeploy {
		autoDeploy = 1
	}

	// Default host key mode
	if req.HostKeyMode == "" {
		req.HostKeyMode = "accept"
	}

	// Only update token if user actually entered a new one
	// Empty string = user didn't touch the field → don't overwrite
	// Contains "****" = masked value echoed back → don't overwrite
	if req.AuthToken != "" && !strings.Contains(req.AuthToken, "****") {
		h.db.Exec(`UPDATE git_sync_config SET auth_token=? WHERE id=1`, req.AuthToken)
	}
	// Clear token if switching away from token auth
	if req.AuthType != "token" {
		h.db.Exec(`UPDATE git_sync_config SET auth_token='' WHERE id=1`)
	}

	_, err := h.db.Exec(`UPDATE git_sync_config SET repo_url=?, branch=?,
		sync_interval=?, auto_deploy=?, auth_type=?, ssh_key_path=?,
		host_key_mode=?, commit_name=?, commit_email=? WHERE id=1`,
		req.RepoURL, req.Branch, req.SyncInterval, autoDeploy,
		req.AuthType, req.SSHKeyPath, req.HostKeyMode, req.CommitName, req.CommitEmail)
	if err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Failed to save config"})
		return
	}

	log.Printf("GIT-SYNC: Config updated — repo=%s branch=%s auth=%s", req.RepoURL, req.Branch, req.AuthType)
	respondJSON(w, 200, map[string]interface{}{"success": true, "message": "Configuration saved"})
}

// ═══════════════════════════════════════════════════════════════
//  POST /api/git-sync/pull — Clone or pull the repo
// ═══════════════════════════════════════════════════════════════

func (h *GitSyncHandler) Pull(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadConfig()
	if err != nil || cfg.RepoURL == "" {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "No repository configured"})
		return
	}

	h.syncMu.Lock()
	if h.syncing {
		h.syncMu.Unlock()
		respondJSON(w, 409, map[string]interface{}{"success": false, "error": "Sync already in progress"})
		return
	}
	h.syncing = true
	h.syncMu.Unlock()

	defer func() {
		h.syncMu.Lock()
		h.syncing = false
		h.syncMu.Unlock()
	}()

	result, syncErr := h.doSync(cfg)

	if syncErr != nil {
		h.db.Exec(`UPDATE git_sync_config SET last_error=?, last_sync_at=? WHERE id=1`,
			syncErr.Error(), time.Now().Format(time.RFC3339))
		respondJSON(w, 200, map[string]interface{}{
			"success": false,
			"error":   syncErr.Error(),
			"output":  result,
		})
		return
	}

	// Get latest commit hash
	commit := h.getLastCommit(cfg.LocalPath)
	h.db.Exec(`UPDATE git_sync_config SET last_sync_at=?, last_commit=?, last_error='' WHERE id=1`,
		time.Now().Format(time.RFC3339), commit)

	resp := map[string]interface{}{
		"success": true,
		"message": "Repository synced",
		"commit":  commit,
	}

	// Auto-deploy if enabled
	if cfg.AutoDeploy {
		stacks := h.findComposeFiles(cfg.LocalPath)
		deployed := 0
		for _, stack := range stacks {
			dir := filepath.Dir(stack)
			out, err := cmdutil.RunSlow("docker", "compose", "--project-directory", dir, "-f", stack, "up", "-d")
			if err != nil {
				log.Printf("GIT-SYNC: Deploy failed for %s: %v — %s", stack, err, string(out))
			} else {
				deployed++
				log.Printf("GIT-SYNC: Deployed %s", dir)
			}
		}
		resp["deployed"] = deployed
		resp["total_stacks"] = len(stacks)
	}

	respondJSON(w, 200, resp)
}

// ═══════════════════════════════════════════════════════════════
//  GET /api/git-sync/status
// ═══════════════════════════════════════════════════════════════

func (h *GitSyncHandler) Status(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadConfig()
	if err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Failed to load config"})
		return
	}

	status := map[string]interface{}{
		"configured":  cfg.RepoURL != "",
		"repo_url":    cfg.RepoURL,
		"branch":      cfg.Branch,
		"syncing":     h.syncing,
		"last_sync":   cfg.LastSyncAt,
		"last_commit":  cfg.LastCommit,
		"last_error":  cfg.LastError,
		"auto_deploy": cfg.AutoDeploy,
	}

	// If repo is cloned, get git log
	if cfg.RepoURL != "" && dirExists(cfg.LocalPath+"/.git") {
		if out, err := cmdutil.RunFast("git", "-C", cfg.LocalPath, "log", "--oneline", "-5"); err == nil {
			status["recent_commits"] = strings.Split(strings.TrimSpace(string(out)), "\n")
		}
		// Fetch remote to update tracking refs (needs auth for private repos)
		env := h.buildGitEnv(cfg)
		h.runGitEnv(cfg.LocalPath, env, "fetch", "origin", cfg.Branch)
		cleanupAskpass()

		// Check how many commits we're behind
		if out, err := cmdutil.RunFast("git", "-C", cfg.LocalPath,
			"rev-list", "HEAD..origin/"+cfg.Branch, "--count"); err == nil {
			behindCount := strings.TrimSpace(string(out))
			status["behind_count"] = behindCount
			status["behind"] = behindCount != "0"
		}
		if out, err := cmdutil.RunFast("git", "-C", cfg.LocalPath, "status", "-sb"); err == nil {
			status["branch_status"] = strings.Split(string(out), "\n")[0]
		}
	}

	respondJSON(w, 200, map[string]interface{}{"success": true, "status": status})
}

// ═══════════════════════════════════════════════════════════════
//  GET /api/git-sync/stacks — List discovered compose files
// ═══════════════════════════════════════════════════════════════

func (h *GitSyncHandler) ListStacks(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadConfig()
	if err != nil || cfg.RepoURL == "" {
		respondJSON(w, 200, map[string]interface{}{"success": true, "stacks": []string{}})
		return
	}

	files := h.findComposeFiles(cfg.LocalPath)
	stacks := []map[string]interface{}{}

	for _, f := range files {
		rel, _ := filepath.Rel(cfg.LocalPath, f)
		dir := filepath.Dir(rel)
		name := dir
		if name == "." {
			name = "root"
		}

		stack := map[string]interface{}{
			"name": name,
			"path": f,
			"file": filepath.Base(f),
		}

		// Check if stack is running
		out, err := cmdutil.RunFast("docker", "compose", "--project-directory", filepath.Dir(f), "-f", f, "ps", "--format", "json")
		if err == nil && len(out) > 2 {
			stack["running"] = true
		} else {
			stack["running"] = false
		}

		stacks = append(stacks, stack)
	}

	respondJSON(w, 200, map[string]interface{}{"success": true, "stacks": stacks})
}

// ═══════════════════════════════════════════════════════════════
//  POST /api/git-sync/deploy — Deploy specific or all stacks
// ═══════════════════════════════════════════════════════════════

func (h *GitSyncHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Stack string `json:"stack"` // specific compose file, empty = all
		Down  bool   `json:"down"`  // tear down instead
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}

	cfg, err := h.loadConfig()
	if err != nil || cfg.RepoURL == "" {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "No repository configured"})
		return
	}

	var files []string
	if req.Stack != "" {
		// Validate the stack path is under our managed directory
		clean := filepath.Clean(req.Stack)
		if !strings.HasPrefix(clean, cfg.LocalPath) {
			respondJSON(w, 403, map[string]interface{}{"success": false, "error": "Path not allowed"})
			return
		}
		files = []string{clean}
	} else {
		files = h.findComposeFiles(cfg.LocalPath)
	}

	results := []map[string]interface{}{}
	for _, f := range files {
		dir := filepath.Dir(f)
		action := "up"
		args := []string{"compose", "--project-directory", dir, "-f", f, "up", "-d"}
		if req.Down {
			action = "down"
			args = []string{"compose", "--project-directory", dir, "-f", f, "down"}
		}

		out, err := cmdutil.RunSlow("docker", args...)
		result := map[string]interface{}{
			"stack":   f,
			"action":  action,
			"success": err == nil,
		}
		if err != nil {
			result["error"] = err.Error()
			result["output"] = string(out)
		}
		results = append(results, result)
		log.Printf("GIT-SYNC: %s %s — success=%v", action, f, err == nil)
	}

	respondJSON(w, 200, map[string]interface{}{"success": true, "results": results})
}

// ═══════════════════════════════════════════════════════════════
//  INTERNAL HELPERS
// ═══════════════════════════════════════════════════════════════

func (h *GitSyncHandler) loadConfig() (*gitSyncConfig, error) {
	var cfg gitSyncConfig
	var autoDeploy int
	err := h.db.QueryRow(`SELECT repo_url, branch, local_path, sync_interval, auto_deploy,
		auth_type, auth_token, ssh_key_path, host_key_mode, commit_name, commit_email,
		COALESCE(last_sync_at,''), COALESCE(last_commit,''), COALESCE(last_error,'')
		FROM git_sync_config WHERE id = 1`).Scan(
		&cfg.RepoURL, &cfg.Branch, &cfg.LocalPath, &cfg.SyncInterval, &autoDeploy,
		&cfg.AuthType, &cfg.AuthToken, &cfg.SSHKeyPath, &cfg.HostKeyMode, &cfg.CommitName, &cfg.CommitEmail,
		&cfg.LastSyncAt, &cfg.LastCommit, &cfg.LastError)
	cfg.AutoDeploy = autoDeploy == 1
	return &cfg, err
}

func (h *GitSyncHandler) doSync(cfg *gitSyncConfig) (string, error) {
	os.MkdirAll(filepath.Dir(cfg.LocalPath), 0755)

	env := h.buildGitEnv(cfg)
	defer cleanupAskpass()

	if dirExists(cfg.LocalPath + "/.git") {
		log.Printf("GIT-SYNC: Pulling %s (branch: %s)", cfg.RepoURL, cfg.Branch)
		return h.runGitEnv(cfg.LocalPath, env, "pull", "origin", cfg.Branch)
	}

	log.Printf("GIT-SYNC: Cloning %s (branch: %s) to %s", cfg.RepoURL, cfg.Branch, cfg.LocalPath)
	return h.runGitGlobalEnv(env, "clone", "--branch", cfg.Branch, "--single-branch", cfg.RepoURL, cfg.LocalPath)
}

// buildGitEnv creates environment variables for git auth
// Uses GIT_ASKPASS for tokens (never leaks token into URLs or logs)
// Uses GIT_SSH_COMMAND for SSH keys with configurable host key verification
func (h *GitSyncHandler) buildGitEnv(cfg *gitSyncConfig) []string {
	var env []string

	if cfg.AuthType == "token" && cfg.AuthToken != "" {
		// Write token to a secure temp file, then askpass script reads it
		// This avoids ALL shell escaping issues regardless of token content
		tokenFile, err := os.CreateTemp("", ".dplaneos-token-*")
		if err != nil {
			log.Printf("GIT-SYNC: Failed to create token temp file: %v", err)
			return env
		}
		tokenFile.Write([]byte(cfg.AuthToken))
		tokenFile.Chmod(0600)
		tokenFile.Close()

		askpassFile, err := os.CreateTemp("", ".dplaneos-askpass-*")
		if err != nil {
			log.Printf("GIT-SYNC: Failed to create askpass temp file: %v", err)
			os.Remove(tokenFile.Name())
			return env
		}
		// Script reads token from file — zero shell escaping needed
		script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n*Username*) echo 'x-access-token' ;;\n*) cat '%s' ;;\nesac\n",
			tokenFile.Name())
		askpassFile.Write([]byte(script))
		askpassFile.Chmod(0700)
		askpassFile.Close()

		env = append(env, "GIT_ASKPASS="+askpassFile.Name())
		env = append(env, "GIT_TERMINAL_PROMPT=0")
	}

	if cfg.AuthType == "ssh" && cfg.SSHKeyPath != "" {
		sshCmd := "ssh -i " + cfg.SSHKeyPath
		switch cfg.HostKeyMode {
		case "skip":
			sshCmd += " -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
		case "strict":
			sshCmd += " -o StrictHostKeyChecking=yes"
		default:
			sshCmd += " -o StrictHostKeyChecking=accept-new"
		}
		env = append(env, "GIT_SSH_COMMAND="+sshCmd)
	}

	return env
}

// cleanupAskpass removes any temporary askpass and token files
func cleanupAskpass() {
	for _, pattern := range []string{"/tmp/.dplaneos-askpass-*", "/tmp/.dplaneos-token-*"} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			os.Remove(m)
		}
	}
}

// runGitEnv runs git -C dir with custom environment
func (h *GitSyncHandler) runGitEnv(dir string, env []string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", dir}, args...)
	if len(env) > 0 {
		return h.execGitWithEnv(env, fullArgs...)
	}
	out, err := cmdutil.RunMedium("git", fullArgs...)
	return string(out), err
}

// runGitGlobalEnv runs git (no -C) with custom environment
func (h *GitSyncHandler) runGitGlobalEnv(env []string, args ...string) (string, error) {
	if len(env) > 0 {
		return h.execGitWithEnv(env, args...)
	}
	out, err := cmdutil.RunSlow("git", args...)
	return string(out), err
}

// execGitWithEnv runs git with custom environment variables
func (h *GitSyncHandler) execGitWithEnv(env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ═══════════════════════════════════════════════════════════════
//  POST /api/git-sync/export — Export running containers to compose.yml
// ═══════════════════════════════════════════════════════════════

func (h *GitSyncHandler) ExportContainers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Containers []string `json:"containers"` // names/IDs, empty = all
		StackName  string   `json:"stack_name"`  // subdirectory name in repo
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}
	if req.StackName == "" {
		req.StackName = "exported"
	}

	// Get running containers via docker inspect
	args := []string{"inspect"}
	if len(req.Containers) > 0 {
		args = append(args, req.Containers...)
	} else {
		// Get all running container names
		out, err := cmdutil.RunFast("docker", "ps", "--format", "{{.Names}}")
		if err != nil {
			respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Failed to list containers"})
			return
		}
		names := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(names) == 0 || (len(names) == 1 && names[0] == "") {
			respondJSON(w, 200, map[string]interface{}{"success": false, "error": "No running containers found"})
			return
		}
		args = append(args, names...)
	}

	out, err := cmdutil.RunMedium("docker", args...)
	if err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "docker inspect failed: " + err.Error()})
		return
	}

	// Parse inspect JSON
	var containers []map[string]interface{}
	if err := json.Unmarshal(out, &containers); err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Failed to parse inspect output"})
		return
	}

	// Generate compose YAML
	compose := h.generateCompose(containers)

	respondJSON(w, 200, map[string]interface{}{
		"success":  true,
		"yaml":     compose,
		"services": len(containers),
	})
}

// ═══════════════════════════════════════════════════════════════
//  POST /api/git-sync/push — Commit and push to remote
// ═══════════════════════════════════════════════════════════════

func (h *GitSyncHandler) Push(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StackName string `json:"stack_name"` // subdirectory
		Yaml      string `json:"yaml"`       // compose content
		Message   string `json:"message"`    // commit message
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}

	cfg, err := h.loadConfig()
	if err != nil || cfg.RepoURL == "" {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "No repository configured"})
		return
	}

	if !dirExists(cfg.LocalPath + "/.git") {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "Repository not cloned yet — pull first"})
		return
	}

	if req.Yaml == "" {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "No YAML content provided"})
		return
	}
	if req.StackName == "" {
		req.StackName = "exported"
	}
	if req.Message == "" {
		req.Message = fmt.Sprintf("Export stack: %s (via D-PlaneOS)", req.StackName)
	}

	// Sanitize stack name
	req.StackName = filepath.Base(filepath.Clean(req.StackName))

	// Write compose file
	stackDir := filepath.Join(cfg.LocalPath, req.StackName)
	os.MkdirAll(stackDir, 0755)
	composePath := filepath.Join(stackDir, "docker-compose.yml")

	if err := os.WriteFile(composePath, []byte(req.Yaml), 0644); err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Failed to write compose file: " + err.Error()})
		return
	}

	// Configure git identity
	h.runGitEnv(cfg.LocalPath, nil, "config", "user.name", cfg.CommitName)
	h.runGitEnv(cfg.LocalPath, nil, "config", "user.email", cfg.CommitEmail)

	// Git add + commit
	h.runGitEnv(cfg.LocalPath, nil, "add", req.StackName+"/")
	commitOut, commitErr := h.runGitEnv(cfg.LocalPath, nil, "commit", "-m", req.Message)
	if commitErr != nil {
		if strings.Contains(commitOut, "nothing to commit") {
			respondJSON(w, 200, map[string]interface{}{"success": true, "message": "No changes to commit"})
			return
		}
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Commit failed: " + commitOut})
		return
	}

	// Push with proper auth (same GIT_ASKPASS/SSH as pull)
	env := h.buildGitEnv(cfg)
	defer cleanupAskpass()
	pushOut, pushErr := h.runGitEnv(cfg.LocalPath, env, "push", "origin", cfg.Branch)
	if pushErr != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Push failed: " + pushOut, "hint": "Check your authentication settings"})
		return
	}

	commit := h.getLastCommit(cfg.LocalPath)
	h.db.Exec(`UPDATE git_sync_config SET last_commit=?, last_sync_at=? WHERE id=1`,
		commit, time.Now().Format(time.RFC3339))

	log.Printf("GIT-SYNC: Pushed stack '%s' to %s/%s", req.StackName, cfg.RepoURL, cfg.Branch)
	respondJSON(w, 200, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Pushed to %s", cfg.Branch),
		"commit":  commit,
	})
}

// generateCompose builds a docker-compose.yml from inspect data
func (h *GitSyncHandler) generateCompose(containers []map[string]interface{}) string {
	var b strings.Builder
	b.WriteString("# Generated by D-PlaneOS Git Sync\n")
	b.WriteString("# " + time.Now().Format(time.RFC3339) + "\n\n")
	b.WriteString("services:\n")

	for _, c := range containers {
		name := getString(c, "Name")
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			continue
		}

		config := getMap(c, "Config")
		hostConfig := getMap(c, "HostConfig")
		networkSettings := getMap(c, "NetworkSettings")

		image := getString(config, "Image")
		b.WriteString(fmt.Sprintf("  %s:\n", name))
		b.WriteString(fmt.Sprintf("    image: %s\n", image))
		b.WriteString("    restart: unless-stopped\n")

		// Environment variables
		if envList, ok := config["Env"].([]interface{}); ok && len(envList) > 0 {
			filtered := filterEnv(envList)
			if len(filtered) > 0 {
				b.WriteString("    environment:\n")
				for _, e := range filtered {
					b.WriteString(fmt.Sprintf("      - %s\n", e))
				}
			}
		}

		// Ports
		if ports := getMap(hostConfig, "PortBindings"); len(ports) > 0 {
			b.WriteString("    ports:\n")
			for containerPort, bindings := range ports {
				if bindList, ok := bindings.([]interface{}); ok {
					for _, bind := range bindList {
						if bm, ok := bind.(map[string]interface{}); ok {
							hostPort := getString(bm, "HostPort")
							cp := strings.Split(containerPort, "/")[0]
							proto := ""
							if strings.HasSuffix(containerPort, "/udp") {
								proto = "/udp"
							}
							b.WriteString(fmt.Sprintf("      - \"%s:%s%s\"\n", hostPort, cp, proto))
						}
					}
				}
			}
		}

		// Volumes
		if mounts, ok := c["Mounts"].([]interface{}); ok && len(mounts) > 0 {
			b.WriteString("    volumes:\n")
			for _, m := range mounts {
				if mount, ok := m.(map[string]interface{}); ok {
					src := getString(mount, "Source")
					dst := getString(mount, "Destination")
					if src != "" && dst != "" {
						mode := ""
						if rw, ok := mount["RW"].(bool); ok && !rw {
							mode = ":ro"
						}
						b.WriteString(fmt.Sprintf("      - %s:%s%s\n", src, dst, mode))
					}
				}
			}
		}

		// Networks
		if nets := getMap(networkSettings, "Networks"); len(nets) > 0 {
			netNames := []string{}
			for netName := range nets {
				if netName != "bridge" && netName != "host" && netName != "none" {
					netNames = append(netNames, netName)
				}
			}
			if len(netNames) > 0 {
				b.WriteString("    networks:\n")
				for _, n := range netNames {
					b.WriteString(fmt.Sprintf("      - %s\n", n))
				}
			}
		}

		// Labels (filter out internal docker labels)
		if labels, ok := config["Labels"].(map[string]interface{}); ok {
			userLabels := map[string]string{}
			for k, v := range labels {
				if !strings.HasPrefix(k, "com.docker.") && !strings.HasPrefix(k, "org.opencontainers.") {
					userLabels[k] = fmt.Sprintf("%v", v)
				}
			}
			if len(userLabels) > 0 {
				b.WriteString("    labels:\n")
				for k, v := range userLabels {
					b.WriteString(fmt.Sprintf("      %s: \"%s\"\n", k, v))
				}
			}
		}

		b.WriteString("\n")
	}

	return b.String()
}

// Helper functions for safe type assertions
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getMap(m map[string]interface{}, key string) map[string]interface{} {
	if v, ok := m[key].(map[string]interface{}); ok {
		return v
	}
	return map[string]interface{}{}
}

func filterEnv(envList []interface{}) []string {
	// Filter out common default env vars that aren't user-configured
	skip := map[string]bool{"PATH": true, "HOME": true, "HOSTNAME": true, "TERM": true}
	var result []string
	for _, e := range envList {
		s, ok := e.(string)
		if !ok {
			continue
		}
		key := strings.SplitN(s, "=", 2)[0]
		if !skip[key] {
			result = append(result, s)
		}
	}
	return result
}

func (h *GitSyncHandler) getLastCommit(localPath string) string {
	out, err := cmdutil.RunFast("git", "-C", localPath, "log", "-1", "--format=%H %s")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (h *GitSyncHandler) findComposeFiles(root string) []string {
	var files []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Skip .git directory
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		name := info.Name()
		if name == "docker-compose.yml" || name == "docker-compose.yaml" ||
			name == "compose.yml" || name == "compose.yaml" {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ═══════════════════════════════════════════════════════════════
//  AUTO-SYNC BACKGROUND WORKER
// ═══════════════════════════════════════════════════════════════

func (h *GitSyncHandler) StartAutoSync() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			cfg, err := h.loadConfig()
			if err != nil || cfg.RepoURL == "" || cfg.SyncInterval <= 0 {
				continue
			}

			// Check if enough time has passed
			if cfg.LastSyncAt != "" {
				lastSync, err := time.Parse(time.RFC3339, cfg.LastSyncAt)
				if err == nil && time.Since(lastSync) < time.Duration(cfg.SyncInterval)*time.Minute {
					continue
				}
			}

			h.syncMu.Lock()
			if h.syncing {
				h.syncMu.Unlock()
				continue
			}
			h.syncing = true
			h.syncMu.Unlock()

			log.Printf("GIT-SYNC: Auto-sync triggered (interval: %dm)", cfg.SyncInterval)
			_, syncErr := h.doSync(cfg)

			if syncErr != nil {
				h.db.Exec(`UPDATE git_sync_config SET last_error=?, last_sync_at=? WHERE id=1`,
					syncErr.Error(), time.Now().Format(time.RFC3339))
				log.Printf("GIT-SYNC: Auto-sync failed: %v", syncErr)
			} else {
				commit := h.getLastCommit(cfg.LocalPath)
				h.db.Exec(`UPDATE git_sync_config SET last_sync_at=?, last_commit=?, last_error='' WHERE id=1`,
					time.Now().Format(time.RFC3339), commit)
				log.Printf("GIT-SYNC: Auto-sync complete — %s", commit)

				// Auto-deploy if enabled
				if cfg.AutoDeploy {
					stacks := h.findComposeFiles(cfg.LocalPath)
					for _, stack := range stacks {
						if out, err := cmdutil.RunSlow("docker", "compose", "--project-directory", filepath.Dir(stack), "-f", stack, "up", "-d"); err != nil {
							log.Printf("GIT-SYNC: Auto-deploy failed for %s: %v — %s", stack, err, string(out))
						}
					}
				}
			}

			h.syncMu.Lock()
			h.syncing = false
			h.syncMu.Unlock()
		}
	}()
}
