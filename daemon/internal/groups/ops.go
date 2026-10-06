package groups

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/libzfs"
)

// Ops are the storage side effects; replaced in tests.
type Ops struct {
	// ImportedPools returns pool name → GUID of the pools imported here.
	ImportedPools func() (map[string]string, error)
	Import        func(guid string) error
	// ImportForce imports a pool the previous owner did not export (failover).
	ImportForce func(guid string) error
	Export      func(name string) error
}

// DefaultOps use zpool through the command whitelist.
var DefaultOps = Ops{
	ImportedPools: func() (map[string]string, error) {
		out, err := cmdutil.RunFast("zpool_list_guid", "list", "-H", "-o", "name,guid")
		if err != nil {
			return nil, fmt.Errorf("listing pools: %v: %s", err, bytes.TrimSpace(out))
		}
		m := map[string]string{}
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if f := strings.Fields(l); len(f) == 2 {
				m[f[0]] = f[1]
			}
		}
		return m, nil
	},
	Import: func(guid string) error {
		rescanDisks()
		out, err := cmdutil.RunSlow("zpool_import", "import", "-d", "/dev/disk/by-id", guid)
		if err != nil {
			return fmt.Errorf("zpool import %s: %v: %s", guid, err, bytes.TrimSpace(out))
		}
		return nil
	},
	ImportForce: func(guid string) error {
		rescanDisks()
		out, err := cmdutil.RunSlow("zpool_import", "import", "-d", "/dev/disk/by-id", "-f", guid)
		if err != nil {
			return fmt.Errorf("zpool import -f %s: %v: %s", guid, err, bytes.TrimSpace(out))
		}
		return nil
	},
	Export: func(name string) error { return libzfs.PoolExport(name, false) },
}

// rescanDisks prepares an import of a pool another node used: it drops the
// kernel's cached blocks of the disks and re-reads their partition tables,
// then waits for udev. On shared storage this node may have read a disk
// before the other node created the pool on it (ZFS partitions whole disks):
// without the re-read, the partition holding the ZFS labels does not exist
// here and the import reports "no such pool available" (VM test). Disks in
// use (the system disk) refuse the re-read, which is harmless.
func rescanDisks() {
	entries, err := os.ReadDir("/dev/disk/by-id")
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if strings.Contains(e.Name(), "-part") {
			continue
		}
		dev, err := filepath.EvalSymlinks(filepath.Join("/dev/disk/by-id", e.Name()))
		if err != nil || seen[dev] || !diskRe.MatchString(dev) {
			continue
		}
		seen[dev] = true
		if out, err := cmdutil.RunFast("blockdev_flushbufs", "--flushbufs", dev); err != nil {
			log.Printf("GROUPS: flushing %s before import: %v: %s", dev, err, bytes.TrimSpace(out))
		}
		_, _ = cmdutil.RunFast("blockdev_rereadpt", "--rereadpt", dev)
	}
	if out, err := cmdutil.RunMedium("udevadm_settle", "settle", "--timeout=15"); err != nil {
		log.Printf("GROUPS: udevadm settle: %v: %s", err, bytes.TrimSpace(out))
	}
}

// diskRe matches whole-disk device nodes (no partitions, no optical drives).
var diskRe = regexp.MustCompile(`^/dev/(sd[a-z]+|vd[a-z]+|xvd[a-z]+|nvme[0-9]+n[0-9]+|dm-[0-9]+)$`)

// Update is sent between members.
type Update struct {
	Group  Group `json:"group"`
	Remove bool  `json:"remove,omitempty"`
}

// Transport reaches the other members (paired-node channel).
type Transport interface {
	Push(node string, u Update) error
	Fetch(node string) ([]Group, error)
	Members() []string // node keys of the cluster members (self included)
}

// Manager runs group operations for this node.
type Manager struct {
	db   *sql.DB
	ops  Ops
	tr   Transport
	self func() string
	view func() View
	mu   sync.Mutex

	// Fence powers off the previous owner before an automatic takeover (IPMI
	// or PDU, when configured); nil = watchdog only. Set before Start.
	Fence func(owner string) error
	// WatchdogFencing: the watchdog baseline is active, so a failed power
	// fence does not stop the takeover (the old owner resets itself).
	WatchdogFencing func() bool

	fmu       sync.Mutex // failover state
	lostSince map[string]time.Time
	decisions map[string]Decision
}

// NewManager wires the manager. view returns the current quorum view.
func NewManager(db *sql.DB, ops Ops, tr Transport, self func() string, view func() View) *Manager {
	return &Manager{db: db, ops: ops, tr: tr, self: self, view: view}
}

func (m *Manager) currentView() (View, error) {
	v := m.view()
	v.Self = m.self()
	imported, err := m.ops.ImportedPools()
	if err != nil {
		return v, err
	}
	v.ImportedHere = map[string]bool{}
	for name := range imported {
		v.ImportedHere[name] = true
	}
	return v, nil
}

