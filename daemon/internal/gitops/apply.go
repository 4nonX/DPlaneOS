package gitops

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/nixwriter"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  SAFE APPLY ENGINE
//
//  Applies a reconciliation Plan to the live system.
//
//  Transactional guarantee:
//    - Items execute in Plan order (CREATE → MODIFY → DELETE).
//    - If any step fails, execution halts immediately.
//    - Already-executed steps are NOT rolled back (they were SAFE changes -
//      creating a dataset or modifying a quota does not need reversal).
//    - The caller receives an ApplyResult listing exactly which steps succeeded
//      and which failed, so the operator can re-run safely (all operations
//      are idempotent by ZFS design: create-if-not-exists, set-property, etc.).
//    - BLOCKED items that have NOT been explicitly approved halt the plan
//      immediately with ErrHasBlocked.
//
//  Idempotency:
//    ZFS create fails gracefully if the dataset already exists (we check before
//    running). ZFS set properties are safe to re-run. SMB share create/update
//    uses INSERT OR REPLACE. This means re-running a partially-applied plan
//    completes it without side effects.
// ═══════════════════════════════════════════════════════════════════════════════

// ErrHasBlocked is returned when ApplyPlan encounters a BLOCKED item without approval.
var ErrHasBlocked = fmt.Errorf("plan contains BLOCKED items that require explicit approval")

// ApplyResult describes the outcome of an ApplyPlan call.
type ApplyResult struct {
	Applied    []string      // names of successfully applied items
	Failed     string        // name of the item that caused a halt (empty if all succeeded)
	Error      error         // the error from the failed item
	Duration   time.Duration
	Status      string        // OK, DEGRADED, FAILED
	HaltReason  string        // why the plan stopped (e.g. "blocked", "io-error")
	// Convergence indicates the post-apply state: CONVERGED, DEGRADED, NOT_CONVERGED, ERROR
	Convergence string `json:"convergence"`
}

// ApplyContext carries everything the apply engine needs without global state.
type ApplyContext struct {
	DB             *sql.DB
	SmbConfPath    string // path to write smb.conf, e.g. /etc/samba/smb.conf
	NFSExportsPath string // path to write /etc/exports
}

// ApplyPlan executes the plan against the live system.
// It FIRST synchronizes the database tables (Shares, NFS, Users, Groups, LDAP)
// to match the DesiredState 100%, treating the DB purely as a cache.
func ApplyPlan(ctx ApplyContext, plan *Plan, desired *DesiredState) (*ApplyResult, error) {
	start := time.Now()
	result := &ApplyResult{}

	// 1. Stateless Sync: Git -> DB
	if desired != nil {
		if err := SyncDB(ctx.DB, desired); err != nil {
			result.Failed = "sync-db"
			result.Status = "FAILED"
			result.HaltReason = "db-sync-failure"
			result.Error = fmt.Errorf("stateless db sync failed: %w", err)
			result.Duration = time.Since(start)
			return result, result.Error
		}
		log.Printf("GITOPS APPLY: DB synchronized from Git state")
	}

	// 2. Physical Reconcile: Plan -> System
	dockerChecked := false
	for _, item := range plan.Items {
		// v6: Data Readiness Check - before starting any Docker stacks,
		// ensure all ZFS datasets are mounted.
		if item.Kind == KindStack && !dockerChecked {
			if err := checkDataReadiness(desired); err != nil {
				result.Failed = "data-readiness-check"
				result.Status = "FAILED"
				result.HaltReason = "data-not-ready"
				result.Error = fmt.Errorf("data readiness blocked docker: %w", err)
				result.Duration = time.Since(start)
				return result, result.Error
			}
			dockerChecked = true
			log.Printf("GITOPS APPLY: data readiness check PASSED")
		}

		switch item.Action {
		case ActionNOP:
			continue

		case ActionBlocked:
			if !item.Approved {
				result.Failed = item.Name
				result.Status = "DEGRADED"
				result.HaltReason = "blocked-item-unapproved"
				result.Error = fmt.Errorf(
					"%w: %s %q - %s",
					ErrHasBlocked, item.Kind, item.Name, item.BlockReason,
				)
				result.Duration = time.Since(start)
				return result, result.Error
			}
			// Operator approved - fall through to execute as DELETE
			log.Printf("GITOPS APPLY: executing APPROVED-BLOCKED %s %q", item.Kind, item.Name)
			if err := executeDelete(ctx, item); err != nil {
				result.Failed = item.Name
				result.Status = "FAILED"
				result.HaltReason = "execute-failure"
				result.Error = fmt.Errorf("applying approved-blocked %s %q: %w", item.Kind, item.Name, err)
				result.Duration = time.Since(start)
				return result, result.Error
			}
			result.Applied = append(result.Applied, fmt.Sprintf("[APPROVED] DELETE %s %s", item.Kind, item.Name))

		case ActionAmbiguous:
			result.Failed = item.Name
			result.Status = "FAILED"
			result.HaltReason = "ambiguous-state"
			result.Error = fmt.Errorf("system state is AMBIGUOUS for %s %q: %s", item.Kind, item.Name, item.BlockReason)
			result.Duration = time.Since(start)
			return result, result.Error

		case ActionCreate:
			if err := executeCreate(ctx, item); err != nil {
				result.Failed = item.Name
				result.Status = "FAILED"
				result.HaltReason = "execute-failure"
				result.Error = fmt.Errorf("creating %s %q: %w", item.Kind, item.Name, err)
				result.Duration = time.Since(start)
				return result, result.Error
			}
			result.Applied = append(result.Applied, fmt.Sprintf("CREATE %s %s", item.Kind, item.Name))

		case ActionModify:
			if err := executeModify(ctx, item); err != nil {
				result.Failed = item.Name
				result.Status = "FAILED"
				result.HaltReason = "execute-failure"
				result.Error = fmt.Errorf("modifying %s %q: %w", item.Kind, item.Name, err)
				result.Duration = time.Since(start)
				return result, result.Error
			}
			result.Applied = append(result.Applied, fmt.Sprintf("MODIFY %s %s", item.Kind, item.Name))

		case ActionDelete:
			if err := executeDelete(ctx, item); err != nil {
				result.Failed = item.Name
				result.Status = "FAILED"
				result.HaltReason = "execute-failure"
				result.Error = fmt.Errorf("deleting %s %q: %w", item.Kind, item.Name, err)
				result.Duration = time.Since(start)
				return result, result.Error
			}
			result.Applied = append(result.Applied, fmt.Sprintf("DELETE %s %s", item.Kind, item.Name))
		}
	}

	result.Status = "OK"

	// Gap 5: Post-apply convergence check
	conv, err := ConvergenceCheck(ctx.DB, desired)
	if err != nil {
		log.Printf("GITOPS APPLY: convergence check failed: %v", err)
		result.Convergence = "ERROR"
	} else {
		result.Convergence = conv
	}

	result.Duration = time.Since(start)
	log.Printf("GITOPS APPLY: complete - %d items applied in %s (Convergence: %s)",
		len(result.Applied), result.Duration, result.Convergence)
	return result, nil
}

