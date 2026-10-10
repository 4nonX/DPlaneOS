package configstore

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"reflect"
	"sync"
	"time"

	"dplaned/internal/gitops"
)

// Origins of a revision (config_revisions.origin).
const (
	OriginBaseline = "baseline" // first capture of an existing system
	OriginGUI      = "gui"      // captured right after a change in the web UI
	OriginDetected = "detected" // found by the periodic capture (shell, other tools)
	OriginRollback = "rollback" // result of a rollback from the history
	OriginPeer     = "peer"     // received from another node and applied here
	OriginMerge    = "merge"    // joins two lineages: conflict resolution or equal changes
)

// Revision is one row of config_revisions.
type Revision struct {
	ID           int64          `json:"id"`
	Changeset    string         `json:"changeset"`
	Scope        string         `json:"scope"`
	ScopeID      string         `json:"scope_id"`
	Kind         string         `json:"kind"`
	Key          string         `json:"key"`
	Payload      map[string]any `json:"payload"` // nil = deleted
	BaseRevision *int64         `json:"base_revision,omitempty"`
	UID          string         `json:"uid"`                 // global identity, same on every node
	BaseUID      string         `json:"base_uid,omitempty"`  // revision this change was made against
	MergeUID     string         `json:"merge_uid,omitempty"` // second parent of a merge
	Epoch        int64          `json:"epoch,omitempty"`     // group epoch of the writer (group scope)
	Origin       string         `json:"origin"`
	OriginNode   string         `json:"origin_node"`
	Author       string         `json:"author"`
	Note         string         `json:"note"`
	CreatedAt    time.Time      `json:"created_at"`
}

func (r Revision) resource() Resource {
	return Resource{Kind: r.Kind, Key: r.Key, Scope: r.Scope, ScopeID: r.ScopeID, Payload: r.Payload}
}

const revisionColumns = `id, changeset, scope_type, scope_id, resource_kind, resource_key, payload,
	base_revision, origin, origin_node, author, note, created_at,
	uid::text, COALESCE(base_uid::text, ''), COALESCE(merge_uid::text, ''), epoch`