// GroupStatus is a group with this node's evaluation.
type GroupStatus struct {
	Group
	Status
	Failover Decision `json:"failover"`
}

// Statuses evaluates every group on this node.
func (m *Manager) Statuses() ([]GroupStatus, error) {
	gs, err := List(m.db)
	if err != nil {
		return nil, err
	}
	v, err := m.currentView()
	if err != nil {
		return nil, err
	}
	out := make([]GroupStatus, 0, len(gs))
	for _, g := range gs {
		out = append(out, GroupStatus{Group: g, Status: Evaluate(g, v), Failover: m.decision(g.Name)})
	}
	return out, nil
}

// CanWritePool reports whether this node may write data and configuration
// of pool (pools outside any group: always).
func (m *Manager) CanWritePool(pool string) (bool, string) {
	g, err := ForPool(m.db, pool)
	if err != nil {
		return false, err.Error()
	}
	if g == nil {
		return true, ""
	}
	v, err := m.currentView()
	if err != nil {
		return false, err.Error()
	}
	st := Evaluate(*g, v)
	if st.CanWrite {
		return true, ""
	}
	if st.Role != RoleOwner {
		return false, fmt.Sprintf("pool %s belongs to group %s, owned by another node (epoch %d)", pool, g.Name, g.Epoch)
	}
	return false, strings.Join(st.Problems, "; ")
}