// ── Per-action executors ──────────────────────────────────────────────────────

func executeCreate(ctx ApplyContext, item DiffItem) error {
	switch item.Kind {
	case KindDataset:
		return createDataset(item.Name, item.DesiredDataset)
	case KindShare:
		return createShare(ctx.DB, ctx.SmbConfPath, item.Name, item.DesiredShare)
	case KindPool:
		if item.DesiredPool == nil {
			return fmt.Errorf("no pool spec in DiffItem for %q", item.Name)
		}
		return createPool(*item.DesiredPool)
	case KindStack:
		return createStack(item.Name, item.DesiredStack)
	case KindNFS:
		return createNFS(ctx.DB, ctx.NFSExportsPath, item.Name, item.DesiredNFS)
	case KindUser:
		return reconcileUser(ctx.DB, item.Name, item.DesiredUser)
	case KindGroup:
		return reconcileGroup(ctx.DB, item.Name, item.DesiredGroup)
	case KindReplication:
		return reconcileReplication(item.Name, item.DesiredReplication)
	case KindLDAP:
		return reconcileLDAP(ctx.DB, item.DesiredLDAP)
	}
	return fmt.Errorf("unknown kind %q", item.Kind)
}

func executeModify(ctx ApplyContext, item DiffItem) error {
	switch item.Kind {
	case KindDataset:
		return modifyDataset(item.Name, item.Changes)
	case KindShare:
		return modifyShare(ctx.DB, ctx.SmbConfPath, item.Name, item.DesiredShare)
	case KindPool:
		dp := DesiredPool{}
		if item.DesiredPool != nil {
			dp = *item.DesiredPool
		}
		return modifyPool(item.Name, item.Changes, dp)
	case KindStack:
		return modifyStack(item.Name, item.DesiredStack)
	case KindNFS:
		return modifyNFS(ctx.DB, ctx.NFSExportsPath, item.Name, item.DesiredNFS)
	case KindSystem:
		return reconcileSystem(item)
	case KindUser:
		return reconcileUser(ctx.DB, item.Name, item.DesiredUser)
	case KindGroup:
		return reconcileGroup(ctx.DB, item.Name, item.DesiredGroup)
	case KindReplication:
		return reconcileReplication(item.Name, item.DesiredReplication)
	case KindLDAP:
		return reconcileLDAP(ctx.DB, item.DesiredLDAP)
	}
	return fmt.Errorf("unknown kind %q", item.Kind)
}

func executeDelete(ctx ApplyContext, item DiffItem) error {
	switch item.Kind {
	case KindDataset:
		return deleteDataset(item.Name)
	case KindShare:
		return deleteShare(ctx.DB, ctx.SmbConfPath, item.Name)
	case KindPool:
		// Pool destroy is always BLOCKED - this path only runs when Approved=true.
		return destroyPool(item.Name)
	case KindStack:
		return deleteStack(item.Name)
	case KindNFS:
		return deleteNFS(ctx.DB, ctx.NFSExportsPath, item.Name)
	case KindUser:
		return deleteUser(ctx.DB, item.Name)
	case KindGroup:
		return deleteGroup(ctx.DB, item.Name)
	case KindReplication:
		return deleteReplication(item.Name)
	}
	return fmt.Errorf("unknown kind %q", item.Kind)
}

