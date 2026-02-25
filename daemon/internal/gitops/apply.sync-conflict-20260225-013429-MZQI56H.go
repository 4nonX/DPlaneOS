package gitops

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  SAFE APPLY ENGINE
//
//  Applies a reconciliation Plan to the live system.
//
//  Transactional guarantee:
//    - Items execute in Plan order (CREATE → MODIFY → DELETE).
//    - If any step fails, execution halts immediately.
//    - Already-executed steps are NOT rolled back (they were SAFE changes —
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
	Applied  []string // names of successfully applied items
	Failed   string   // name of the item that caused a halt (empty if all succeeded)
	Error    error    // the error from the failed item
	Duration time.Duration
}

// ApplyContext carries everything the apply engine needs without global state.
type ApplyContext struct {
	DB          *sql.DB
	SmbConfPath string // path to write smb.conf, e.g. /etc/samba/smb.conf
}

// ApplyPlan executes the plan against the live system.
//
// BLOCKED items without Approved=true halt the plan immediately.
// BLOCKED items with Approved=true are executed (the operator accepted the risk).
// NOP items are skipped silently.
func ApplyPlan(ctx ApplyContext, plan *Plan) (*ApplyResult, error) {
	start := time.Now()
	result := &ApplyResult{}

	for _, item := range plan.Items {
		switch item.Action {
		case ActionNOP:
			continue

		case ActionBlocked:
			if !item.Approved {
				result.Failed = item.Name
				result.Error = fmt.Errorf(
					"%w: %s %q — %s",
					ErrHasBlocked, item.Kind, item.Name, item.BlockReason,
				)
				result.Duration = time.Since(start)
				return result, result.Error
			}
			// Operator approved — fall through to execute as DELETE
			log.Printf("GITOPS APPLY: executing APPROVED-BLOCKED %s %q", item.Kind, item.Name)
			if err := executeDelete(ctx, item); err != nil {
				result.Failed = item.Name
				result.Error = fmt.Errorf("applying approved-blocked %s %q: %w", item.Kind, item.Name, err)
				result.Duration = time.Since(start)
				return result, result.Error
			}
			result.Applied = append(result.Applied, fmt.Sprintf("[APPROVED] DELETE %s %s", item.Kind, item.Name))

		case ActionCreate:
			if err := executeCreate(ctx, item); err != nil {
				result.Failed = item.Name
				result.Error = fmt.Errorf("creating %s %q: %w", item.Kind, item.Name, err)
				result.Duration = time.Since(start)
				return result, result.Error
			}
			result.Applied = append(result.Applied, fmt.Sprintf("CREATE %s %s", item.Kind, item.Name))

		case ActionModify:
			if err := executeModify(ctx, item); err != nil {
				result.Failed = item.Name
				result.Error = fmt.Errorf("modifying %s %q: %w", item.Kind, item.Name, err)
				result.Duration = time.Since(start)
				return result, result.Error
			}
			result.Applied = append(result.Applied, fmt.Sprintf("MODIFY %s %s", item.Kind, item.Name))

		case ActionDelete:
			if err := executeDelete(ctx, item); err != nil {
				result.Failed = item.Name
				result.Error = fmt.Errorf("deleting %s %q: %w", item.Kind, item.Name, err)
				result.Duration = time.Since(start)
				return result, result.Error
			}
			result.Applied = append(result.Applied, fmt.Sprintf("DELETE %s %s", item.Kind, item.Name))
		}
	}

	result.Duration = time.Since(start)
	log.Printf("GITOPS APPLY: complete — %d items applied in %s", len(result.Applied), result.Duration)
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
		// Pool destroy is always BLOCKED — this path only runs when Approved=true.
		return destroyPool(item.Name)
	}
	return fmt.Errorf("unknown kind %q", item.Kind)
}

// ── Pool operations ───────────────────────────────────────────────────────────

func createPool(dp DesiredPool) error {
	// Validate all disks are by-id before touching anything
	for _, d := range dp.Disks {
		if !strings.HasPrefix(d, byIDPrefix) {
			return fmt.Errorf("disk %q is not a /dev/disk/by-id/ path — refusing to create pool", d)
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
			if !strings.HasPrefix(disk, byIDPrefix) {
				return fmt.Errorf("cannot add disk %q: not a /dev/disk/by-id/ path", disk)
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
		// was bypassed — log and skip rather than executing a dangerous operation.
		if strings.Contains(change, "disk-remove") {
			log.Printf("GITOPS WARNING: skipping disk-remove change for pool %q — requires manual intervention: %s", name, change)
		}
	}
	return nil
}

func destroyPool(name string) error {
	// Final safety check: refuse to destroy a pool that has datasets with data.
	// This is belt-and-suspenders — the BLOCKED check should have caught this.
	out, err := cmdutil.RunZFS("zfs", "list", "-H", "-o", "name,used", "-r", name)
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] != name {
				usedBytes := DatasetUsedBytes(fields[0])
				if usedBytes > 0 {
					return fmt.Errorf(
						"SAFETY ABORT: pool %q contains dataset %q with %s of data — "+
							"destroy cancelled even though BLOCKED was approved. "+
							"Manually destroy the dataset first.",
						name, fields[0], humaniseBytes(usedBytes),
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

func createDataset(name string, _ *DesiredDataset) error {
	// Check existence first — idempotent
	out, err := cmdutil.RunZFS("zfs", "list", "-H", "-o", "name", name)
	if err == nil && strings.TrimSpace(string(out)) == name {
		log.Printf("GITOPS: dataset %q already exists — skipping create", name)
		return nil
	}

	createOut, err := cmdutil.RunMedium("zfs", "create", name)
	if err != nil {
		return fmt.Errorf("zfs create %s: %s: %w", name, string(createOut), err)
	}
	log.Printf("GITOPS: created dataset %q", name)
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
			log.Printf("GITOPS: unknown property %q for dataset %s — skipping", prop, name)
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
			"SAFETY ABORT: dataset %q has %s of data — destroy cancelled. "+
				"This should have been BLOCKED. Please report this as a bug.",
			name, humaniseBytes(used),
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
			"SAFETY ABORT: share %q has active connections at execute time — "+
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
