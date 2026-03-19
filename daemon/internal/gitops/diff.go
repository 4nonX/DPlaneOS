package gitops

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"dplaned/internal/nixwriter"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  DIFF ENGINE + BLOCKED SAFETY CONTRACT
//
//  The diff engine computes the delta between DesiredState and LiveState.
//  Every delta item is classified into one of five DiffAction values.
//
//  THE SAFETY CONTRACT (non-negotiable):
//
//  A DiffItem is classified BLOCKED - not SAFE, not MODIFY, not DELETE - when
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
	// ActionNOP - desired matches live. Nothing to do.
	ActionNOP DiffAction = "NOP"

	// ActionCreate - resource exists in desired but not in live. Safe to create.
	ActionCreate DiffAction = "CREATE"

	// ActionModify - resource exists in both, but properties differ. Safe to update.
	ActionModify DiffAction = "MODIFY"

	// ActionDelete - resource exists in live but not in desired.
	// Requires the BLOCKED check before becoming safe.
	ActionDelete DiffAction = "DELETE"

	// ActionBlocked - action would cause irreversible data loss or disrupt active
	// users. Requires explicit human approval before it can be applied.
	// See: BlockReason for the specific reason.
	ActionBlocked DiffAction = "BLOCKED"

	// ActionAmbiguous - the live system is in an unexpected or degenerate state
	// (e.g. duplicate pool names) that makes automated action unsafe.
	// Requires manual resolution on the host.
	ActionAmbiguous DiffAction = "AMBIGUOUS"
)

// ResourceKind identifies what type of resource a DiffItem describes.
type ResourceKind string

const (
	KindPool    ResourceKind = "pool"
	KindDataset ResourceKind = "dataset"
	KindShare   ResourceKind = "share"
	KindNFS     ResourceKind = "nfs"
	KindStack   ResourceKind = "stack"
	KindSystem  ResourceKind = "system"
	KindUser    ResourceKind = "user"
	KindGroup   ResourceKind = "group"
	KindReplication ResourceKind = "replication"
	KindLDAP        ResourceKind = "ldap"
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
	DesiredNFS     *DesiredNFS     `json:"desired_nfs,omitempty"`
	DesiredStack   *DesiredStack   `json:"desired_stack,omitempty"`
	DesiredSystem  *DesiredSystem  `json:"desired_system,omitempty"`
	DesiredUser    *DesiredUser    `json:"desired_user,omitempty"`
	DesiredGroup   *DesiredGroup   `json:"desired_group,omitempty"`
	DesiredReplication *DesiredReplication `json:"desired_replication,omitempty"`
	DesiredLDAP        *DesiredLDAP        `json:"desired_ldap,omitempty"`
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
	AmbiguousCount int     `json:"ambiguous_count"`
	NopCount     int        `json:"nop_count"`
	HasAmbiguous bool       `json:"has_ambiguous"`
}

// ═══════════════════════════════════════════════════════════════════════════════
//  DIFF ENGINE
// ═══════════════════════════════════════════════════════════════════════════════

