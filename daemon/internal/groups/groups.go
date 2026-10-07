// Package groups implements storage groups and their epochs (Design 0001
// phase 3b, ADR-0003).
//
// A storage group is the unit of ownership and failover: one or more pools,
// the nodes that may own them (candidates, in priority order), the topology,
// the current owner and the epoch. Every member keeps a copy. The epoch
// increases with every change of owner and works as a fencing token: a node
// whose epoch is lower than the group's must not write, and a node that
// learns it is no longer the owner releases the pools.
package groups

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Topologies (design section 5.3).
const (
	Standalone = "standalone" // one candidate
	Shared     = "shared"     // candidates see the same disks
	Replicated = "replicated" // candidates hold copies (ZFS send/receive)
)

// PoolRef identifies a pool; the GUID is used to import it.
type PoolRef struct {
	Name string `json:"name"`
	GUID string `json:"guid"`
}

// Group is one storage group.
type Group struct {
	Name       string    `json:"name"`
	Topology   string    `json:"topology"`
	Pools      []PoolRef `json:"pools"`
	Candidates []string  `json:"candidates"` // node keys, priority order
	Owner      string    `json:"owner"`
	Epoch      int64     `json:"epoch"`
	Version    int64     `json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
	UpdatedBy  string    `json:"updated_by"`

	// Replicated groups: replication interval, and whether the group may fail
	// over automatically (data written since the last replication is lost).
	IntervalSecs int  `json:"interval_secs"`
	AutoFailover bool `json:"auto_failover"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var poolRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,254}$`)
var guidRe = regexp.MustCompile(`^[0-9]{1,20}$`)

// Validate checks a group definition.
func (g Group) Validate() error {
	if !nameRe.MatchString(g.Name) {
		return fmt.Errorf("invalid group name %q", g.Name)
	}
	switch g.Topology {
	case Standalone, Shared, Replicated:
	default:
		return fmt.Errorf("invalid topology %q", g.Topology)
	}
	if len(g.Pools) == 0 {
		return errors.New("a group needs at least one pool")
	}
	seen := map[string]bool{}
	for _, p := range g.Pools {
		// Each node of a replicated group has its own copy (own GUID).
		guidOK := guidRe.MatchString(p.GUID) || (g.Topology == Replicated && p.GUID == "")
		if !poolRe.MatchString(p.Name) || !guidOK {
			return fmt.Errorf("invalid pool %q (guid %q)", p.Name, p.GUID)
		}
		if seen[p.Name] {
			return fmt.Errorf("pool %s listed twice", p.Name)
		}
		seen[p.Name] = true
	}
	if len(g.Candidates) == 0 {
		return errors.New("a group needs at least one candidate node")
	}
	if g.Topology == Standalone && len(g.Candidates) != 1 {
		return errors.New("a standalone group has exactly one node")
	}
	if g.Topology != Standalone && len(g.Candidates) < 2 {
		return errors.New("shared and replicated groups need at least two candidate nodes")
	}
	if !g.IsCandidate(g.Owner) {
		return errors.New("the owner must be one of the candidates")
	}
	if g.Epoch < 1 || g.Version < 1 {
		return errors.New("epoch and version start at 1")
	}
	return nil
}

// IsCandidate reports whether node may own the group.
func (g Group) IsCandidate(node string) bool {
	for _, c := range g.Candidates {
		if c == node {
			return true
		}
	}
	return false
}

// HasPool reports whether the group contains pool.
func (g Group) HasPool(pool string) bool {
	for _, p := range g.Pools {
		if p.Name == pool {
			return true
		}
	}
	return false
}

// Newer reports whether g supersedes other: a higher epoch always wins
// (ownership), then a higher version (definition edits).
func (g Group) Newer(other Group) bool {
	if g.Epoch != other.Epoch {
		return g.Epoch > other.Epoch
	}
	return g.Version > other.Version
}

// ── Decision ──────────────────────────────────────────────────────────────────

// Role of this node for a group.
type Role string

const (
	RoleOwner   Role = "owner"   // owns it and may serve and write
	RoleStandby Role = "standby" // candidate, not owner
	RoleOther   Role = "other"   // not a candidate
)

// View is what this node knows when deciding.
type View struct {
	Self         string          // this node's key
	Quorum       bool            // cluster quorum configured
	Quorate      bool            // this node is in the quorate partition
	ImportedHere map[string]bool // pools imported on this node

	// Failover inputs (phase 3c).
	Online             []string      // node keys in this node's partition
	AutoFailover       bool          // quorum allows automatic failover (three votes, quorate)
	AutoFailoverReason string        // why not
	FenceOK            bool          // a fencing method is configured (watchdog or power fencing)
	FenceReason        string        // why not
	FenceDelay         time.Duration // wait after the owner left before taking over
}

// Status is the evaluated state of a group on this node.
type Status struct {
	Role     Role     `json:"role"`
	CanWrite bool     `json:"can_write"`         // may serve and write group data and configuration
	Problems []string `json:"problems"`          // shown in the GUI
	Release  []string `json:"release,omitempty"` // pools this node must export
}

// Evaluate decides this node's role and what is wrong, without side effects.
//
//   - The owner may write only while it has quorum (when a cluster is
//     configured); otherwise it must stop (its watchdog resets it).
//   - A node that is not the owner must not have the group's pools imported
//     (shared storage): it releases them. On replicated groups the standby
//     holds its copy imported (as replication target), which is expected.
func Evaluate(g Group, v View) Status {
	st := Status{Role: RoleOther, Problems: []string{}}
	switch {
	case g.Owner == v.Self:
		st.Role = RoleOwner
	case g.IsCandidate(v.Self):
		st.Role = RoleStandby
	}
	var missing []string
	for _, p := range g.Pools {
		imported := v.ImportedHere[p.Name]
		switch {
		case st.Role == RoleOwner && !imported:
			missing = append(missing, p.Name)
		case st.Role != RoleOwner && imported && g.Topology == Shared:
			st.Release = append(st.Release, p.Name)
		}
	}
	if st.Role == RoleOwner {
		st.CanWrite = len(missing) == 0 && (!v.Quorum || v.Quorate)
		if len(missing) > 0 {
			st.Problems = append(st.Problems, fmt.Sprintf("this node owns the group but these pools are not imported: %v", missing))
		}
		if v.Quorum && !v.Quorate {
			st.Problems = append(st.Problems, "this node owns the group but has no quorum; it must not write")
		}
	}
	if len(st.Release) > 0 {
		st.Problems = append(st.Problems, fmt.Sprintf("this node is not the owner but has these shared pools imported: %v", st.Release))
	}
	return st
}

// ── Persistence ───────────────────────────────────────────────────────────────

func scan(rows *sql.Rows) ([]Group, error) {
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		var pools, cands []byte
		if err := rows.Scan(&g.Name, &g.Topology, &pools, &cands, &g.Owner, &g.Epoch, &g.Version, &g.UpdatedAt, &g.UpdatedBy,
			&g.IntervalSecs, &g.AutoFailover); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(pools, &g.Pools); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(cands, &g.Candidates); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

const cols = `name, topology, pools, candidates, owner, epoch, version, updated_at, updated_by, interval_secs, auto_failover`

// List returns all groups.
func List(db *sql.DB) ([]Group, error) {
	rows, err := db.Query(`SELECT ` + cols + ` FROM storage_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	gs, err := scan(rows)
	if gs == nil {
		gs = []Group{}
	}
	return gs, err
}