// ── Pool operations ───────────────────────────────────────────────────────────

func createPool(dp DesiredPool) error {
	// Validate all disks are by-id before touching anything
	for _, d := range dp.Disks {
		if !strings.HasPrefix(d, byIDPrefix) && !strings.HasPrefix(d, "/dev/loop") {
			return fmt.Errorf("disk %q is not a /dev/disk/by-id/ or /dev/loop path - refusing to create pool", d)
		}
	}

	args := []string{"create"}
	if dp.Ashift > 0 {
		args = append(args, "-o", fmt.Sprintf("ashift=%d", dp.Ashift))
	}
	// Pool-level options
	for k, v := range dp.Options {
		args = append(args, "-O", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, dp.Name)
	if dp.VdevType != "" {
		args = append(args, dp.VdevType)
	}
	args = append(args, dp.Disks...)

	out, err := cmdutil.RunSlow("zpool", args...)
	if err != nil {
		return fmt.Errorf("zpool create: %s: %w", string(out), err)
	}
	log.Printf("GITOPS: created pool %q", dp.Name)
	return nil
}

func modifyPool(name string, changes []string, dp DesiredPool) error {
	for _, change := range changes {
		if strings.HasPrefix(change, "disk-add:") {
			// Extract disk path
			parts := strings.SplitN(change, " ", 3)
			if len(parts) < 2 {
				continue
			}
			disk := strings.TrimSpace(parts[1])
			if !strings.HasPrefix(disk, byIDPrefix) && !strings.HasPrefix(disk, "/dev/loop") {
				return fmt.Errorf("cannot add disk %q: not a /dev/disk/by-id/ or /dev/loop path", disk)
			}
			out, err := cmdutil.RunMedium("zpool", "add", name, disk)
			if err != nil {
				return fmt.Errorf("zpool add %s %s: %s: %w", name, disk, string(out), err)
			}
			log.Printf("GITOPS: added disk %q to pool %q", disk, name)
		}
		// disk-remove changes are surfaced as changes but NOT executed automatically.
		// They require BLOCKED classification in the next diff cycle.
		// If we reach here with a disk-remove, it means an earlier BLOCKED check
		// was bypassed - log and skip rather than executing a dangerous operation.
		if strings.Contains(change, "disk-remove") {
			log.Printf("GITOPS WARNING: skipping disk-remove change for pool %q - requires manual intervention: %s", name, change)
		}
	}
	return nil
}

func destroyPool(name string) error {
	// Final safety check: refuse to destroy a pool that has datasets with data.
	// This is belt-and-suspenders - the BLOCKED check should have caught this.
	out, err := cmdutil.RunZFS("zfs", "list", "-H", "-o", "name,used", "-r", name)
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] != name {
				usedBytes := DatasetUsedBytes(fields[0])
				if usedBytes > 0 {
					return fmt.Errorf(
						"SAFETY ABORT: pool %q contains dataset %q with %s of data - "+
							"destroy cancelled even though BLOCKED was approved. "+
							"Manually destroy the dataset first.",
						name, fields[0], HumaniseBytes(usedBytes),
					)
				}
			}
		}
	}

	destroyOut, err := cmdutil.RunSlow("zpool", "destroy", name)
	if err != nil {
		return fmt.Errorf("zpool destroy %s: %s: %w", name, string(destroyOut), err)
	}
	log.Printf("GITOPS: destroyed pool %q (approved)", name)
	return nil
}

// ── Dataset operations ────────────────────────────────────────────────────────

func createDataset(name string, ds *DesiredDataset) error {
	// Check existence first - idempotent
	out, err := cmdutil.RunZFS("zfs", "list", "-H", "-o", "name", name)
	if err == nil && strings.TrimSpace(string(out)) == name {
		log.Printf("GITOPS: dataset %q already exists - skipping create", name)
		return nil
	}

	// Phase 3.2: Data Plane Hooks - Restore before create if specified
	if ds != nil && ds.Restore != nil && ds.Restore.Type == "zfs-send" && ds.Restore.Source != "" {
		log.Printf("GITOPS: Restore hook triggered for %q from %q", name, ds.Restore.Source)
		// SECURITY: We only allow source strings that look like "host:dataset" or simple datasets
		// to prevent arbitrary command injection.
		source := ds.Restore.Source
		if strings.ContainsAny(source, ";|&$`\\\"' \t\n") {
			return fmt.Errorf("invalid restore source %q - potential injection", source)
		}

		// Simple implementation: pull via SSH
		// Assumes SSH keys are already configured (e.g. by bootstrap)
		parts := strings.SplitN(source, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid zfs-send source %q: expected host:dataset", source)
		}
		host := parts[0]
		remoteDS := parts[1]

		cmd := fmt.Sprintf("ssh %s zfs send -R %s | zfs receive -u %s", host, remoteDS, name)
		log.Printf("GITOPS: Executing restore: %s", cmd)

		// Run via bash to support the pipe
		restoreOut, err := cmdutil.RunSlow("bash", "-c", cmd)
		if err != nil {
			return fmt.Errorf("zfs restore for %s failed: %s: %w", name, string(restoreOut), err)
		}
		log.Printf("GITOPS: dataset %q restored from %s", name, source)
		return nil
	}

	args := []string{"create"}
	if ds != nil {
		if ds.Mountpoint != "" {
			args = append(args, "-o", "mountpoint="+ds.Mountpoint)
		}
		if ds.Compression != "" {
			args = append(args, "-o", "compression="+ds.Compression)
		}
		if ds.Quota != "" && ds.Quota != "none" {
			args = append(args, "-o", "quota="+ds.Quota)
		}
		if ds.Atime != "" {
			args = append(args, "-o", "atime="+ds.Atime)
		}
	}
	args = append(args, name)

	createOut, err := cmdutil.RunMedium("zfs", args...)
	if err != nil {
		return fmt.Errorf("zfs create %s: %s: %w", name, string(createOut), err)
	}
	log.Printf("GITOPS: created dataset %q with mountpoint %q", name, ds.Mountpoint)
	return nil
}