// ComputeDiff compares desired against live and returns the reconciliation Plan.
//
// The diff engine makes live ZFS/smbstatus calls for BLOCKED classification -
// callers must ensure these are acceptable at call time (e.g., not during
// a ZFS scrub or replication operation).
func ComputeDiff(desired *DesiredState, live *LiveState) *Plan {
	plan := &Plan{}

	// Build lookup maps for O(1) access
	livePools := make(map[string]LivePool)
	ambiguousPools := make(map[string]bool)
	for _, p := range live.Pools {
		if _, exists := livePools[p.Name]; exists {
			ambiguousPools[p.Name] = true
		}
		livePools[p.Name] = p
	}

	liveDatasets := make(map[string]LiveDataset)
	ambiguousDatasets := make(map[string]bool)
	for _, d := range live.Datasets {
		if _, exists := liveDatasets[d.Name]; exists {
			ambiguousDatasets[d.Name] = true
		}
		liveDatasets[d.Name] = d
	}

	liveShares := make(map[string]LiveShare)
	for _, s := range live.Shares {
		liveShares[s.Name] = s
	}
 
	liveNFS := make(map[string]LiveNFSExport)
	for _, e := range live.NFS {
		liveNFS[e.Path] = e
	}

	liveUsers := make(map[string]LiveUser)
	for _, u := range live.Users {
		liveUsers[u.Username] = u
	}

	liveGroups := make(map[string]LiveGroup)
	for _, g := range live.Groups {
		liveGroups[g.Name] = g
	}

	liveRepls := make(map[string]LiveReplication)
	for _, r := range live.Replication {
		liveRepls[r.Name] = r
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

	desiredNFS := make(map[string]DesiredNFS)
	for _, n := range desired.NFS {
		desiredNFS[n.Path] = n
	}

	desiredStacks := make(map[string]DesiredStack)
	for _, s := range desired.Stacks {
		desiredStacks[s.Name] = s
	}

	liveStacks := make(map[string]LiveStack)
	for _, s := range live.Stacks {
		liveStacks[s.Name] = s
	}

	desiredGroups := make(map[string]DesiredGroup)
	for _, g := range desired.Groups {
		desiredGroups[g.Name] = g
	}

	desiredRepls := make(map[string]DesiredReplication)
	for _, r := range desired.Replication {
		desiredRepls[r.Name] = r
	}

	// ── Phase -1: AMBIGUITY Detection ─────────────────────────────────────────
	// If the live state is ambiguous (e.g. two pools with the same name),
	// we must halt before doing anything else.
	for name := range ambiguousPools {
		plan.Items = append(plan.Items, DiffItem{
			Kind:   KindPool,
			Name:   name,
			Action: ActionAmbiguous,
			BlockReason: fmt.Sprintf(
				"Multiple pools found with name %q. Automated GitOps cannot safely distinguish "+
				"between them. Please resolve the name collision manually (e.g. by exporting or renaming one).",
				name,
			),
			RiskLevel: "critical",
		})
		plan.HasAmbiguous = true
		plan.AmbiguousCount++
	}
	for name := range ambiguousDatasets {
		plan.Items = append(plan.Items, DiffItem{
			Kind:   KindDataset,
			Name:   name,
			Action: ActionAmbiguous,
			BlockReason: fmt.Sprintf(
				"Multiple datasets found with name %q. This state is degenerate and requires manual investigation.",
				name,
			),
			RiskLevel: "critical",
		})
		plan.HasAmbiguous = true
		plan.AmbiguousCount++
	}
	if plan.HasAmbiguous {
		return plan
	}
 
	// ── Phase 0: SYSTEM configuration ─────────────────────────────────────────
	if desired.System != nil {
		if live.System == nil {
			// System always exists conceptually, but if for some reason live state 
			// reading failed, we treat it as a modify if we have desired state.
			// Actually ReadLiveState always returns a non-nil System if it doesn't error.
			plan.Items = append(plan.Items, DiffItem{
				Kind:          KindSystem,
				Name:          "system",
				Action:        ActionModify,
				RiskLevel:     "medium",
				DesiredSystem: desired.System,
			})
		} else {
			changes := diffSystem(*desired.System, *live.System)
			if len(changes) > 0 {
				plan.Items = append(plan.Items, DiffItem{
					Kind:          KindSystem,
					Name:          "system",
					Action:        ActionModify,
					Changes:       changes,
					RiskLevel:     riskForSystemChanges(changes),
					DesiredSystem: desired.System,
				})
			} else {
				plan.Items = append(plan.Items, DiffItem{
					Kind: KindSystem, Name: "system", Action: ActionNOP, RiskLevel: "low",
				})
			}
		}
	}

	// ── Phase 1: CREATE items (desired exists, live does not) ─────────────────
	// These go first in the plan - safe operations with no existing data at risk.

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
			// Gap 4: Validate that the share path points to a known dataset mountpoint.
			// This prevents "dangling shares" and ensures proper ordering.
			datasetFound := false
			for _, dd := range desired.Datasets {
				if dd.Mountpoint != "" && ds.Path == dd.Mountpoint {
					datasetFound = true
					break
				}
			}
			if !datasetFound {
				plan.Items = append(plan.Items, DiffItem{
					Kind:   KindShare,
					Name:   ds.Name,
					Action: ActionBlocked,
					BlockReason: fmt.Sprintf(
						"Share %q path %q does not correspond to any managed dataset's mountpoint. "+
							"Every share must have a backing dataset to ensure data persistence and correct boot ordering.",
						ds.Name, ds.Path,
					),
					RiskLevel: "high",
				})
				continue
			}
			plan.Items = append(plan.Items, DiffItem{
				Kind:         KindShare,
				Name:         ds.Name,
				Action:       ActionCreate,
				RiskLevel:    "low",
				DesiredShare: &item,
			})
		}
	}
 
	for _, dn := range desired.NFS {
		if _, exists := liveNFS[dn.Path]; !exists {
			item := dn // copy
			// Gap 4: Validation for NFS paths
			datasetFound := false
			for _, dd := range desired.Datasets {
				if dd.Mountpoint != "" && dn.Path == dd.Mountpoint {
					datasetFound = true
					break
				}
			}
			if !datasetFound {
				plan.Items = append(plan.Items, DiffItem{
					Kind:   KindNFS,
					Name:   dn.Path,
					Action: ActionBlocked,
					BlockReason: fmt.Sprintf(
						"NFS export path %q does not correspond to any managed dataset's mountpoint.",
						dn.Path,
					),
					RiskLevel: "high",
				})
				continue
			}
			plan.Items = append(plan.Items, DiffItem{
				Kind:       KindNFS,
				Name:       dn.Path,
				Action:     ActionCreate,
				RiskLevel:  "low",
				DesiredNFS: &item,
			})
		}
	}

	for _, dst := range desired.Stacks {
		if _, exists := liveStacks[dst.Name]; !exists {
			item := dst // copy
			plan.Items = append(plan.Items, DiffItem{
				Kind:         KindStack,
				Name:         dst.Name,
				Action:       ActionCreate,
				RiskLevel:    "medium",
				DesiredStack: &item,
			})
		}
	}

	for _, du := range desired.Users {
		if _, exists := liveUsers[du.Username]; !exists {
			item := du
			plan.Items = append(plan.Items, DiffItem{
				Kind:        KindUser,
				Name:        du.Username,
				Action:      ActionCreate,
				RiskLevel:   "medium",
				DesiredUser: &item,
			})
			plan.CreateCount++
		}
	}

	for _, dg := range desired.Groups {
		if _, exists := liveGroups[dg.Name]; !exists {
			item := dg
			plan.Items = append(plan.Items, DiffItem{
				Kind:         KindGroup,
				Name:         dg.Name,
				Action:       ActionCreate,
				RiskLevel:    "low",
				DesiredGroup: &item,
			})
			plan.CreateCount++
		}
	}

	for _, dr := range desired.Replication {
		if _, exists := liveRepls[dr.Name]; !exists {
			item := dr
			plan.Items = append(plan.Items, DiffItem{
				Kind:               KindReplication,
				Name:               dr.Name,
				Action:             ActionCreate,
				RiskLevel:          "low",
				DesiredReplication: &item,
			})
			plan.CreateCount++
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

		// Gap 4: Validation for modified shares too
		datasetFound := false
		for _, dd := range desired.Datasets {
			if dd.Mountpoint != "" && ds.Path == dd.Mountpoint {
				datasetFound = true
				break
			}
		}
		if !datasetFound {
			plan.Items = append(plan.Items, DiffItem{
				Kind:   KindShare,
				Name:   ds.Name,
				Action: ActionBlocked,
				BlockReason: fmt.Sprintf(
					"Share %q path %q does not correspond to any managed dataset's mountpoint.",
					ds.Name, ds.Path,
				),
				RiskLevel: "high",
			})
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
 
	for _, dn := range desired.NFS {
		ln, exists := liveNFS[dn.Path]
		if !exists {
			continue
		}
		changes := diffNFS(dn, ln)
		if len(changes) == 0 {
			plan.Items = append(plan.Items, DiffItem{
				Kind: KindNFS, Name: dn.Path, Action: ActionNOP, RiskLevel: "low",
			})
			continue
		}
		dnCopy := dn
		plan.Items = append(plan.Items, DiffItem{
			Kind:       KindNFS,
			Name:       dn.Path,
			Action:     ActionModify,
			Changes:    changes,
			RiskLevel:  "low",
			DesiredNFS: &dnCopy,
		})
	}

	for _, dst := range desired.Stacks {
		lst, exists := liveStacks[dst.Name]
		if !exists {
			continue
		}
		changes := diffStack(dst, lst)
		if len(changes) == 0 {
			plan.Items = append(plan.Items, DiffItem{
				Kind: KindStack, Name: dst.Name, Action: ActionNOP, RiskLevel: "low",
			})
			continue
		}
		dstCopy := dst
		plan.Items = append(plan.Items, DiffItem{
			Kind:         KindStack,
			Name:         dst.Name,
			Action:       ActionModify,
			Changes:      changes,
			RiskLevel:    riskForStackChanges(changes),
			DesiredStack: &dstCopy,
		})
	}

	for _, du := range desired.Users {
		if l, ok := liveUsers[du.Username]; ok {
			changes := diffUser(du, l)
			if len(changes) > 0 {
				item := du
				plan.Items = append(plan.Items, DiffItem{
					Kind:        KindUser,
					Name:        du.Username,
					Action:      ActionModify,
					Changes:     changes,
					RiskLevel:   "high",
					DesiredUser: &item,
				})
			}
		}
	}

	for _, dg := range desired.Groups {
		if l, ok := liveGroups[dg.Name]; ok {
			changes := diffGroup(dg, l)
			if len(changes) > 0 {
				item := dg
				plan.Items = append(plan.Items, DiffItem{
					Kind:         KindGroup,
					Name:         dg.Name,
					Action:       ActionModify,
					Changes:      changes,
					RiskLevel:    "medium",
					DesiredGroup: &item,
				})
			}
		}
	}

	for _, dr := range desired.Replication {
		if l, ok := liveRepls[dr.Name]; ok {
			changes := diffReplication(dr, l)
			if len(changes) > 0 {
				item := dr
				plan.Items = append(plan.Items, DiffItem{
					Kind:               KindReplication,
					Name:               dr.Name,
					Action:             ActionModify,
					Changes:            changes,
					RiskLevel:          "medium",
					DesiredReplication: &item,
				})
			}
		}
	}

	// ── Phase 3: DELETE / BLOCKED (live exists, not in desired) ───────────────
	// This is where the safety contract is enforced.
	// Every potential deletion goes through blockedCheck before being classified.

	if !desired.IgnoreExtraneous {
		for _, lp := range live.Pools {
			if _, wanted := desiredPools[lp.Name]; wanted {
				continue
			}
			item := blockedCheckPool(lp)
			plan.Items = append(plan.Items, item)
		}
	}

	if !desired.IgnoreExtraneous {
		for _, ld := range live.Datasets {
			if _, wanted := desiredDatasets[ld.Name]; wanted {
				continue
			}
			item := blockedCheckDataset(ld)
			plan.Items = append(plan.Items, item)
		}
	}

	if !desired.IgnoreExtraneous {
		for _, ls := range live.Shares {
			if _, wanted := desiredShares[ls.Name]; wanted {
				continue
			}
			item := blockedCheckShare(ls)
			plan.Items = append(plan.Items, item)
		}
	}
 
	if !desired.IgnoreExtraneous {
		for _, ln := range live.NFS {
			if _, wanted := desiredNFS[ln.Path]; wanted {
				continue
			}
			// NFS deletion is safe (no data loss)
			plan.Items = append(plan.Items, DiffItem{
				Kind:      KindNFS,
				Name:      ln.Path,
				Action:    ActionDelete,
				RiskLevel: "low",
			})
		}
	}

	if !desired.IgnoreExtraneous {
		for _, lst := range live.Stacks {
			if _, wanted := desiredStacks[lst.Name]; wanted {
				continue
			}
			// Stack deletion is safe (ActionDelete), but we mark it high risk
			plan.Items = append(plan.Items, DiffItem{
				Kind:      KindStack,
				Name:      lst.Name,
				Action:    ActionDelete,
				RiskLevel: "high",
			})
		}
	}

	for _, lu := range live.Users {
		found := false
		for _, du := range desired.Users {
			if du.Username == lu.Username { found = true; break }
		}
		if !found && lu.Username != "admin" && lu.Username != "root" {
			plan.Items = append(plan.Items, DiffItem{
				Kind:      KindUser,
				Name:      lu.Username,
				Action:    ActionDelete,
				RiskLevel: "high",
			})
		}
	}

	for _, lg := range live.Groups {
		if _, wanted := desiredGroups[lg.Name]; wanted {
			continue
		}
		plan.Items = append(plan.Items, DiffItem{
			Kind:      KindGroup,
			Name:      lg.Name,
			Action:    ActionDelete,
			RiskLevel: "medium",
		})
	}

	for _, lr := range live.Replication {
		if _, wanted := desiredRepls[lr.Name]; wanted {
			continue
		}
		plan.Items = append(plan.Items, DiffItem{
			Kind:      KindReplication,
			Name:      lr.Name,
			Action:    ActionDelete,
			RiskLevel: "low",
		})
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
		case ActionAmbiguous:
			plan.AmbiguousCount++
			plan.HasAmbiguous = true
		case ActionNOP:
			plan.NopCount++
		}
	}
	// ── Phase 4: Finalize and Sort ────────────────────────────────────────────

	// RELEVANT for v6: Sort items by Kind to ensure strict execution phases.
	// Order: System -> Pool -> Dataset -> Share -> NFS -> User -> Group -> Replication -> LDAP -> Stack
	kindOrder := map[ResourceKind]int{
		KindSystem:      1,
		KindPool:        2,
		KindDataset:     3,
		KindShare:       4,
		KindNFS:         5,
		KindUser:        6,
		KindGroup:       7,
		KindReplication: 8,
		KindLDAP:        9,
		KindStack:       10,
	}

	sort.SliceStable(plan.Items, func(i, j int) bool {
		return kindOrder[plan.Items[i].Kind] < kindOrder[plan.Items[j].Kind]
	})

	plan.SafeToApply = !plan.HasBlocked
	return plan
}

// ═══════════════════════════════════════════════════════════════════════════════
//  BLOCKED SAFETY CONTRACT - implementation
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
// We re-query `zfs get used` live at diff time - not the cached LiveState value -
// because data may have been written between ReadLiveState() and this check.
func blockedCheckDataset(ld LiveDataset) DiffItem {
	// Re-query used space live - must not rely on cached value
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
				ld.Name, HumaniseBytes(usedBytes), ld.Name,
			),
			RiskLevel: "critical",
		}
	}

	// Dataset exists but is genuinely empty - safe to delete
	return DiffItem{
		Kind:      KindDataset,
		Name:      ld.Name,
		Action:    ActionDelete,
		RiskLevel: "medium", // medium even when empty - irreversible
	}
}

