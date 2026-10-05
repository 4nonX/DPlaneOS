package configstore

import (
	"database/sql"
	"errors"
	"fmt"

	"dplaned/internal/gitops"
)

// ErrBusy is returned when another apply or rollback holds the reconcile lock.
var ErrBusy = errors.New("another reconciliation is in progress")

// RollbackResult describes a rollback.
type RollbackResult struct {
	Applied   []string          `json:"applied"`
	Blocked   []gitops.DiffItem `json:"blocked,omitempty"`
	Changeset string            `json:"changeset,omitempty"`
	NoChange  bool              `json:"no_change"`
}

// Rollback returns one resource to the state recorded in revision revID: it
// computes the GitOps plan for that resource alone and applies it, then
// records the result as a rollback revision.
//
// The GitOps engine's safety rules apply unchanged: destructive items it
// classifies as BLOCKED (a dataset with data, a share with open connections)
// are not executed; they are returned for the operator to handle.
func Rollback(db *sql.DB, ctx gitops.ApplyContext, revID int64, author string) (*RollbackResult, error) {
	rev, err := Get(db, revID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("revision %d not found", revID)
	}
	if err != nil {
		return nil, err
	}
	if !RollbackKinds[rev.Kind] {
		return nil, fmt.Errorf("%s resources cannot be rolled back from the history", rev.Kind)
	}
	if ok, reason := gitops.IsWriter(); !ok {
		return nil, fmt.Errorf("%w: %s", gitops.ErrNotWriter, reason)
	}
	if !gitops.TryLock() {
		return nil, ErrBusy
	}
	defer gitops.Unlock()

	live, err := gitops.ReadLiveState(db)
	if err != nil {
		return nil, fmt.Errorf("reading live state: %w", err)
	}
	if rev.Kind == KindUser && rev.Payload != nil && !liveHasUser(live, rev.Key) {
		return nil, fmt.Errorf("user %q was deleted; it cannot be restored from the history because passwords are not stored", rev.Key)
	}

	desired, err := Assemble([]Resource{rev.resource()})
	if err != nil {
		return nil, err
	}
	// The engine cross-checks shares and NFS exports against the declared
	// datasets' mountpoints, so declare the live datasets as context. They
	// match the live system (no plan items), and the plan is filtered to the
	// target below. A dataset target keeps its recorded version.
	for _, d := range liveToDesired(live).Datasets {
		if rev.Kind == KindDataset && d.Name == rev.Key {
			continue
		}
		desired.Datasets = append(desired.Datasets, d)
	}

	// A rollback to "deleted" needs the engine to compute a DELETE for this
	// resource: describe it as absent from a complete state, then keep only its
	// plan item below.
	deleting := rev.Payload == nil
	desired.IgnoreExtraneous = !deleting

	plan := gitops.ComputeDiff(desired, live)
	var items []gitops.DiffItem
	res := &RollbackResult{}
	for _, it := range plan.Items {
		if string(it.Kind) != rev.Kind || it.Name != rev.Key || it.Action == gitops.ActionNOP {
			continue
		}
		if it.Action == gitops.ActionBlocked || it.Action == gitops.ActionAmbiguous || it.Action == gitops.ActionManual {
			res.Blocked = append(res.Blocked, it)
			continue
		}
		items = append(items, it)
	}
	if len(res.Blocked) > 0 {
		return res, fmt.Errorf("rollback needs manual action: %s", res.Blocked[0].BlockReason)
	}
	if len(items) == 0 {
		res.NoChange = true
		return res, nil
	}

	// The engine first writes the declared resources to the database (SyncDB).
	// With ignore_extraneous it touches only this resource; a deletion has
	// nothing to declare, so that step is skipped.
	applyDesired := desired
	if deleting {
		applyDesired = nil
	}
	result, err := gitops.ApplyPlan(ctx, &gitops.Plan{Items: items}, applyDesired)
	if result != nil {
		res.Applied = result.Applied
	}
	if err != nil {
		return res, err
	}

	cs, err := Capture(db, OriginRollback, author, fmt.Sprintf("rollback of %s %q to revision %d", rev.Kind, rev.Key, rev.ID))
	if err != nil {
		return res, fmt.Errorf("rolled back, but recording the result failed: %w", err)
	}
	res.Changeset = cs
	return res, nil
}

func liveHasUser(live *gitops.LiveState, name string) bool {
	for _, u := range live.Users {
		if u.Username == name {
			return true
		}
	}
	return false
}

// Export assembles the current configuration (latest revisions) as state.yaml.
func Export(db *sql.DB) (string, error) {
	latest, err := Latest(db)
	if err != nil {
		return "", err
	}
	resources := make([]Resource, 0, len(latest))
	for _, r := range latest {
		resources = append(resources, r.resource())
	}
	ds, err := Assemble(resources)
	if err != nil {
		return "", err
	}
	return gitops.PrintStateYAML(ds), nil
}