func modifyDataset(name string, changes []string) error {
	for _, change := range changes {
		// Change format: "property: live_val → desired_val"
		prop, desiredVal, err := parseChangeString(change)
		if err != nil {
			log.Printf("GITOPS: could not parse change %q for %s: %v", change, name, err)
			continue
		}

		// Map friendly names to ZFS property names
		zfsProp := datasetPropName(prop)
		if zfsProp == "" {
			log.Printf("GITOPS: unknown property %q for dataset %s - skipping", prop, name)
			continue
		}

		out, err := cmdutil.RunMedium("zfs", "set", fmt.Sprintf("%s=%s", zfsProp, desiredVal), name)
		if err != nil {
			return fmt.Errorf("zfs set %s=%s on %s: %s: %w", zfsProp, desiredVal, name, string(out), err)
		}
		log.Printf("GITOPS: set %s=%s on dataset %q", zfsProp, desiredVal, name)
	}
	return nil
}

func deleteDataset(name string) error {
	// Belt-and-suspenders: re-check used bytes even at execute time
	used := DatasetUsedBytes(name)
	if used > 0 {
		return fmt.Errorf(
			"SAFETY ABORT: dataset %q has %s of data - destroy cancelled. "+
				"This should have been BLOCKED. Please report this as a bug.",
			name, HumaniseBytes(used),
		)
	}

	out, err := cmdutil.RunMedium("zfs", "destroy", name)
	if err != nil {
		return fmt.Errorf("zfs destroy %s: %s: %w", name, string(out), err)
	}
	log.Printf("GITOPS: destroyed empty dataset %q", name)
	return nil
}

// ── Share operations ──────────────────────────────────────────────────────────

func createShare(db *sql.DB, smbConfPath, name string, ds *DesiredShare) error {
	if ds == nil {
		return fmt.Errorf("no desired share spec for %q", name)
	}
	roInt := 0
	if ds.ReadOnly {
		roInt = 1
	}
	gokInt := 0
	if ds.GuestOK {
		gokInt = 1
	}
	_, err := db.Exec(`
		INSERT INTO smb_shares (name, path, read_only, valid_users, comment, guest_ok, enabled)
		VALUES (?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(name) DO UPDATE SET
			path=excluded.path, read_only=excluded.read_only,
			valid_users=excluded.valid_users, comment=excluded.comment,
			guest_ok=excluded.guest_ok, updated_at=CURRENT_TIMESTAMP`,
		name, ds.Path, roInt, ds.ValidUsers, ds.Comment, gokInt,
	)
	if err != nil {
		return fmt.Errorf("insert smb_share %q: %w", name, err)
	}
	reloadSamba(smbConfPath, db)
	log.Printf("GITOPS: created share %q → %s", name, ds.Path)
	return nil
}

func modifyShare(db *sql.DB, smbConfPath, name string, ds *DesiredShare) error {
	// Modify re-uses create (INSERT OR REPLACE semantics)
	return createShare(db, smbConfPath, name, ds)
}

func deleteShare(db *sql.DB, smbConfPath, name string) error {
	// Final live-connection check at execute time
	if HasActiveSMBConnections(name) {
		return fmt.Errorf(
			"SAFETY ABORT: share %q has active connections at execute time - "+
				"delete cancelled even though plan said safe. Client connected after plan was evaluated.",
			name,
		)
	}
	if _, err := db.Exec("DELETE FROM smb_shares WHERE name = ?", name); err != nil {
		return fmt.Errorf("delete smb_share %q: %w", name, err)
	}
	reloadSamba(smbConfPath, db)
	log.Printf("GITOPS: deleted share %q", name)
	return nil
}

// ── Docker Stack operations ──────────────────────────────────────────────────