func scanRevisions(rows *sql.Rows) ([]Revision, error) {
	defer rows.Close()
	var out []Revision
	for rows.Next() {
		var r Revision
		var payload []byte
		var base sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Changeset, &r.Scope, &r.ScopeID, &r.Kind, &r.Key, &payload,
			&base, &r.Origin, &r.OriginNode, &r.Author, &r.Note, &r.CreatedAt,
			&r.UID, &r.BaseUID, &r.MergeUID, &r.Epoch); err != nil {
			return nil, err
		}
		if payload != nil {
			if err := json.Unmarshal(payload, &r.Payload); err != nil {
				return nil, fmt.Errorf("revision %d payload: %w", r.ID, err)
			}
		}
		if base.Valid {
			b := base.Int64
			r.BaseRevision = &b
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Latest returns the newest revision of every resource, keyed by Resource.ID.
func Latest(db *sql.DB) (map[string]Revision, error) {
	rows, err := db.Query(`SELECT DISTINCT ON (resource_kind, resource_key) ` + revisionColumns + `
		FROM config_revisions ORDER BY resource_kind, resource_key, id DESC`)
	if err != nil {
		return nil, err
	}
	revs, err := scanRevisions(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Revision, len(revs))
	for _, r := range revs {
		out[r.resource().ID()] = r
	}
	return out, nil
}

// HistoryFilter narrows History; empty fields match everything.
type HistoryFilter struct {
	Kind   string
	Key    string
	Limit  int
	Before int64 // only revisions with id < Before (paging); 0 = newest
}

// History returns revisions newest first.
func History(db *sql.DB, f HistoryFilter) ([]Revision, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	rows, err := db.Query(`SELECT `+revisionColumns+` FROM config_revisions
		WHERE ($1 = '' OR resource_kind = $1) AND ($2 = '' OR resource_key = $2) AND ($3 = 0 OR id < $3)
		ORDER BY id DESC LIMIT $4`, f.Kind, f.Key, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	return scanRevisions(rows)
}

// Get returns one revision.
func Get(db *sql.DB, id int64) (*Revision, error) {
	rows, err := db.Query(`SELECT `+revisionColumns+` FROM config_revisions WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	revs, err := scanRevisions(rows)
	if err != nil {
		return nil, err
	}
	if len(revs) == 0 {
		return nil, sql.ErrNoRows
	}
	return &revs[0], nil
}

// normalize gives a payload the form it has after a database round trip
// (numbers as float64), so equality does not depend on where it came from.
func normalize(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return m
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return m
	}
	return out
}

func samePayload(a, b map[string]any) bool {
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func newChangeset() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// nodeName is the display name recorded as origin_node. The stable identity
// of a node is NodeID (node.go).
func nodeName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "local"
	}
	return h
}

// record inserts the given resource states as one changeset. latest provides
// the base revision of each resource.
func record(db *sql.DB, changes []Resource, latest map[string]Revision, origin, author, note string) (string, error) {
	if len(changes) == 0 {
		return "", nil
	}
	cs := newChangeset()
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	for _, c := range changes {
		var payload any
		if c.Payload != nil {
			raw, err := json.Marshal(c.Payload)
			if err != nil {
				return "", fmt.Errorf("%s: %w", c.ID(), err)
			}
			payload = raw
		}
		var base, baseUID any
		if prev, ok := latest[c.ID()]; ok {
			base, baseUID = prev.ID, prev.UID
		}
		if _, err := tx.Exec(`INSERT INTO config_revisions
			(changeset, scope_type, scope_id, resource_kind, resource_key, payload, base_revision, base_uid, origin, origin_node, author, note, epoch)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			cs, c.Scope, c.ScopeID, c.Kind, c.Key, payload, base, baseUID, origin, nodeName(), author, note, epochFor(c.Scope, c.ScopeID)); err != nil {
			return "", fmt.Errorf("%s: %w", c.ID(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	notifyChange()
	return cs, nil
}

// insertRevision records one revision with a given identity and parents: a
// revision adopted from a peer (same uid as on the peer) or a merge (new uid,
// two parents). uid "" lets the database assign one.
func insertRevision(db *sql.DB, r Revision, local *Revision) (*Revision, error) {
	var payload any
	if r.Payload != nil {
		raw, err := json.Marshal(r.Payload)
		if err != nil {
			return nil, err
		}
		payload = raw
	}
	var base any
	if local != nil {
		base = local.ID
	}
	nullUID := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	if r.Changeset == "" {
		r.Changeset = newChangeset()
	}
	if r.OriginNode == "" {
		r.OriginNode = nodeName()
	}
	if r.Epoch == 0 {
		r.Epoch = epochFor(r.Scope, r.ScopeID)
	}
	err := db.QueryRow(`INSERT INTO config_revisions
		(uid, changeset, scope_type, scope_id, resource_kind, resource_key, payload, base_revision, base_uid, merge_uid,
		 origin, origin_node, author, note, epoch)
		VALUES (COALESCE($1::uuid, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		RETURNING id, uid::text, created_at`,
		nullUID(r.UID), r.Changeset, r.Scope, r.ScopeID, r.Kind, r.Key, payload, base, nullUID(r.BaseUID), nullUID(r.MergeUID),
		r.Origin, r.OriginNode, r.Author, r.Note, r.Epoch).Scan(&r.ID, &r.UID, &r.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", r.Kind, r.Key, err)
	}
	if local != nil {
		b := local.ID
		r.BaseRevision = &b
	}
	notifyChange()
	return &r, nil
}

// EpochForPool returns the epoch of the storage group a pool belongs to (0
// when it belongs to none). Set by the daemon (internal/groups); group-scope
// revisions are stamped with it, and received ones from a lower epoch (a
// former owner) are refused.
var EpochForPool = func(pool string) int64 { return 0 }

func epochFor(scope, scopeID string) int64 {
	if scope != ScopeGroup || scopeID == "" {
		return 0
	}
	return EpochForPool(scopeID)
}

// staleWriter reports why a received group-scope revision must be refused.
func staleWriter(r Revision) string {
	if r.Scope != ScopeGroup || r.Epoch == 0 {
		return ""
	}
	if cur := epochFor(r.Scope, r.ScopeID); r.Epoch < cur {
		return fmt.Sprintf("written by a former owner of the storage group of pool %s (epoch %d, current %d)", r.ScopeID, r.Epoch, cur)
	}
	return ""
}

// changeListeners run after new revisions were recorded (peer notification).
var (
	changeListenersMu sync.Mutex
	changeListeners   []func()
)

// OnChange registers fn to run (in its own goroutine) after revisions are recorded.
func OnChange(fn func()) {
	changeListenersMu.Lock()
	defer changeListenersMu.Unlock()
	changeListeners = append(changeListeners, fn)
}

func notifyChange() {
	changeListenersMu.Lock()
	fns := append([]func(){}, changeListeners...)
	changeListenersMu.Unlock()
	for _, fn := range fns {
		go fn()
	}
}

// ── Capture ───────────────────────────────────────────────────────────────────

var captureMu sync.Mutex

// liveToDesired converts the whole live state into its state.yaml form.
// Pools are left out: the live view has no vdev topology.
func liveToDesired(live *gitops.LiveState) *gitops.DesiredState {
	cats := make([]string, 0, len(gitops.ValidCategories))
	for c := range gitops.ValidCategories {
		cats = append(cats, c)
	}
	ds := gitops.CaptureCategories(live, cats)
	for _, d := range live.Datasets {
		ds.Datasets = append(ds.Datasets, gitops.DesiredDataset{
			Name: d.Name, Quota: d.Quota, Compression: d.Compression, Atime: d.Atime,
			Sync: d.Sync, Recordsize: d.Recordsize, Xattr: d.Xattr,
			Secondarycache: d.Secondarycache, Mountpoint: d.Mountpoint, Encrypted: d.Encrypted,
		})
	}
	ds.LDAP = live.LDAP
	ds.ACME = live.ACME
	ds.Certificates = live.Certificates
	ds.SMART = live.SMART
	if len(live.NVMeFabric) > 0 {
		ds.Fabrics = &gitops.DesiredFabrics{NVMe: live.NVMeFabric}
	}
	return ds
}

// changesAgainst compares the current resources with the latest revisions:
// new or changed resources, and captured kinds that disappeared. A group-scope
// resource on a pool that is not imported here is not missing, only out of
// view (exported pool, standby of shared storage): it is not recorded as
// deleted, which would otherwise propagate to peers. imported nil = no filter.
func changesAgainst(current []Resource, latest map[string]Revision, imported map[string]bool) []Resource {
	var changes []Resource
	seen := make(map[string]bool, len(current))
	for _, r := range current {
		seen[r.ID()] = true
		prev, ok := latest[r.ID()]
		// A changed scope is a change too (e.g. a stack whose volumes moved to a pool).
		if !ok || !samePayload(prev.Payload, r.Payload) || prev.Scope != r.Scope || prev.ScopeID != r.ScopeID {
			changes = append(changes, r)
		}
	}
	for id, prev := range latest {
		if !seen[id] && capturedKinds[prev.Kind] && prev.Payload != nil {
			if imported != nil && prev.Scope == ScopeGroup && !imported[prev.ScopeID] {
				continue
			}
			gone := prev.resource()
			gone.Payload = nil
			changes = append(changes, gone)
		}
	}
	return changes
}

// Capture records every change between the live configuration and the latest
// revisions as one changeset. It runs only on the GitOps writer (in an HA pair
// both nodes share the database). Returns the changeset id ("" = no change).
func Capture(db *sql.DB, origin, author, note string) (string, error) {
	if ok, _ := gitops.IsWriter(); !ok {
		return "", nil
	}
	captureMu.Lock()
	defer captureMu.Unlock()
	return captureLocked(db, origin, author, note)
}

// captureLocked is Capture with captureMu held by the caller.
func captureLocked(db *sql.DB, origin, author, note string) (string, error) {
	live, err := gitops.ReadLiveState(db)
	if err != nil {
		return "", fmt.Errorf("reading live state: %w", err)
	}
	latest, err := Latest(db)
	if err != nil {
		return "", fmt.Errorf("reading revisions: %w", err)
	}
	if len(latest) == 0 {
		origin, note = OriginBaseline, "configuration when history recording started"
	}
	current := Extract(liveToDesired(live), nodeName())
	secretFingerprints(db, current)
	changes := changesAgainst(current, latest, importedPools(live))
	return record(db, changes, latest, origin, author, note)
}

func importedPools(live *gitops.LiveState) map[string]bool {
	out := make(map[string]bool, len(live.Pools))
	for _, p := range live.Pools {
		out[p.Name] = true
	}
	return out
}

// StartPeriodicCapture records changes made outside the web UI (shell, other
// tools) and is the first baseline after an upgrade.
func StartPeriodicCapture(db *sql.DB, interval time.Duration) {
	go func() {
		run := func() {
			if _, err := Capture(db, OriginDetected, "", ""); err != nil {
				log.Printf("CONFIG HISTORY: capture failed: %v", err)
			}
		}
		run()
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			run()
		}
	}()
}
