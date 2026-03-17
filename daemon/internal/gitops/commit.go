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
	"dplaned/internal/config"
)

const stateFileName = "state.yaml"

// CommitAll reads the current live state and writes it back to the Git repo,
// then performs a git commit and push.
// This is the post-write hook for all UI-driven infrastructure changes.
func CommitAll(db *sql.DB) error {
	// 0. Check if GitOps is enabled and configured
	var enabled int
	var repoID sql.NullInt64
	var storage, access, app, identity, protection, system int
	err := db.QueryRow(`SELECT enabled, repo_id, sync_storage, sync_access, sync_app, 
		sync_identity, sync_protection, sync_system FROM gitops_config WHERE id = 1`).Scan(
		&enabled, &repoID, &storage, &access, &app, &identity, &protection, &system)

	if err != nil {
		return fmt.Errorf("loading gitops config: %w", err)
	}

	if enabled == 0 {
		return nil // Disabled, skip commit
	}

	// 1. Read Live State and Filter
	state, err := ReadLiveState(db)
	if err != nil {
		return fmt.Errorf("reading live state for commit: %w", err)
	}

	// Apply granular filters
	if storage == 0 {
		state.Pools = nil
		state.Datasets = nil
	}
	if access == 0 {
		state.Shares = nil
	}
	if app == 0 {
		state.Stacks = nil
	}
	if identity == 0 {
		state.Users = nil
		state.Groups = nil
		state.LDAP = nil
	}
	if protection == 0 {
		state.Replication = nil
	}
	// System config filtering logic would go here if implemented in state.go

	repoDir := config.GitOpsStateDir
	
	// 2. Generate YAML
	yamlContent := GenerateStateYAML(state)

	// 3. Ensure repo exists and is a git repo
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		log.Printf("GITOPS COMMIT: %s is not a git repository, skipping push", repoDir)
		return saveStateLocally(repoDir, yamlContent)
	}

	// 4. Write state.yaml
	statePath := filepath.Join(repoDir, stateFileName)
	if err := os.WriteFile(statePath, []byte(yamlContent), 0644); err != nil {
		return fmt.Errorf("writing state.yaml: %w", err)
	}

	// 5. Load Credentials for Push
	var env []string
	if repoID.Valid {
		env = buildPushEnv(db, repoID.Int64)
		defer cleanupAskpass()
	}

	// 6. Git Commit & Push
	return gitCommitAndPush(repoDir, env, "feat: infrastructure state update via D-PlaneOS")
}

