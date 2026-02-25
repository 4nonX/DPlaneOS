package gitops

import (
	"fmt"
	"strconv"
	"strings"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  DIFF ENGINE + BLOCKED SAFETY CONTRACT
//
//  The diff engine computes the delta between DesiredState and LiveState.
//  Every delta item is classified into one of five DiffAction values.
//
//  THE SAFETY CONTRACT (non-negotiable):
//
//  A DiffItem is classified BLOCKED — not SAFE, not MODIFY, not DELETE — when
//  the proposed action could cause irreversible data loss with active users.
//
//  Specifically:
//
//    1. DATASET DESTROY: blocked if `zfs get used` > 0 bytes.
//       Even 1 byte of used space is enough to block. The operator must manually
//       snapshot and destroy, or explicitly override via the approval API.
//
//    2. SHARE REMOVAL: blocked if smbstatus reports ≥1 active connection to
//       that share name at the moment the plan is evaluated.
//
//  BLOCKED items are included in the plan JSON so the operator can see them,
//  approve them individually (via /api/gitops/approve), and then re-apply.
//  They are NEVER applied automatically, even if auto_apply is enabled.
//
//  A plan containing even one BLOCKED item does NOT apply. The entire plan
//  halts at the BLOCKED item. Already-applied steps are NOT rolled back
//  (they were SAFE changes). Only BLOCKED items prevent forward progress.
// ═══════════════════════════════════════════════════════════════════════════════

// DiffAction classifies what the reconciler must do for one resource.
type DiffAction string

const (
	// ActionNOP — desired matches live. Nothing to do.
	ActionNOP DiffAction = "NOP"

	// ActionCreate — resource exists in desired but not in live. Safe to create.
	ActionCreate DiffAction = "CREATE"

	// ActionModify — resource exists in both, but properties differ. Safe to update.
	ActionModify DiffAction = "MODIFY"

	// ActionDelete — resource exists in live but not in desired.
	// Requires the BLOCKED check before becoming safe.
	ActionDelete DiffAction = "DELETE"

	// ActionBlocked — action would cause irreversible data loss or disrupt active
	// users. Requires explicit human approval before it can be applied.
	// See: BlockReason for the specific reason.
	ActionBlocked DiffAction = "BLOCKED"
)

// ResourceKind identifies what type of resource a DiffItem describes.
type ResourceKind string

const (
	KindPool    ResourceKind = "pool"
	KindDataset ResourceKind = "dataset"
	KindShare   ResourceKind = "share"
)

// DiffItem is one entry in the reconciliation plan.
type DiffItem struct {
	Kind    ResourceKind `json:"kind"`
	Name    string       `json:"name"`
	Action  DiffAction   `json:"action"`

	// Changes lists the specific property deltas (for MODIFY actions).
	// Format: "property: live_value → desired_value"
	Changes []string `json:"changes,omitempty"`

	// BlockReason is set when Action == ActionBlocked.
	// This is the human-readable explanation shown to the operator.
	BlockReason string `json:"block_reason,omitempty"`

	// RiskLevel classifies severity: "low", "medium", "high", "critical".
	// Drives UI coloring and approval UX.
	RiskLevel string `json:"risk_level"`

	// Approved is set true when the operator has explicitly approved a BLOCKED item.
	// Only approved BLOCKED items can be applied via ApplyPlan.
	Approved bool `json:"approved"`

	// Desired* carry the full spec for CREATE/MODIFY operations so the apply
	// engine does not need to re-look up the DesiredState.
	DesiredPool    *DesiredPool    `json:"desired_pool,omitempty"`
	DesiredDataset *DesiredDataset `json:"desired_dataset,omitempty"`
	DesiredShare   *DesiredShare   `json:"desired_share,omitempty"`
}

// Plan is the complete reconciliation plan: the ordered list of DiffItems.
// Items are ordered: CREATE first, then MODIFY, then DELETE/BLOCKED last.
// This ordering ensures new dependencies exist before things that need them.
type Plan struct {
	Items        []DiffItem `json:"items"`
	HasBlocked   bool       `json:"has_blocked"`
	SafeToApply  bool       `json:"safe_to_apply"`  // true only if zero BLOCKED items
	CreateCount  int        `json:"create_count"`
	ModifyCount  int        `json:"modify_count"`
	DeleteCount  int        `json:"delete_count"`
	BlockedCount int        `json:"blocked_count"`
	NopCount     int        `json:"nop_count"`
}

// ═══════════════════════════════════════════════════════════════════════════════
//  DIFF ENGINE
// ═══════════════════════════════════════════════════════════════════════════════

// ComputeDiff compares desired against live and returns the reconciliation Plan.
//
// The diff engine makes live ZFS/smbstatus calls for BLOCKED classification —
// callers must ensure these are acceptable at call time (e.g., not during
// a ZFS scrub or replication operation).
func ComputeDiff(desired *DesiredState, live *LiveState) *Plan {
	plan := &Plan{}

	// Build lookup maps for O(1) access
	livePools := make(map[string]LivePool)
	for _, p := range live.Pools {
		livePools[p.Name] = p
	}

	liveDatasets := make(map[string]LiveDataset)
	for _, d := range live.Datasets {
		liveDatasets[d.Name] = d
	}

	liveShares := make(map[string]LiveShare)
	for _, s := range live.Shares {
		liveShares[s.Name] = s
	}

	desiredPools := make(map[string]DesiredPool)
	for _, p := range desired.Pools {
		desiredPools[p.Name] = p
	}

	desiredDatasets := make(map[string]DesiredDataset)
	for _, d := range desired.Datasets {
		desiredDatasets[d.Name] = d
	}

	desiredShares := make(map[string]DesiredShare)
	for _, s := range desired.Shares {
		desiredShares[s.Name] = s
	}

	// ── Phase 1: CREATE items (desired exists, live does not) ─────────────────
	// These go first in the plan — safe operations with no existing data at risk.

	for _, dp := range desired.Pools {
		if _, exists := livePools[dp.Name]; !exists {
			item := dp // copy
		plan.Items = append(plan.Items, DiffItem{
				Kind:        KindPool,
				Name:        dp.Name,
				Action:      ActionCreate,
				RiskLevel:   "low",
				DesiredPool: &item,
			})
		}
	}

	for _, dd := range desired.Datasets {
		if _, exists := liveDatasets[dd.Name]; !exists {
			item := dd // copy
		plan.Items = append(plan.Items, DiffItem{
				Kind:            KindDataset,
				Name:            dd.Name,
				Action:          ActionCreate,
				RiskLevel:       "low",
				DesiredDataset:  &item,
			})
		}
	}

	for _, ds := range desired.Shares {
		if _, exists := liveShares[ds.Name]; !exists {
			item := ds // copy
		plan.Items = append(plan.Items, DiffItem{
				Kind:         KindShare,
				Name:         ds.Name,
				Action:       ActionCreate,
				RiskLevel:    "low",
				DesiredShare: &item,
			})
		}
	}

	// ── Phase 2: MODIFY items (both exist, properties differ) ─────────────────

	for _, dp := range desired.Pools {
		lp, exists := livePools[dp.Name]
		if !exists {
			continue // handled as CREATE above
		}
		changes := diffPool(dp, lp)
		if len(changes) == 0 {
			plan.Items = append(plan.Items, DiffItem{
				Kind: KindPool, Name: dp.Name, Action: ActionNOP, RiskLevel: "low",
			})
			continue
		}
		dpCopy := dp
		plan.Items = append(plan.Items, DiffItem{
			Kind:        KindPool,
			Name:        dp.Name,
			Action:      ActionModify,
			Changes:     changes,
			RiskLevel:   riskForPoolChanges(changes),
			DesiredPool: &dpCopy,
		})
	}

	for _, dd := range desired.Datasets {
		ld, exists := liveDatasets[dd.Name]
		if !exists {
			continue
		}
		changes := diffDataset(dd, ld)
		if len(changes) == 0 {
			plan.Items = append(plan.Items, DiffItem{
				Kind: KindDataset, Name: dd.Name, Action: ActionNOP, RiskLevel: "low",
			})
			continue
		}
		ddCopy := dd
		plan.Items = append(plan.Items, DiffItem{
			Kind:           KindDataset,
			Name:           dd.Name,
			Action:         ActionModify,
			Changes:        changes,
			RiskLevel:      riskForDatasetChanges(changes),
			DesiredDataset: &ddCopy,
		})
	}

	for _, ds := range desired.Shares {
		ls, exists := liveShares[ds.Name]
		if !exists {
			continue
		}
		changes := diffShare(ds, ls)
		if len(changes) == 0 {
			plan.Items = append(plan.Items, DiffItem{
				Kind: KindShare, Name: ds.Name, Action: ActionNOP, RiskLevel: "low",
			})
			continue
		}
		dsCopy := ds
		plan.Items = append(plan.Items, DiffItem{
			Kind:         KindShare,
			Name:         ds.Name,
			Action:       ActionModify,
			Changes:      changes,
			RiskLevel:    "low",
			DesiredShare: &dsCopy,
		})
	}

	// ── Phase 3: DELETE / BLOCKED (live exists, not in desired) ───────────────
	// This is where the safety contract is enforced.
	// Every potential deletion goes through blockedCheck before being classified.

	for _, lp := range live.Pools {
		if _, wanted := desiredPools[lp.Name]; wanted {
			continue
		}
		item := blockedCheckPool(lp)
		plan.Items = append(plan.Items, item)
	}

	for _, ld := range live.Datasets {
		if _, wanted := desiredDatasets[ld.Name]; wanted {
			continue
		}
		item := blockedCheckDataset(ld)
		plan.Items = append(plan.Items, item)
	}

	for _, ls := range live.Shares {
		if _, wanted := desiredShares[ls.Name]; wanted {
			continue
		}
		item := blockedCheckShare(ls)
		plan.Items = append(plan.Items, item)
	}

	// ── Tally ─────────────────────────────────────────────────────────────────
	for _, item := range plan.Items {
		switch item.Action {
		case ActionCreate:
			plan.CreateCount++
		case ActionModify:
			plan.ModifyCount++
		case ActionDelete:
			plan.DeleteCount++
		case ActionBlocked:
			plan.BlockedCount++
			plan.HasBlocked = true
		case ActionNOP:
			plan.NopCount++
		}
	}
	plan.SafeToApply = !plan.HasBlocked

	return plan
}

// ═══════════════════════════════════════════════════════════════════════════════
//  BLOCKED SAFETY CONTRACT — implementation
//
//  These three functions are the authoritative implementation of the safety
//  contract described at the top of this file.
//  They make live system calls to gather current state at plan-evaluation time.
// ═══════════════════════════════════════════════════════════════════════════════

// blockedCheckPool evaluates whether destroying a pool should be BLOCKED.
//
// A pool destroy is always BLOCKED. Pools contain datasets, snapshots, and
// potentially terabytes of data. There is no safe automatic pool destruction.
// The operator must manually `zpool destroy` after verifying the pool is empty.
func blockedCheckPool(lp LivePool) DiffItem {
	return DiffItem{
		Kind:   KindPool,
		Name:   lp.Name,
		Action: ActionBlocked,
		BlockReason: fmt.Sprintf(
			"Pool %q exists in live state but not in desired state. "+
				"Pool destruction is never performed automatically because it destroys ALL "+
				"datasets and snapshots contained within it. "+
				"To remove this pool: manually run `zpool export %s` or `zpool destroy %s` "+
				"after verifying all data has been migrated.",
			lp.Name, lp.Name, lp.Name,
		),
		RiskLevel: "critical",
	}
}

// blockedCheckDataset evaluates whether destroying a dataset should be BLOCKED.
//
// Rule: if the dataset has ANY used space (used > 0 bytes), the action is BLOCKED.
// We re-query `zfs get used` live at diff time — not the cached LiveState value —
// because data may have been written between ReadLiveState() and this check.
func blockedCheckDataset(ld LiveDataset) DiffItem {
	// Re-query used space live — must not rely on cached value
	usedBytes := DatasetUsedBytes(ld.Name)

	if usedBytes > 0 {
		return DiffItem{
			Kind:   KindDataset,
			Name:   ld.Name,
			Action: ActionBlocked,
			BlockReason: fmt.Sprintf(
				"Dataset %q has %s of used data and cannot be destroyed automatically. "+
					"To resolve: (1) verify the data is no longer needed, "+
					"(2) create a snapshot if you want a recovery point, "+
					"(3) manually run `zfs destroy %s`, "+
					"(4) then re-apply this plan.",
				ld.Name, humaniseBytes(usedBytes), ld.Name,
			),
			RiskLevel: "critical",
		}
	}

	// Dataset exists but is genuinely empty — safe to delete
	return DiffItem{
		Kind:      KindDataset,
		Name:      ld.Name,
		Action:    ActionDelete,
		RiskLevel: "medium", // medium even when empty — irreversible
	}
}

// blockedCheckShare evaluates whether removing a share should be BLOCKED.
//
// Rule: if smbstatus reports any active connection to this share name at the
// moment the plan is evaluated, the action is BLOCKED.
//
// If smbstatus is unavailable (Samba not running, tool not installed), the
// check returns DELETE (not BLOCKED) — we cannot confirm connections, and
// removing a share does not destroy data.
func blockedCheckShare(ls LiveShare) DiffItem {
	if HasActiveSMBConnections(ls.Name) {
		return DiffItem{
			Kind:   KindShare,
			Name:   ls.Name,
			Action: ActionBlocked,
			BlockReason: fmt.Sprintf(
				"Share %q has active client connections and cannot be removed while in use. "+
					"Disconnecting active clients without warning may cause data loss "+
					"on the client side (unsaved open files). "+
					"To resolve: wait for clients to disconnect, or notify users before removing. "+
					"Then re-evaluate the plan.",
				ls.Name,
			),
			RiskLevel: "high",
		}
	}

	return DiffItem{
		Kind:      KindShare,
		Name:      ls.Name,
		Action:    ActionDelete,
		RiskLevel: "low", // share removal does not destroy data
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
//  PROPERTY DIFF HELPERS
// ═══════════════════════════════════════════════════════════════════════════════

// diffPool returns the list of property changes between desired and live pool.
// Pool vdev structure changes (adding disks) are reported but are MODIFY, not
// BLOCKED — adding disks is safe. Removing disks from a live pool is complex
// and is always flagged as high-risk in the change description.
func diffPool(desired DesiredPool, live LivePool) []string {
	var changes []string

	// Health changes are read-only (we can't set health), so only report.
	if live.Health != "ONLINE" {
		changes = append(changes, fmt.Sprintf("health: %s (degraded — check zpool status)", live.Health))
	}

	// Disk membership changes
	desiredDisks := make(map[string]bool)
	for _, d := range desired.Disks {
		desiredDisks[d] = true
	}
	liveDisks := make(map[string]bool)
	for _, d := range live.Disks {
		liveDisks[d] = true
	}

	for d := range desiredDisks {
		if !liveDisks[d] {
			changes = append(changes, fmt.Sprintf("disk-add: %s (will run zpool add)", d))
		}
	}
	for d := range liveDisks {
		if !desiredDisks[d] {
			// Disk removal from a pool is complex and potentially dangerous
			changes = append(changes, fmt.Sprintf(
				"disk-remove: %s ⚠ disk removal from live pools requires careful planning — manual intervention required", d,
			))
		}
	}

	return changes
}

// diffDataset returns property changes between desired and live dataset.
func diffDataset(desired DesiredDataset, live LiveDataset) []string {
	var changes []string

	if desired.Compression != "" && desired.Compression != live.Compression {
		changes = append(changes, fmt.Sprintf("compression: %s → %s", live.Compression, desired.Compression))
	}
	if desired.Atime != "" && desired.Atime != live.Atime {
		changes = append(changes, fmt.Sprintf("atime: %s → %s", live.Atime, desired.Atime))
	}
	if desired.Mountpoint != "" && desired.Mountpoint != live.Mountpoint {
		changes = append(changes, fmt.Sprintf("mountpoint: %s → %s", live.Mountpoint, desired.Mountpoint))
	}

	// Quota: normalise both to bytes for comparison
	desiredQuotaBytes := parseQuota(desired.Quota)
	liveQuotaBytes := parseQuota(live.Quota)
	if desired.Quota != "" && desiredQuotaBytes != liveQuotaBytes {
		changes = append(changes, fmt.Sprintf("quota: %s → %s", live.Quota, desired.Quota))
	}

	return changes
}

// diffShare returns property changes between desired and live share.
func diffShare(desired DesiredShare, live LiveShare) []string {
	var changes []string

	if desired.Path != live.Path {
		changes = append(changes, fmt.Sprintf("path: %s → %s", live.Path, desired.Path))
	}
	if desired.ReadOnly != live.ReadOnly {
		changes = append(changes, fmt.Sprintf("read_only: %v → %v", live.ReadOnly, desired.ReadOnly))
	}
	if desired.ValidUsers != live.ValidUsers {
		changes = append(changes, fmt.Sprintf("valid_users: %q → %q", live.ValidUsers, desired.ValidUsers))
	}
	if desired.Comment != live.Comment {
		changes = append(changes, fmt.Sprintf("comment: %q → %q", live.Comment, desired.Comment))
	}
	if desired.GuestOK != live.GuestOK {
		changes = append(changes, fmt.Sprintf("guest_ok: %v → %v", live.GuestOK, desired.GuestOK))
	}

	return changes
}

// ── Risk classification ───────────────────────────────────────────────────────

func riskForPoolChanges(changes []string) string {
	for _, c := range changes {
		if strings.Contains(c, "disk-remove") || strings.Contains(c, "degraded") {
			return "critical"
		}
		if strings.Contains(c, "disk-add") {
			return "medium"
		}
	}
	return "low"
}

func riskForDatasetChanges(changes []string) string {
	for _, c := range changes {
		if strings.Contains(c, "mountpoint") {
			return "medium" // mountpoint changes affect running services
		}
		if strings.Contains(c, "quota") {
			return "low"
		}
	}
	return "low"
}

// ── Utility ───────────────────────────────────────────────────────────────────

// parseQuota converts a quota string to bytes for comparison.
// Returns 0 for "none" or unparseable values.
func parseQuota(q string) uint64 {
	q = strings.TrimSpace(q)
	if q == "" || q == "none" || q == "0" {
		return 0
	}

	multipliers := map[byte]uint64{
		'K': 1024, 'M': 1024 * 1024, 'G': 1024 * 1024 * 1024,
		'T': 1024 * 1024 * 1024 * 1024,
	}

	if len(q) > 1 {
		suffix := q[len(q)-1]
		if mult, ok := multipliers[suffix]; ok {
			// Human-readable form (e.g., "2T")
			var n float64
			fmt.Sscanf(q[:len(q)-1], "%f", &n)
			return uint64(n * float64(mult))
		}
	}

	// Already a raw byte count (from `zfs get -p`)
	n, _ := strconv.ParseUint(q, 10, 64)
	return n
}

// humaniseBytes formats a byte count as a human-readable string.
// Mirrors the format used by ZFS output for consistency in error messages.
func humaniseBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