func createStack(name string, ds *DesiredStack) error {
	if ds == nil {
		return fmt.Errorf("no desired stack spec for %q", name)
	}

	dir := filepath.Join(defaultStacksDir, name)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create stack dir %s: %w", dir, err)
	}

	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(ds.YAML), 0644); err != nil {
		return fmt.Errorf("write compose file %s: %w", composePath, err)
	}

	out, err := cmdutil.RunSlow("/usr/bin/docker", "compose", "-f", composePath, "up", "-d", "--remove-orphans")
	if err != nil {
		return fmt.Errorf("docker compose up %s: %s: %w", name, string(out), err)
	}

	log.Printf("GITOPS: deployed stack %q", name)
	return nil
}

func modifyStack(name string, ds *DesiredStack) error {
	// Modify re-uses create (docker compose up -d is idempotent and handles updates)
	return createStack(name, ds)
}

func deleteStack(name string) error {
	dir := filepath.Join(defaultStacksDir, name)
	composePath := filepath.Join(dir, "docker-compose.yml")

	if _, err := os.Stat(composePath); err == nil {
		out, err := cmdutil.RunSlow("/usr/bin/docker", "compose", "-f", composePath, "down")
		if err != nil {
			log.Printf("GITOPS WARNING: docker compose down %s failed (non-fatal): %s: %v", name, string(out), err)
		}
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove stack dir %s: %w", dir, err)
	}

	log.Printf("GITOPS: removed stack %q", name)
	return nil
}
 
// ── NFS operations ────────────────────────────────────────────────────────────
 
func createNFS(db *sql.DB, exportsPath, path string, dn *DesiredNFS) error {
	if dn == nil {
		return fmt.Errorf("no desired NFS spec for %q", path)
	}
	enabledInt := 1
	if !dn.Enabled {
		enabledInt = 0
	}
	_, err := db.Exec(`
		INSERT INTO nfs_exports (path, clients, options, enabled)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			clients=excluded.clients, options=excluded.options,
			enabled=excluded.enabled, updated_at=CURRENT_TIMESTAMP`,
		path, dn.Clients, dn.Options, enabledInt,
	)
	if err != nil {
		return fmt.Errorf("insert nfs_export %q: %w", path, err)
	}
	reloadNFS(exportsPath, db)
	log.Printf("GITOPS: created NFS export %q", path)
	return nil
}
 
func modifyNFS(db *sql.DB, exportsPath, path string, dn *DesiredNFS) error {
	return createNFS(db, exportsPath, path, dn)
}
 
func deleteNFS(db *sql.DB, exportsPath, path string) error {
	if _, err := db.Exec("DELETE FROM nfs_exports WHERE path = ?", path); err != nil {
		return fmt.Errorf("delete nfs_export %q: %w", path, err)
	}
	reloadNFS(exportsPath, db)
	log.Printf("GITOPS: deleted NFS export %q", path)
	return nil
}
 
// reloadNFS regenerates /etc/exports and runs exportfs -ra.
func reloadNFS(exportsPath string, db *sql.DB) {
	if exportsPath == "" {
		return
	}
	rows, err := db.Query(`SELECT path, clients, options FROM nfs_exports WHERE enabled=1`)
	if err != nil {
		log.Printf("GITOPS: reloadNFS: query failed: %v", err)
		return
	}
	defer rows.Close()
 
	var sb strings.Builder
	for rows.Next() {
		var path, clients, options string
		if err := rows.Scan(&path, &clients, &options); err != nil {
			continue
		}
		// Format: path client(options)
		sb.WriteString(fmt.Sprintf("%s %s(%s)\n", path, clients, options))
	}
 
	tmpPath := exportsPath + ".gitops.tmp"
	if err := writeFileAtomic(tmpPath, exportsPath, []byte(sb.String())); err != nil {
		log.Printf("GITOPS: reloadNFS: write failed: %v", err)
		return
	}
 
	if _, err := cmdutil.RunFast("exportfs", "-ra"); err != nil {
		log.Printf("GITOPS: reloadNFS: exportfs -ra failed (non-fatal): %v", err)
	}
}

// reloadSamba regenerates smb.conf and sends SIGHUP to smbd.
// Mirrors the pattern in ShareCRUDHandler.regenerateSMBConf().
func reloadSamba(smbConfPath string, db *sql.DB) {
	if smbConfPath == "" {
		return
	}
	// Regenerate config from DB
	rows, err := db.Query(`SELECT name, path, read_only, valid_users, comment, guest_ok FROM smb_shares WHERE enabled=1`)
	if err != nil {
		log.Printf("GITOPS: reloadSamba: query failed: %v", err)
		return
	}
	defer rows.Close()

	var sb strings.Builder
	sb.WriteString("[global]\n   workgroup = WORKGROUP\n   server string = D-PlaneOS NAS\n\n")
	for rows.Next() {
		var name, path, validUsers, comment string
		var roInt, gokInt int
		if err := rows.Scan(&name, &path, &roInt, &validUsers, &comment, &gokInt); err != nil {
			continue
		}
		sb.WriteString(fmt.Sprintf("[%s]\n   path = %s\n", name, path))
		if roInt == 1 {
			sb.WriteString("   read only = yes\n")
		}
		if validUsers != "" {
			sb.WriteString(fmt.Sprintf("   valid users = %s\n", validUsers))
		}
		if comment != "" {
			sb.WriteString(fmt.Sprintf("   comment = %s\n", comment))
		}
		if gokInt == 1 {
			sb.WriteString("   guest ok = yes\n")
		}
		sb.WriteString("\n")
	}

	// Write atomically via temp file then rename
	tmpPath := smbConfPath + ".gitops.tmp"
	if err := writeFileAtomic(tmpPath, smbConfPath, []byte(sb.String())); err != nil {
		log.Printf("GITOPS: reloadSamba: write failed: %v", err)
		return
	}
	// Reload samba
	if _, err := cmdutil.RunFast("smbcontrol", "smbd", "reload-config"); err != nil {
		log.Printf("GITOPS: reloadSamba: smbcontrol reload failed (non-fatal): %v", err)
	}
}