// GenerateStateYAML converts LiveState into the declarative state.yaml format.
func GenerateStateYAML(state *LiveState) string {
	var sb strings.Builder

	sb.WriteString("version: \"1\"\n\n")

	if len(state.Pools) > 0 {
		sb.WriteString("pools:\n")
		for _, p := range state.Pools {
			sb.WriteString(fmt.Sprintf("  - name: %q\n", p.Name))
			if len(p.Disks) > 0 {
				sb.WriteString("    disks: [")
				for i, d := range p.Disks {
					if i > 0 {
						sb.WriteString(", ")
					}
					sb.WriteString(fmt.Sprintf("%q", d))
				}
				sb.WriteString("]\n")
			}
		}
		sb.WriteString("\n")
	}

	if len(state.Datasets) > 0 {
		sb.WriteString("datasets:\n")
		for _, d := range state.Datasets {
			// Skip root datasets (pool names)
			if !strings.Contains(d.Name, "/") {
				continue
			}
			sb.WriteString(fmt.Sprintf("  - name: %q\n", d.Name))
			if d.Quota != "none" && d.Quota != "0" && d.Quota != "" {
				sb.WriteString(fmt.Sprintf("    quota: %q\n", d.Quota))
			}
			if d.Compression != "off" && d.Compression != "" {
				sb.WriteString(fmt.Sprintf("    compression: %q\n", d.Compression))
			}
		}
		sb.WriteString("\n")
	}

	if len(state.Shares) > 0 {
		sb.WriteString("shares:\n")
		for _, s := range state.Shares {
			sb.WriteString(fmt.Sprintf("  - name: %q\n", s.Name))
			sb.WriteString(fmt.Sprintf("    path: %q\n", s.Path))
			if s.ReadOnly {
				sb.WriteString("    read_only: true\n")
			}
			if s.ValidUsers != "" {
				sb.WriteString(fmt.Sprintf("    valid_users: %q\n", s.ValidUsers))
			}
			if s.GuestOK {
				sb.WriteString("    guest_ok: true\n")
			}
		}
		sb.WriteString("\n")
	}

	if len(state.Stacks) > 0 {
		sb.WriteString("stacks:\n")
		for _, st := range state.Stacks {
			sb.WriteString(fmt.Sprintf("  - name: %q\n", st.Name))
			sb.WriteString("    yaml: |\n")
			lines := strings.Split(strings.TrimSpace(st.YAML), "\n")
			for _, line := range lines {
				sb.WriteString(fmt.Sprintf("      %s\n", line))
			}
		}
		sb.WriteString("\n")
	}

	if len(state.Users) > 0 {
		sb.WriteString("users:\n")
		for _, u := range state.Users {
			sb.WriteString(fmt.Sprintf("  - username: %q\n", u.Username))
			if u.Email != "" {
				sb.WriteString(fmt.Sprintf("    email: %q\n", u.Email))
			}
			if u.Role != "" {
				sb.WriteString(fmt.Sprintf("    role: %q\n", u.Role))
			}
			if !u.Active {
				sb.WriteString("    active: false\n")
			}
		}
		sb.WriteString("\n")
	}

	if len(state.Groups) > 0 {
		sb.WriteString("groups:\n")
		for _, g := range state.Groups {
			sb.WriteString(fmt.Sprintf("  - name: %q\n", g.Name))
			if g.Description != "" {
				sb.WriteString(fmt.Sprintf("    description: %q\n", g.Description))
			}
			if g.GID != 0 {
				sb.WriteString(fmt.Sprintf("    gid: %d\n", g.GID))
			}
			if len(g.Members) > 0 {
				sb.WriteString("    members: [")
				for i, m := range g.Members {
					if i > 0 { sb.WriteString(", ") }
					sb.WriteString(fmt.Sprintf("%q", m))
				}
				sb.WriteString("]\n")
			}
		}
		sb.WriteString("\n")
	}

	if len(state.Replication) > 0 {
		sb.WriteString("replication:\n")
		for _, r := range state.Replication {
			sb.WriteString(fmt.Sprintf("  - name: %q\n", r.Name))
			sb.WriteString(fmt.Sprintf("    source_dataset: %q\n", r.SourceDataset))
			sb.WriteString(fmt.Sprintf("    remote_host: %q\n", r.RemoteHost))
			sb.WriteString(fmt.Sprintf("    remote_port: %d\n", r.RemotePort))
			sb.WriteString(fmt.Sprintf("    interval: %q\n", r.Interval))
			if !r.Enabled {
				sb.WriteString("    enabled: false\n")
			}
		}
		sb.WriteString("\n")
	}

	if state.LDAP != nil {
		sb.WriteString("ldap:\n")
		sb.WriteString(fmt.Sprintf("  enabled: %v\n", state.LDAP.Enabled))
		sb.WriteString(fmt.Sprintf("  server: %q\n", state.LDAP.Server))
		sb.WriteString(fmt.Sprintf("  port: %d\n", state.LDAP.Port))
		sb.WriteString(fmt.Sprintf("  use_tls: %v\n", state.LDAP.UseTLS))
		sb.WriteString(fmt.Sprintf("  bind_dn: %q\n", state.LDAP.BindDN))
		sb.WriteString(fmt.Sprintf("  base_dn: %q\n", state.LDAP.BaseDN))
		sb.WriteString(fmt.Sprintf("  user_filter: %q\n", state.LDAP.UserFilter))
		sb.WriteString(fmt.Sprintf("  jit_provisioning: %v\n", state.LDAP.JITProvisioning))
		sb.WriteString(fmt.Sprintf("  default_role: %q\n", state.LDAP.DefaultRole))
		sb.WriteString(fmt.Sprintf("  sync_interval: %d\n", state.LDAP.SyncInterval))
		sb.WriteString(fmt.Sprintf("  timeout: %d\n", state.LDAP.Timeout))
		sb.WriteString("\n")
	}

	return sb.String()
}

func saveStateLocally(dir, content string) error {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, stateFileName), []byte(content), 0644)
}

func gitCommitAndPush(dir string, env []string, message string) error {
	// Add
	if _, err := cmdutil.RunFastInDir(dir, "git", "add", stateFileName); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	// Check if there are changes
	out, _ := cmdutil.RunFastInDir(dir, "git", "status", "--short")
	if len(strings.TrimSpace(string(out))) == 0 {
		log.Printf("GITOPS COMMIT: no changes to commit")
		return nil
	}

	// Commit
	if _, err := cmdutil.RunFastInDir(dir, "git", "commit", "-m", message); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	// Push
	if len(env) > 0 {
		log.Printf("GITOPS COMMIT: pushing with authenticated environment")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "push")
		cmd.Env = append(os.Environ(), env...)
		pout, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("GITOPS COMMIT WARNING: authenticated git push failed: %v - %s", err, string(pout))
		}
	} else {
		if _, err := cmdutil.RunMediumInDir(dir, "git", "push"); err != nil {
			log.Printf("GITOPS COMMIT WARNING: git push failed (check upstream connection): %v", err)
		}
	}

	return nil
}

// ── Authenticated Git Helpers (Internal Copy to avoid cycle) ──────────────────

func buildPushEnv(db *sql.DB, repoID int64) []string {
	var credID int
	db.QueryRow(`SELECT CAST(auth_token AS INTEGER) FROM git_sync_repos WHERE id=? AND auth_type='cred'`, repoID).Scan(&credID)
	if credID == 0 {
		return nil
	}

	var authType, token, sshKey string
	err := db.QueryRow(`SELECT auth_type, token, ssh_key FROM git_credentials WHERE id=?`, credID).Scan(&authType, &token, &sshKey)
	if err != nil {
		return nil
	}

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

func cleanupAskpass() {
	for _, pattern := range []string{"/tmp/.dplaneos-askpass-*", "/tmp/.dplaneos-token-*", "/tmp/.dplaneos-sshkey-*"} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			os.Remove(m)
		}
	}
}