func (m *Manager) pushAll(g Group, skip ...string) []error {
	var errs []error
	for _, n := range g.Candidates {
		if n == m.self() || contains(skip, n) {
			continue
		}
		if err := m.tr.Push(n, Update{Group: g}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", n, err))
		}
	}
	return errs
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

// Create defines a group owned by this node. Its pools must be imported here.
func (m *Manager) Create(name, topology string, pools []string, candidates []string) (*Group, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	self := m.self()
	if _, err := Get(m.db, name); err == nil {
		return nil, fmt.Errorf("group %s exists", name)
	}
	imported, err := m.ops.ImportedPools()
	if err != nil {
		return nil, err
	}
	g := Group{Name: name, Topology: topology, Owner: self, Epoch: 1, Version: 1, UpdatedAt: time.Now(), UpdatedBy: self}
	for _, p := range pools {
		guid, ok := imported[p]
		if !ok {
			return nil, fmt.Errorf("pool %s is not imported on this node", p)
		}
		if other, _ := ForPool(m.db, p); other != nil {
			return nil, fmt.Errorf("pool %s already belongs to group %s", p, other.Name)
		}
		g.Pools = append(g.Pools, PoolRef{Name: p, GUID: guid})
	}
	g.Candidates = append([]string{self}, without(candidates, self)...)
	if err := g.Validate(); err != nil {
		return nil, err
	}
	if g.Topology != Standalone {
		members := m.tr.Members()
		for _, c := range g.Candidates {
			if !contains(members, c) {
				return nil, fmt.Errorf("node %s is not a member of this node's cluster", c)
			}
		}
	}
	if err := put(m.db, g); err != nil {
		return nil, err
	}
	if errs := m.pushAll(g); len(errs) > 0 {
		return &g, fmt.Errorf("group created here, but not on every member yet (retried automatically): %w", errors.Join(errs...))
	}
	return &g, nil
}

func without(s []string, x string) []string {
	var out []string
	for _, v := range s {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

// MoveResult reports a planned move.
type MoveResult struct {
	Group   *Group   `json:"group"`
	Warning []string `json:"warnings,omitempty"`
}

// Move hands a shared-storage group to another candidate: export here,
// increase the epoch, the target imports. If the target cannot import, this
// node takes the pools back and the group stays where it was.
func (m *Manager) Move(name, target string) (*MoveResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	self := m.self()
	g, err := Get(m.db, name)
	if err != nil {
		return nil, fmt.Errorf("group %s: %w", name, err)
	}
	if g.Owner != self {
		return nil, errors.New("only the owner can move the group; open the owner's interface")
	}
	if target == self || !g.IsCandidate(target) {
		return nil, errors.New("the target must be another candidate of the group")
	}
	switch g.Topology {
	case Shared:
	case Replicated:
		return nil, errors.New("planned moves of replicated groups (final send, then direction flip) are not available yet")
	default:
		return nil, errors.New("a standalone group has no other node")
	}
	if v := m.view(); v.Quorum && !v.Quorate {
		return nil, errors.New("this node has no quorum")
	}

	// 1. Release the pools here.
	var exported []PoolRef
	for _, p := range g.Pools {
		if err := m.ops.Export(p.Name); err != nil {
			m.reimport(exported)
			return nil, fmt.Errorf("exporting %s: %w (the group stays here)", p.Name, err)
		}
		exported = append(exported, p)
	}
	// 2. New epoch, the target imports.
	ng := *g
	ng.Owner, ng.Epoch, ng.UpdatedAt, ng.UpdatedBy = target, g.Epoch+1, time.Now(), self
	if err := m.tr.Push(target, Update{Group: ng}); err != nil {
		if rerr := m.reimport(exported); rerr != nil {
			return nil, fmt.Errorf("the target did not take over (%v) and re-importing here failed: %v. The pools are imported nowhere: import them on one node", err, rerr)
		}
		return nil, fmt.Errorf("the target did not take over: %w (the group stays here)", err)
	}
	if err := put(m.db, ng); err != nil {
		return nil, err
	}
	res := &MoveResult{Group: &ng}
	for _, e := range m.pushAll(ng, target) {
		res.Warning = append(res.Warning, e.Error()+" (retried automatically)")
	}
	log.Printf("GROUPS: %s moved to %s (epoch %d)", name, target, ng.Epoch)
	return res, nil
}

func (m *Manager) reimport(pools []PoolRef) error {
	var errs []error
	for _, p := range pools {
		if err := m.ops.Import(p.GUID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Receive applies an update from another member. If this node becomes the
// owner it imports the pools; if it was the owner and is not any more (it
// missed the change, e.g. during a partition) it releases them.
func (m *Manager) Receive(u Update) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u.Remove {
		cur, err := Get(m.db, u.Group.Name)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if u.Group.Epoch < cur.Epoch {
			return fmt.Errorf("%w: removal at epoch %d, stored %d", ErrStale, u.Group.Epoch, cur.Epoch)
		}
		return Remove(m.db, u.Group.Name)
	}
	return m.adopt(u.Group)
}

func (m *Manager) adopt(g Group) error {
	cur, err := Get(m.db, g.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cur != nil && !g.Newer(*cur) {
		if cur.Epoch == g.Epoch && cur.Version == g.Version {
			return nil // already known
		}
		return fmt.Errorf("%w: %s epoch %d/%d, stored %d/%d", ErrStale, g.Name, g.Epoch, g.Version, cur.Epoch, cur.Version)
	}
	if err := g.Validate(); err != nil {
		return err
	}
	imported, err := m.ops.ImportedPools()
	if err != nil {
		return err
	}
	imports, exports := planAdopt(g, m.self(), imported)
	// Planned move to this node: import first; refuse (no state change) if
	// that fails, so the sender takes the pools back.
	for _, p := range imports {
		if err := m.ops.Import(p.GUID); err != nil {
			return fmt.Errorf("importing %s: %w", p.Name, err)
		}
	}
	// Stale owner: give the shared pools up.
	var errs []error
	for _, name := range exports {
		log.Printf("GROUPS: %s is owned by %s at epoch %d; releasing %s here", g.Name, g.Owner, g.Epoch, name)
		if err := m.ops.Export(name); err != nil {
			errs = append(errs, fmt.Errorf("releasing %s: %w", name, err))
		}
	}
	if err := put(m.db, g); err != nil {
		return err
	}
	return errors.Join(errs...)
}

// planAdopt decides what adopting g means for the pools on this node: import
// them when this node becomes the owner, release shared pools when another
// node owns the group.
func planAdopt(g Group, self string, imported map[string]string) (imports []PoolRef, exports []string) {
	for _, p := range g.Pools {
		_, here := imported[p.Name]
		switch {
		case g.Owner == self && !here:
			imports = append(imports, p)
		case g.Owner != self && here && g.Topology == Shared:
			exports = append(exports, p.Name)
		}
	}
	return imports, exports
}

// Remove deletes a group everywhere (pools are not touched).
func (m *Manager) RemoveGroup(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := Get(m.db, name)
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range g.Candidates {
		if n != m.self() {
			if err := m.tr.Push(n, Update{Group: *g, Remove: true}); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w (remove it there too)", n, err))
			}
		}
	}
	if err := Remove(m.db, name); err != nil {
		return err
	}
	return errors.Join(errs...)
}

// SyncOnce pulls the groups of every member and adopts newer ones. This is
// how a node that missed a change (partition, restart) learns it.
func (m *Manager) SyncOnce() error {
	var errs []error
	for _, n := range m.tr.Members() {
		if n == m.self() {
			continue
		}
		gs, err := m.tr.Fetch(n)
		if err != nil {
			continue // unreachable members are normal during a partition
		}
		for _, g := range gs {
			m.mu.Lock()
			err := m.adopt(g)
			m.mu.Unlock()
			if err != nil && !errors.Is(err, ErrStale) {
				errs = append(errs, fmt.Errorf("%s from %s: %w", g.Name, n, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Start pulls from the members every interval.
func (m *Manager) Start(interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			if err := m.SyncOnce(); err != nil {
				log.Printf("GROUPS: sync: %v", err)
			}
		}
	}()
}
