package gitops

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dplaned/internal/config"
)

const stateFileName = "state.yaml"

// CommitAllAsync performs CommitAll in a background goroutine and logs any errors.
// Use this for UI handlers where we don't want to block the response.
func CommitAllAsync(db *sql.DB) {
	go func() {
		if err := CommitAll(db); err != nil {
			log.Printf("GITOPS: background commit failed: %v", err)
		}
	}()
}

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
	if system == 0 {
		state.System = nil
	}

	repoDir := config.GitOpsStateDir
	
	// 2. Generate YAML
	yamlContent := GenerateStateYAML(state)

	// 3. Get repository details for authentication and identity
	var repoURL, branch, commitName, commitEmail sql.NullString
	if repoID.Valid {
		if err := db.QueryRow(`SELECT repo_url, branch, commit_name, commit_email FROM git_sync_repos WHERE id = $1`, repoID.Int64).Scan(
			&repoURL, &branch, &commitName, &commitEmail); err != nil {
			return fmt.Errorf("loading git repo config for id %d: %w", repoID.Int64, err)
		}
	}

	// 4. Ensure repo exists and is initialized
	var env []string
	if repoID.Valid {
		env = BuildPushEnvForRepoID(db, repoID.Int64)
		defer CleanupAskpass()

		if err := EnsureRepoRootDir(repoDir, repoURL.String, branch.String, env); err != nil {
			log.Printf("GITOPS COMMIT: failed to ensure repo root %s: %v", repoDir, err)
			return saveStateLocally(repoDir, yamlContent)
		}
	} else {
		// No repo configured, fallback to local save if .git is missing
		if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
			return saveStateLocally(repoDir, yamlContent)
		}
	}

	// 5. Save state.yaml
	if err := os.WriteFile(filepath.Join(repoDir, stateFileName), []byte(yamlContent), 0644); err != nil {
		return fmt.Errorf("writing state.yaml: %w", err)
	}

	// 6. Git Commit & Push
	return CommitAndPush(repoDir, env, "feat: infrastructure state update via D-PlaneOS",
		commitName.String, commitEmail.String, branch.String)
}

