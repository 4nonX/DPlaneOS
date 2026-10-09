package groups

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// Importing shared pools on the owner (Design 0001 phase 3e).
//
// The pools of a shared group are imported with cachefile=none, so the boot
// never imports them (on shared disks the boot cannot know which node owns
// them). The owner's daemon imports them instead, once it is safe:
//
//   - every other candidate answered and does not have them imported, or
//   - the candidates that do not answer are outside this node's quorate
//     partition and the fencing delay has passed (their watchdog reset them).
//
// Otherwise the group's line says what the node waits for; Import here
// (Takeover on the owner) imports on request.

// PeerView is what a member reports: its groups and its imported pools.
type PeerView struct {
	Groups      []Group
	Imported    []string
	HasImported bool // false: an older version that does not report pools
}

type peerSeen struct {
	Reached  bool
	Imported map[string]bool // nil: not reported
}

// planOwnerImport decides whether the owner imports the pools of g now.
// absentSince is when each candidate was first seen not answering.
func planOwnerImport(g Group, v View, peers map[string]peerSeen, absentSince map[string]time.Time, now time.Time) Decision {
	if g.Topology != Shared || g.Owner != v.Self {
		return Decision{Action: "none"}
	}
	missing := false
	for _, p := range g.Pools {
		if !v.ImportedHere[p.Name] {
			missing = true
		}
	}
	if !missing {
		return Decision{Action: "none"}
	}
	if v.Quorum && !v.Quorate {
		return Decision{Action: "none", Reason: "this node owns the group but is not in the quorate partition; it imports the pools once it is (Import here imports them now if the other nodes are off)"}
	}
	var silent []string
	for _, c := range g.Candidates {
		if c == v.Self {
			continue
		}
		ps := peers[c]
		if !ps.Reached || ps.Imported == nil {
			silent = append(silent, c)
			continue
		}
		for _, p := range g.Pools {
			if ps.Imported[p.Name] {
				return Decision{Action: "none", Reason: fmt.Sprintf("%s has pool %s imported although this node owns the group; export it there, or take the group over there", c, p.Name)}
			}
		}
	}
	if len(silent) == 0 {
		return Decision{Action: "import", Reason: "the other candidates confirmed they do not use the pools"}
	}
	names := strings.Join(silent, ", ")
	if !v.Quorum || !v.FenceOK {
		return Decision{Action: "none", Reason: fmt.Sprintf("waiting for %s before importing: it may be using the pools. If it is off, use Import here", names)}
	}
	var latest time.Time
	for _, c := range silent {
		if contains(v.Online, c) {
			return Decision{Action: "none", Reason: fmt.Sprintf("%s is in the cluster but does not answer; waiting before importing", c)}
		}
		since := absentSince[c]
		if since.IsZero() {
			since = now
		}
		if since.After(latest) {
			latest = since
		}
	}
	if until := latest.Add(v.FenceDelay); now.Before(until) {
		return Decision{Action: "wait", WaitUntil: until,
			Reason: fmt.Sprintf("importing at %s, after the watchdog of %s has reset it", until.Format("15:04:05"), names)}
	}
	return Decision{Action: "import", Reason: fmt.Sprintf("%s left the quorate partition and the fencing delay has passed", names)}
}

// reconcileOwned imports the pools of shared groups this node owns where
// planOwnerImport allows it, and keeps them out of the boot-time cache.
func (m *Manager) reconcileOwned(peers map[string]peerSeen) {
	gs, err := List(m.db)
	if err != nil {
		return
	}
	v, err := m.currentView()
	if err != nil {
		return
	}
	now := time.Now()
	m.fmu.Lock()
	if m.absentSince == nil {
		m.absentSince = map[string]time.Time{}
	}
	for n, ps := range peers {
		if ps.Reached {
			delete(m.absentSince, n)
		} else if _, ok := m.absentSince[n]; !ok {
			m.absentSince[n] = now
		}
	}
	absent := map[string]time.Time{}
	for k, t := range m.absentSince {
		absent[k] = t
	}
	m.fmu.Unlock()

	for _, g := range gs {
		if g.Topology != Shared || g.Owner != v.Self {
			continue
		}
		for _, p := range g.Pools {
			if v.ImportedHere[p.Name] {
				m.uncache(p.Name)
			}
		}
		d := planOwnerImport(g, v, peers, absent, now)
		m.fmu.Lock()
		if m.ownerImport == nil {
			m.ownerImport = map[string]Decision{}
		}
		m.ownerImport[g.Name] = d
		m.fmu.Unlock()
		if d.Action != "import" {
			continue
		}
		log.Printf("GROUPS: %s: importing the pools of this node's group: %s", g.Name, d.Reason)
		if err := m.importOwned(g); err != nil {
			log.Printf("GROUPS: %s: %v", g.Name, err)
			m.fmu.Lock()
			m.ownerImport[g.Name] = Decision{Action: "none", Reason: err.Error()}
			m.fmu.Unlock()
			continue
		}
		m.fmu.Lock()
		delete(m.ownerImport, g.Name)
		m.fmu.Unlock()
		go m.ActivateTick()
	}
}

// importOwned imports the missing pools of g here (this node is the owner).
func (m *Manager) importOwned(g Group) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, err := Get(m.db, g.Name)
	if err != nil {
		return err
	}
	if cur.Owner != m.self() {
		return fmt.Errorf("group %s is now owned by %s", g.Name, cur.Owner)
	}
	imported, err := m.ops.ImportedPools()
	if err != nil {
		return err
	}
	for _, p := range cur.Pools {
		if _, ok := imported[p.Name]; ok {
			continue
		}
		// -f: the pool was last used by another node (a takeover) or not
		// exported (a crash). ZFS multihost still refuses a pool that is
		// active elsewhere.
		if err := m.ops.ImportForce(p.GUID); err != nil {
			return fmt.Errorf("importing %s: %w", p.Name, err)
		}
		log.Printf("GROUPS: %s: imported pool %s", g.Name, p.Name)
	}
	return nil
}

// uncache keeps a shared pool out of the boot-time import (once per run).
func (m *Manager) uncache(pool string) {
	m.fmu.Lock()
	if m.uncached == nil {
		m.uncached = map[string]bool{}
	}
	done := m.uncached[pool]
	m.uncached[pool] = true
	m.fmu.Unlock()
	if done || m.ops.Uncache == nil {
		return
	}
	if err := m.ops.Uncache(pool); err != nil {
		log.Printf("GROUPS: %s: %v", pool, err)
		m.fmu.Lock()
		delete(m.uncached, pool)
		m.fmu.Unlock()
	}
}

func (m *Manager) ownerImportDecision(name string) Decision {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	return m.ownerImport[name]
}
