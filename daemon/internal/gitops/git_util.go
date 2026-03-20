package gitops

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
)

// BuildPushEnvForRepoID prepares environment variables for authenticated Git operations using a Repo ID.
func BuildPushEnvForRepoID(db *sql.DB, repoID int64) []string {
	var credID int
	// Find the credential ID used by this repo.
	db.QueryRow(`SELECT CAST(auth_token AS INTEGER) FROM git_sync_repos WHERE id=? AND auth_type='cred'`, repoID).Scan(&credID)
	if credID == 0 {
		return nil
	}
	return BuildPushEnvForCred(db, int64(credID))
}

// BuildPushEnvForCred prepares environment variables for authenticated Git operations using a Credential ID.
func BuildPushEnvForCred(db *sql.DB, credID int64) []string {
	var authType, token, sshKey string
	err := db.QueryRow(`SELECT auth_type, token, ssh_key FROM git_credentials WHERE id=?`, credID).Scan(&authType, &token, &sshKey)
	if err != nil {
		return nil
	}
	return BuildPushEnv(authType, token, sshKey)
}

// BuildPushEnv prepares environment variables for authenticated Git operations using raw credentials.
func BuildPushEnv(authType, token, sshKey string) []string {
	if authType == "token" && token != "" {
		tokenFile, _ := os.CreateTemp("", ".dplaneos-token-*")
		tokenFile.Write([]byte(token))
		tokenFile.Chmod(0600)
		tokenFile.Close()

		askpassFile, _ := os.CreateTemp("", ".dplaneos-askpass-*")
		script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n*Username*) echo 'x-access-token' ;;\n*) cat '%s' ;;\nesac\n", tokenFile.Name())
		askpassFile.Write([]byte(script))
		askpassFile.Chmod(0700)
		askpassFile.Close()

		return []string{
			"GIT_ASKPASS=" + askpassFile.Name(),
			"GIT_TERMINAL_PROMPT=0",
		}
	}

	if authType == "ssh" && sshKey != "" {
		keyFile, _ := os.CreateTemp("", ".dplaneos-sshkey-*")
		keyFile.Write([]byte(sshKey))
		keyFile.Chmod(0600)
		keyFile.Close()
		sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=accept-new", keyFile.Name())
		return []string{"GIT_SSH_COMMAND=" + sshCmd}
	}
	return nil
}

// CleanupAskpass removes temporary credential files created by BuildPushEnv.
func CleanupAskpass() {
	for _, pattern := range []string{"/tmp/.dplaneos-askpass-*", "/tmp/.dplaneos-token-*", "/tmp/.dplaneos-sshkey-*"} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			os.Remove(m)
		}
	}
}

// CommitAndPush performs a git add, commit, and push for the specified directory.
func CommitAndPush(dir string, env []string, commitMessage string) error {
	// 1. Add all changes
	if _, err := cmdutil.RunFastInDir(dir, "git", "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	// 2. Check if there are changes to commit
	out, _ := cmdutil.RunFastInDir(dir, "git", "status", "--short")
	if len(strings.TrimSpace(string(out))) == 0 {
		log.Printf("GIT-UTIL: no changes to commit in %s", dir)
		return nil
	}

	// 3. Commit
	if _, err := cmdutil.RunFastInDir(dir, "git", "commit", "-m", commitMessage); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	// 4. Push
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "push")
	cmd.Env = append(os.Environ(), env...)
	pout, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed: %v - %s", err, string(pout))
	}

	return nil
}

// EnsureRepoRootDir ensures the directory exists and is a git repository.
// If it's not a repo, it initializes it and sets up the remote if a URL is provided.
func EnsureRepoRootDir(dir string, remoteURL string, branch string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil // Already a repo
	}

	log.Printf("GIT-UTIL: Initializing fresh repository at %s", dir)
	if _, err := cmdutil.RunFastInDir(dir, "git", "init"); err != nil {
		return fmt.Errorf("git init: %w", err)
	}

	if remoteURL != "" {
		if _, err := cmdutil.RunFastInDir(dir, "git", "remote", "add", "origin", remoteURL); err != nil {
			return fmt.Errorf("git remote add: %w", err)
		}
	}

	if branch == "" {
		branch = "main"
	}
	// Initial empty commit to create the branch
	if _, err := cmdutil.RunFastInDir(dir, "git", "commit", "--allow-empty", "-m", "init: repository initialized via D-PlaneOS"); err != nil {
		return fmt.Errorf("git initial commit: %w", err)
	}
	if _, err := cmdutil.RunFastInDir(dir, "git", "branch", "-M", branch); err != nil {
		return fmt.Errorf("git branch rename: %w", err)
	}

	return nil
}