// Get returns one group (sql.ErrNoRows if unknown).
func Get(db *sql.DB, name string) (*Group, error) {
	rows, err := db.Query(`SELECT `+cols+` FROM storage_groups WHERE name = $1`, name)
	if err != nil {
		return nil, err
	}
	gs, err := scan(rows)
	if err != nil {
		return nil, err
	}
	if len(gs) == 0 {
		return nil, sql.ErrNoRows
	}
	return &gs[0], nil
}

// ErrStale: the update is not newer than the stored group.
var ErrStale = errors.New("stale group update")

// Accept stores g if it is newer than the stored copy (or unknown).
func Accept(db *sql.DB, g Group) error {
	if err := g.Validate(); err != nil {
		return err
	}
	cur, err := Get(db, g.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cur != nil && !g.Newer(*cur) {
		return fmt.Errorf("%w: %s epoch %d/%d, stored %d/%d", ErrStale, g.Name, g.Epoch, g.Version, cur.Epoch, cur.Version)
	}
	return put(db, g)
}

func put(db *sql.DB, g Group) error {
	pools, _ := json.Marshal(g.Pools)
	cands, _ := json.Marshal(g.Candidates)
	if g.UpdatedAt.IsZero() {
		g.UpdatedAt = time.Now()
	}
	if g.IntervalSecs <= 0 {
		g.IntervalSecs = 300
	}
	_, err := db.Exec(`INSERT INTO storage_groups (`+cols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (name) DO UPDATE SET topology = EXCLUDED.topology, pools = EXCLUDED.pools,
			candidates = EXCLUDED.candidates, owner = EXCLUDED.owner, epoch = EXCLUDED.epoch,
			version = EXCLUDED.version, updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by,
			interval_secs = EXCLUDED.interval_secs, auto_failover = EXCLUDED.auto_failover`,
		g.Name, g.Topology, pools, cands, g.Owner, g.Epoch, g.Version, g.UpdatedAt, g.UpdatedBy, g.IntervalSecs, g.AutoFailover)
	return err
}

// Remove deletes a group definition (pools are not touched).
func Remove(db *sql.DB, name string) error {
	_, err := db.Exec(`DELETE FROM storage_groups WHERE name = $1`, name)
	return err
}

// ForPool returns the group containing pool (nil if none).
func ForPool(db *sql.DB, pool string) (*Group, error) {
	gs, err := List(db)
	if err != nil {
		return nil, err
	}
	for _, g := range gs {
		if g.HasPool(pool) {
			g := g
			return &g, nil
		}
	}
	return nil, nil
}

// EpochForPool returns the epoch of the group containing pool (0 if none).
// Used to stamp and check group-scope configuration revisions.
func EpochForPool(db *sql.DB, pool string) int64 {
	g, err := ForPool(db, pool)
	if err != nil || g == nil {
		return 0
	}
	return g.Epoch
}