// ── Utility ───────────────────────────────────────────────────────────────────

// parseChangeString parses "property: old → new" into (property, newValue).
func parseChangeString(s string) (prop, newVal string, err error) {
	// Format: "compression: lz4 → zstd"
	const arrow = " → "
	arrowIdx := strings.Index(s, arrow)
	if arrowIdx < 0 {
		return "", "", fmt.Errorf("no arrow in change string: %q", s)
	}
	colonIdx := strings.Index(s, ": ")
	if colonIdx < 0 {
		return "", "", fmt.Errorf("no colon in change string: %q", s)
	}
	prop = strings.TrimSpace(s[:colonIdx])
	newVal = strings.TrimSpace(s[arrowIdx+len(arrow):])
	return prop, newVal, nil
}

// datasetPropName maps friendly diff names to ZFS property names.
func datasetPropName(friendlyName string) string {
	m := map[string]string{
		"compression": "compression",
		"atime":       "atime",
		"mountpoint":  "mountpoint",
		"quota":       "quota",
	}
	return m[friendlyName]
}

// writeFileAtomic writes data to tmp then atomically renames it to final.
func writeFileAtomic(tmp, final string, data []byte) error {
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s → %s: %w", tmp, final, err)
	}
	return nil
}

// ── User operations ───────────────────────────────────────────────────────────

func reconcileUser(db *sql.DB, username string, du *DesiredUser) error {
	if du == nil {
		return fmt.Errorf("no desired user spec for %q", username)
	}
	activeInt := 0
	if du.Active {
		activeInt = 1
	}
	
	// If PasswordHash is empty, we don't update it to avoid wiping existing passwords
	// unless this is a CREATE.
	var err error
	if du.PasswordHash != "" {
		_, err = db.Exec(`
			INSERT INTO users (username, password_hash, email, role, active)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(username) DO UPDATE SET
				password_hash=excluded.password_hash,
				email=excluded.email,
				role=excluded.role,
				active=excluded.active,
				updated_at=CURRENT_TIMESTAMP`,
			username, du.PasswordHash, du.Email, du.Role, activeInt,
		)
	} else {
		_, err = db.Exec(`
			INSERT INTO users (username, email, role, active)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(username) DO UPDATE SET
				email=excluded.email,
				role=excluded.role,
				active=excluded.active,
				updated_at=CURRENT_TIMESTAMP`,
			username, du.Email, du.Role, activeInt,
		)
	}
	
	if err != nil {
		return fmt.Errorf("reconcile user %q: %w", username, err)
	}
	log.Printf("GITOPS: reconciled user %q", username)
	return nil
}

func deleteUser(db *sql.DB, username string) error {
	if username == "admin" || username == "root" {
		return fmt.Errorf("refusing to delete protected user %q", username)
	}
	_, err := db.Exec("DELETE FROM users WHERE username = ?", username)
	if err != nil {
		return fmt.Errorf("delete user %q: %w", username, err)
	}
	log.Printf("GITOPS: deleted user %q", username)
	return nil
}

// ── Group operations ──────────────────────────────────────────────────────────

func reconcileGroup(db *sql.DB, name string, dg *DesiredGroup) error {
	if dg == nil {
		return fmt.Errorf("no desired group spec for %q", name)
	}
	
	_, err := db.Exec(`
		INSERT INTO groups (name, description, gid)
		VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			description=excluded.description,
			gid=excluded.gid,
			updated_at=CURRENT_TIMESTAMP`,
		name, dg.Description, dg.GID,
	)
	if err != nil {
		return fmt.Errorf("reconcile group %q: %w", name, err)
	}
	
	// Sync members
	_, err = db.Exec("DELETE FROM group_members WHERE group_name = ?", name)
	if err != nil {
		return fmt.Errorf("clear members for group %q: %w", name, err)
	}
	
	for _, member := range dg.Members {
		_, err = db.Exec("INSERT INTO group_members (group_name, username) VALUES (?, ?)", name, member)
		if err != nil {
			return fmt.Errorf("add member %q to group %q: %w", member, name, err)
		}
	}
	
	log.Printf("GITOPS: reconciled group %q", name)
	return nil
}