// blockedCheckShare evaluates whether removing a share should be BLOCKED.
//
// Rule: if smbstatus reports any active connection to this share name at the
// moment the plan is evaluated, the action is BLOCKED.
//
// If smbstatus is unavailable (Samba not running, tool not installed), the
// check returns DELETE (not BLOCKED) - we cannot confirm connections, and
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
// BLOCKED - adding disks is safe. Removing disks from a live pool is complex
// and is always flagged as high-risk in the change description.
func diffPool(desired DesiredPool, live LivePool) []string {
	var changes []string

	// Health changes are read-only (we can't set health), so only report.
	if live.Health != "ONLINE" {
		changes = append(changes, fmt.Sprintf("health: %s (degraded - check zpool status)", live.Health))
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
				"disk-remove: %s ⚠ disk removal from live pools requires careful planning - manual intervention required", d,
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

func riskForStackChanges(changes []string) string {
	for _, c := range changes {
		if strings.Contains(c, "yaml") {
			return "high" // redeploying can be disruptive
		}
	}
	return "medium"
}
 
func diffUser(desired DesiredUser, live LiveUser) []string {
	var changes []string
	if desired.PasswordHash != "" && desired.PasswordHash != live.PasswordHash {
		changes = append(changes, "password_hash: (hidden) → (updated)")
	}
	if desired.Email != live.Email {
		changes = append(changes, fmt.Sprintf("email: %q → %q", live.Email, desired.Email))
	}
	if desired.Role != live.Role {
		changes = append(changes, fmt.Sprintf("role: %q → %q", live.Role, desired.Role))
	}
	if desired.Active != live.Active {
		changes = append(changes, fmt.Sprintf("active: %v → %v", live.Active, desired.Active))
	}
	return changes
}

func diffGroup(desired DesiredGroup, live LiveGroup) []string {
	var changes []string
	if desired.Description != live.Description {
		changes = append(changes, fmt.Sprintf("description: %q → %q", live.Description, desired.Description))
	}
	if desired.GID != 0 && desired.GID != live.GID {
		changes = append(changes, fmt.Sprintf("gid: %d → %d", live.GID, desired.GID))
	}
	if !equalSlices(desired.Members, live.Members) {
		changes = append(changes, fmt.Sprintf("members: %v → %v", live.Members, desired.Members))
	}
	return changes
}

func diffReplication(desired DesiredReplication, live LiveReplication) []string {
	var changes []string
	if desired.SourceDataset != live.SourceDataset {
		changes = append(changes, fmt.Sprintf("source_dataset: %q → %q", live.SourceDataset, desired.SourceDataset))
	}
	if desired.RemoteHost != live.RemoteHost {
		changes = append(changes, fmt.Sprintf("remote_host: %q → %q", live.RemoteHost, desired.RemoteHost))
	}
	if desired.RemotePort != live.RemotePort {
		changes = append(changes, fmt.Sprintf("remote_port: %d → %d", live.RemotePort, desired.RemotePort))
	}
	if desired.Interval != live.Interval {
		changes = append(changes, fmt.Sprintf("interval: %q → %q", live.Interval, desired.Interval))
	}
	if desired.Enabled != live.Enabled {
		changes = append(changes, fmt.Sprintf("enabled: %v → %v", live.Enabled, desired.Enabled))
	}
	return changes
}

func riskForSystemChanges(changes []string) string {
	for _, c := range changes {
		if strings.Contains(c, "hostname") || strings.Contains(c, "networking") {
			return "high" // changes that can break connectivity
		}
	}
	return "medium"
}
 
func diffNFS(desired DesiredNFS, live LiveNFSExport) []string {
	var changes []string
	if desired.Options != live.Options {
		changes = append(changes, fmt.Sprintf("options: %q → %q", live.Options, desired.Options))
	}
	if desired.Clients != live.Clients {
		changes = append(changes, fmt.Sprintf("clients: %q → %q", live.Clients, desired.Clients))
	}
	if desired.Enabled != live.Enabled {
		changes = append(changes, fmt.Sprintf("enabled: %v → %v", live.Enabled, desired.Enabled))
	}
	return changes
}

func diffStack(desired DesiredStack, live LiveStack) []string {
	var changes []string
	if desired.YAML != live.YAML {
		changes = append(changes, "yaml: (updated)")
	}
	if live.Status != "running" {
		changes = append(changes, fmt.Sprintf("status: %s → running", live.Status))
	}
	return changes
}

func diffSystem(desired DesiredSystem, live nixwriter.DPlaneState) []string {
	var changes []string
	if desired.Hostname != "" && desired.Hostname != live.Hostname {
		changes = append(changes, fmt.Sprintf("hostname: %q → %q", live.Hostname, desired.Hostname))
	}
	if desired.Timezone != "" && desired.Timezone != live.Timezone {
		changes = append(changes, fmt.Sprintf("timezone: %q → %q", live.Timezone, desired.Timezone))
	}
	// ... add more diffs for DNS, NTP, etc ...
	// Simple slice comparisons for brevity here
	if !equalSlices(desired.DNSServers, live.DNSServers) {
		changes = append(changes, "dns_servers changed")
	}
	if !equalSlices(desired.NTPServers, live.NTPServers) {
		changes = append(changes, "ntp_servers changed")
	}
	// Firewall
	if !equalIntSlices(desired.Firewall.TCP, live.FirewallTCP) {
		changes = append(changes, "firewall_tcp changed")
	}
	if !equalIntSlices(desired.Firewall.UDP, live.FirewallUDP) {
		changes = append(changes, "firewall_udp changed")
	}
	
	// Networking (basic check for changes)
	if len(desired.Networking.Statics) > 0 {
		changes = append(changes, "networking_statics synchronized")
	}
 
	return changes
}
 
func equalSlices(a, b []string) bool {
	if len(a) != len(b) { return false }
	for i := range a {
		if a[i] != b[i] { return false }
	}
	return true
}
 
func equalIntSlices(a, b []int) bool {
	if len(a) != len(b) { return false }
	for i := range a {
		if a[i] != b[i] { return false }
	}
	return true
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

// HumaniseBytes formats a byte count as a human-readable string.
// Mirrors the format used by ZFS output for consistency in error messages.
func HumaniseBytes(b uint64) string {
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

func diffLDAP(desired *DesiredLDAP, live *DesiredLDAP) []DiffItem {
	if live == nil {
		return []DiffItem{{
			Kind: KindLDAP, Name: "LDAP Config", Action: ActionCreate,
			DesiredLDAP: desired,
			Changes:     []string{"Enable LDAP configuration"},
		}}
	}

	var changes []string
	if desired.Enabled != live.Enabled {
		changes = append(changes, fmt.Sprintf("Enabled: %v -> %v", live.Enabled, desired.Enabled))
	}
	if desired.Server != live.Server {
		changes = append(changes, fmt.Sprintf("Server: %s -> %s", live.Server, desired.Server))
	}
	if desired.Port != live.Port {
		changes = append(changes, fmt.Sprintf("Port: %d -> %d", live.Port, desired.Port))
	}
	if desired.UseTLS != live.UseTLS {
		changes = append(changes, fmt.Sprintf("UseTLS: %v -> %v", live.UseTLS, desired.UseTLS))
	}
	if desired.BindDN != live.BindDN {
		changes = append(changes, fmt.Sprintf("BindDN: %s -> %s", live.BindDN, desired.BindDN))
	}
	if desired.BindPassword != "" && desired.BindPassword != live.BindPassword {
		changes = append(changes, "BindPassword: (changed)")
	}
	if desired.BaseDN != live.BaseDN {
		changes = append(changes, fmt.Sprintf("BaseDN: %s -> %s", live.BaseDN, desired.BaseDN))
	}

	if len(changes) == 0 {
		return nil
	}

	return []DiffItem{{
		Kind: KindLDAP, Name: "LDAP Config", Action: ActionModify,
		DesiredLDAP: desired,
		Changes:     changes,
	}}
}