// GenerateStateYAML converts LiveState into the declarative state.yaml format.
// This implementation ensures DETERMINISTIC output by sorting all resources.
func GenerateStateYAML(state *LiveState) string {
	var sb strings.Builder

	sb.WriteString("version: 1\n\n")

	// 1. Pools
	if len(state.Pools) > 0 {
		sort.Slice(state.Pools, func(i, j int) bool {
			return state.Pools[i].Name < state.Pools[j].Name
		})
		sb.WriteString("pools:\n")
		for _, p := range state.Pools {
			sb.WriteString(fmt.Sprintf("  - name: %q\n", p.Name))
			if len(p.Disks) > 0 {
				sort.Strings(p.Disks)
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

	// 2. Datasets
	if len(state.Datasets) > 0 {
		sort.Slice(state.Datasets, func(i, j int) bool {
			return state.Datasets[i].Name < state.Datasets[j].Name
		})
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

	// 3. Shares
	if len(state.Shares) > 0 {
		sort.Slice(state.Shares, func(i, j int) bool {
			return state.Shares[i].Name < state.Shares[j].Name
		})
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

	// 4. NFS
	if len(state.NFS) > 0 {
		sort.Slice(state.NFS, func(i, j int) bool {
			return state.NFS[i].Path < state.NFS[j].Path
		})
		sb.WriteString("nfs:\n")
		for _, n := range state.NFS {
			sb.WriteString(fmt.Sprintf("  - path: %q\n", n.Path))
			sb.WriteString(fmt.Sprintf("    clients: %q\n", n.Clients))
			sb.WriteString(fmt.Sprintf("    options: %q\n", n.Options))
			if !n.Enabled {
				sb.WriteString("    enabled: false\n")
			}
		}
		sb.WriteString("\n")
	}

	// 5. Stacks
	if len(state.Stacks) > 0 {
		sort.Slice(state.Stacks, func(i, j int) bool {
			return state.Stacks[i].Name < state.Stacks[j].Name
		})
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

	// 6. Users
	if len(state.Users) > 0 {
		sort.Slice(state.Users, func(i, j int) bool {
			return state.Users[i].Username < state.Users[j].Username
		})
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

	// 7. Groups
	if len(state.Groups) > 0 {
		sort.Slice(state.Groups, func(i, j int) bool {
			return state.Groups[i].Name < state.Groups[j].Name
		})
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
				sort.Strings(g.Members)
				sb.WriteString("    members: [")
				for i, m := range g.Members {
					if i > 0 {
						sb.WriteString(", ")
					}
					sb.WriteString(fmt.Sprintf("%q", m))
				}
				sb.WriteString("]\n")
			}
		}
		sb.WriteString("\n")
	}

	// 8. Replication
	if len(state.Replication) > 0 {
		sort.Slice(state.Replication, func(i, j int) bool {
			return state.Replication[i].Name < state.Replication[j].Name
		})
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

	// 9. LDAP
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

	// 10. System
	if state.System != nil {
		sb.WriteString("system:\n")
		if state.System.Hostname != "" {
			sb.WriteString(fmt.Sprintf("  hostname: %s\n", state.System.Hostname))
		}
		if state.System.Timezone != "" {
			sb.WriteString(fmt.Sprintf("  timezone: %s\n", state.System.Timezone))
		}
		if len(state.System.DNSServers) > 0 {
			sb.WriteString("  dns_servers: [")
			for i, dns := range state.System.DNSServers {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(fmt.Sprintf("%q", dns))
			}
			sb.WriteString("]\n")
		}
		if len(state.System.NTPServers) > 0 {
			sb.WriteString("  ntp_servers: [")
			for i, ntp := range state.System.NTPServers {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(fmt.Sprintf("%q", ntp))
			}
			sb.WriteString("]\n")
		}

		sb.WriteString("  firewall:\n")
		sb.WriteString("    tcp: [")
		for i, p := range state.System.FirewallTCP {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(fmt.Sprintf("%d", p))
		}
		sb.WriteString("]\n")
		sb.WriteString("    udp: [")
		for i, p := range state.System.FirewallUDP {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(fmt.Sprintf("%d", p))
		}
		sb.WriteString("]\n")

		sb.WriteString("  networking:\n")
		if len(state.System.NetworkStatics) > 0 {
			sb.WriteString("    statics:\n")
			var keys []string
			for k := range state.System.NetworkStatics {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				st := state.System.NetworkStatics[k]
				sb.WriteString(fmt.Sprintf("      %s:\n", k))
				sb.WriteString(fmt.Sprintf("        cidr: %q\n", st.CIDR))
				if st.Gateway != "" {
					sb.WriteString(fmt.Sprintf("        gateway: %q\n", st.Gateway))
				}
			}
		}
		if len(state.System.NetworkBonds) > 0 {
			sb.WriteString("    bonds:\n")
			var keys []string
			for k := range state.System.NetworkBonds {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				bn := state.System.NetworkBonds[k]
				sb.WriteString(fmt.Sprintf("      %s:\n", k))
				sb.WriteString(fmt.Sprintf("        mode: %q\n", bn.Mode))
				sb.WriteString("        slaves: [")
				for i, sl := range bn.Slaves {
					if i > 0 {
						sb.WriteString(", ")
					}
					sb.WriteString(fmt.Sprintf("%q", sl))
				}
				sb.WriteString("]\n")
			}
		}
		if len(state.System.NetworkVLANs) > 0 {
			sb.WriteString("    vlans:\n")
			var keys []string
			for k := range state.System.NetworkVLANs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				vl := state.System.NetworkVLANs[k]
				sb.WriteString(fmt.Sprintf("      %s:\n", k))
				sb.WriteString(fmt.Sprintf("        parent: %q\n", vl.Parent))
				sb.WriteString(fmt.Sprintf("        vid: %d\n", vl.VID))
			}
		}

		sb.WriteString("  samba:\n")
		sb.WriteString(fmt.Sprintf("    workgroup: %q\n", state.System.SambaWorkgroup))
		sb.WriteString(fmt.Sprintf("    server_string: %q\n", state.System.SambaServerString))
		if state.System.SambaTimeMachine {
			sb.WriteString("    time_machine: true\n")
		}
		if state.System.SambaAllowGuest {
			sb.WriteString("    allow_guest: true\n")
		}
		if state.System.SambaExtraGlobal != "" {
			sb.WriteString("    extra_global: |\n")
			lines := strings.Split(strings.TrimSpace(state.System.SambaExtraGlobal), "\n")
			for _, line := range lines {
				sb.WriteString(fmt.Sprintf("      %s\n", line))
			}
		}
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

// buildPushEnv, cleanupAskpass and gitCommitAndPush have been moved to git_util.go