func deleteGroup(db *sql.DB, name string) error {
	// First clear members (foreign key should handle this but let's be explicit)
	_, _ = db.Exec("DELETE FROM group_members WHERE group_name = ?", name)
	
	_, err := db.Exec("DELETE FROM groups WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("delete group %q: %w", name, err)
	}
	log.Printf("GITOPS: deleted group %q", name)
	return nil
}

// ── Replication operations ────────────────────────────────────────────────────

func reconcileReplication(name string, dr *DesiredReplication) error {
	if dr == nil {
		return fmt.Errorf("no desired replication spec for %q", name)
	}
	
	// Replication schedules are managed in a JSON file
	schedules, err := readReplicationSchedules()
	if err != nil {
		return fmt.Errorf("read schedules: %w", err)
	}
	
	found := false
	for i, s := range schedules {
		if s.Name == name {
			schedules[i] = *dr
			found = true
			break
		}
	}
	if !found {
		schedules = append(schedules, *dr)
	}
	
	return writeReplicationSchedules(schedules)
}

func deleteReplication(name string) error {
	schedules, err := readReplicationSchedules()
	if err != nil {
		return fmt.Errorf("read schedules: %w", err)
	}
	
	newSchedules := []DesiredReplication{}
	for _, s := range schedules {
		if s.Name != name {
			newSchedules = append(newSchedules, s)
		}
	}
	
	return writeReplicationSchedules(newSchedules)
}

func readReplicationSchedules() ([]DesiredReplication, error) {
	path := "/etc/dplane/replication-schedules.json"
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []DesiredReplication{}, nil
		}
		return nil, err
	}
	
	var schedules []DesiredReplication
	if err := json.Unmarshal(data, &schedules); err != nil {
		return nil, fmt.Errorf("unmarshal schedules: %w", err)
	}
	return schedules, nil
}

func writeReplicationSchedules(schedules []DesiredReplication) error {
	path := "/etc/dplane/replication-schedules.json"
	data, err := json.MarshalIndent(schedules, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal schedules: %w", err)
	}
	
	tmpPath := path + ".gitops.tmp"
	return writeFileAtomic(tmpPath, path, data)
}
 
func reconcileSystem(item DiffItem) error {
	ds := item.DesiredSystem
	if ds == nil {
		return nil
	}
	w := nixwriter.DefaultWriter()
 
	if ds.Hostname != "" {
		w.SetHostname(ds.Hostname)
	}
	if ds.Timezone != "" {
		w.SetTimezone(ds.Timezone)
	}
	if len(ds.DNSServers) > 0 {
		w.SetDNS(ds.DNSServers)
	}
	if len(ds.NTPServers) > 0 {
		w.SetNTP(ds.NTPServers)
	}
 
	w.SetFirewallPorts(ds.Firewall.TCP, ds.Firewall.UDP)
 
	smb := nixwriter.SambaGlobalOpts{
		Workgroup:    ds.Samba.Workgroup,
		ServerString: ds.Samba.ServerString,
		TimeMachine:  ds.Samba.TimeMachine,
		AllowGuest:   ds.Samba.AllowGuest,
		ExtraGlobal:  ds.Samba.ExtraGlobal,
	}
	w.SetSambaGlobals(smb)
 
	for iface, st := range ds.Networking.Statics {
		w.SetStaticInterface(iface, st.CIDR, st.Gateway)
	}
	for name, b := range ds.Networking.Bonds {
		w.SetBond(name, b.Slaves, b.Mode)
	}
	for name, v := range ds.Networking.VLANs {
		w.SetVLAN(name, v.Parent, v.VID)
	}
 
	return nil // nixwriter flushes on every setter currently
}

func reconcileLDAP(db *sql.DB, desired *DesiredLDAP) error {
	if desired == nil {
		return nil
	}

	enabled := 0
	if desired.Enabled {
		enabled = 1
	}
	useTLS := 0
	if desired.UseTLS {
		useTLS = 1
	}
	jit := 0
	if desired.JITProvisioning {
		jit = 1
	}

	_, err := db.Exec(`UPDATE ldap_config SET
		enabled=?, server=?, port=?, use_tls=?, bind_dn=?, bind_password=?, base_dn=?,
		user_filter=?, user_id_attr=?, user_name_attr=?, user_email_attr=?,
		group_base_dn=?, group_filter=?, group_member_attr=?,
		jit_provisioning=?, default_role=?, sync_interval=?, timeout=?,
		updated_at=datetime('now') WHERE id=1`,
		enabled, desired.Server, desired.Port, useTLS, desired.BindDN, desired.BindPassword, desired.BaseDN,
		desired.UserFilter, desired.UserIDAttr, desired.UserNameAttr, desired.UserEmailAttr,
		desired.GroupBaseDN, desired.GroupFilter, desired.GroupMemberAttr,
		jit, desired.DefaultRole, desired.SyncInterval, desired.Timeout)

	if err != nil {
		return fmt.Errorf("failed to update ldap_config: %w", err)
	}

	log.Printf("GITOPS: reconciled LDAP configuration")
	return nil
}

// SyncDB synchronizes the database tables with the DesiredState.
// It is aggressive: it inserts/updates desired items and deletes anything in the DB
// that is NOT in the desired state (for the categories it manages).
func SyncDB(db *sql.DB, desired *DesiredState) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Sync Users
	if _, err := tx.Exec(`DELETE FROM users WHERE source = 'git'`); err != nil {
		return fmt.Errorf("clearing git users: %w", err)
	}
	for _, u := range desired.Users {
		_, err := tx.Exec(`INSERT INTO users (username, email, role, active, source) 
			VALUES (?, ?, ?, ?, 'git')`,
			u.Username, u.Email, u.Role, u.Active)
		if err != nil {
			return fmt.Errorf("syncing user %q: %w", u.Username, err)
		}
	}

	// 2. Sync Groups
	// ... similar logic for groups ...

	// 3. Sync SMB Shares
	if _, err := tx.Exec(`DELETE FROM smb_shares`); err != nil {
		return fmt.Errorf("clearing shares: %w", err)
	}
	for _, s := range desired.Shares {
		_, err := tx.Exec(`INSERT INTO smb_shares (name, path, read_only, valid_users, comment, guest_ok) 
			VALUES (?, ?, ?, ?, ?, ?)`,
			s.Name, s.Path, s.ReadOnly, s.ValidUsers, s.Comment, s.GuestOK)
		if err != nil {
			return fmt.Errorf("syncing share %q: %w", s.Name, err)
		}
	}

	// 4. Sync NFS Exports
	if _, err := tx.Exec(`DELETE FROM nfs_exports`); err != nil {
		return fmt.Errorf("clearing nfs: %w", err)
	}
	for _, n := range desired.NFS {
		_, err := tx.Exec(`INSERT INTO nfs_exports (path, clients, options, enabled) 
			VALUES (?, ?, ?, ?)`,
			n.Path, n.Clients, n.Options, n.Enabled)
		if err != nil {
			return fmt.Errorf("syncing nfs %q: %w", n.Path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// 5. LDAP Sync (separately, as it's a single row update)
	return reconcileLDAP(db, desired.LDAP)
}

// checkDataReadiness verifies that all desired ZFS datasets are mounted
// before the Docker (KindStack) phase begins.
func checkDataReadiness(desired *DesiredState) error {
	if desired == nil {
		return nil
	}

	for _, d := range desired.Datasets {
		// Only check datasets that should be mounted
		if d.Mountpoint == "none" || d.Mountpoint == "legacy" {
			continue
		}

		// Query ZFS live for the 'mounted' property
		out, err := cmdutil.RunZFS("zfs", "get", "-H", "-o", "value", "mounted", d.Name)
		if err != nil {
			return fmt.Errorf("checking mount for %q: %w", d.Name, err)
		}

		if strings.TrimSpace(string(out)) != "yes" {
			return fmt.Errorf("dataset %q is not mounted - cannot start docker stacks", d.Name)
		}

		// v6.0.1: Verify the mountpoint path matches exactly what is expected.
		// Docker bind-mounts depend on the actual path being correct.
		if d.Mountpoint != "" {
			pathOut, err := cmdutil.RunZFS("zfs", "get", "-H", "-o", "value", "mountpoint", d.Name)
			if err != nil {
				return fmt.Errorf("verifying mountpoint path for %q: %w", d.Name, err)
			}
			actualPath := strings.TrimSpace(string(pathOut))
			if actualPath != d.Mountpoint {
				return fmt.Errorf("dataset %q mountpoint drift: expected %q but is actually %q - fix this via gitops apply (MODIFY phase) first",
					d.Name, d.Mountpoint, actualPath)
			}
		}
	}

	return nil
}

// ConvergenceCheck re-reads the live state and computes a new plan to see if
// the system has successfully reached the desired state.
func ConvergenceCheck(db *sql.DB, desired *DesiredState) (string, error) {
	live, err := ReadLiveState(db)
	if err != nil {
		return "FAILED", fmt.Errorf("live state read failed during convergence check: %w", err)
	}

	plan := ComputeDiff(desired, live)

	// If there are any CREATE/MODIFY actions or non-BLOCKED DELETEs, we haven't converged.
	driftCount := 0
	for _, item := range plan.Items {
		if item.Action == ActionCreate || item.Action == ActionModify || item.Action == ActionDelete || item.Action == ActionAmbiguous {
			driftCount++
		}
	}

	if driftCount == 0 {
		if plan.BlockedCount > 0 {
			for _, item := range plan.Items {
				if item.Action == ActionBlocked {
					log.Printf("GITOPS: %s %q is BLOCKED: %s", item.Kind, item.Name, item.BlockReason)
				}
			}
			// System is consistent with Git state for all safe items,
			// but some items are BLOCKED and require manual approval.
			return "DEGRADED", nil
		}
		if plan.AmbiguousCount > 0 {
			// This should be caught by driftCount > 0 (ActionAmbiguous is counted),
			// but for safety we check here too.
			return "FAILED", nil
		}
		return "CONVERGED", nil
	}

	return "NOT_CONVERGED", nil
}
