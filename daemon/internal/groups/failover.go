package groups

import (
	"errors"
	"fmt"
	"log"
	"time"
)

// Automatic failover per storage group and manual takeover (Design 0001
// phase 3c, ADR-0009).
//
// When the owner leaves this node's quorate partition, the first candidate
// (in priority order) that is still in the partition waits the fencing delay
// (watchdog timeout plus a margin: by then the old owner, which lost quorum,
// has been reset by its watchdog) and takes the group over: epoch + 1,
// import with -f. ZFS multihost still refuses the import if the old owner is
// in fact still writing, so a wrong decision cannot corrupt the pool.

// Decision of the failover engine for one group on this node.
type Decision struct {
	Action    string    `json:"action"` // none, wait, takeover
	Reason    string    `json:"reason,omitempty"`
	WaitUntil time.Time `json:"wait_until,omitempty"`
}

func contains2(s []string, x string) bool { return contains(s, x) }

// planFailover decides, without side effects, whether this node should take
// over g. lostSince is when the owner was first seen missing (zero: now).
func planFailover(g Group, v View, lostSince, now time.Time) Decision {
	if g.Owner == v.Self || !g.IsCandidate(v.Self) || !v.Quorum {
		return Decision{Action: "none"}
	}
	if contains2(v.Online, g.Owner) {
		return Decision{Action: "none"}
	}
	switch {
	case g.Topology == Standalone:
		return Decision{Action: "none"}
	case g.Topology == Replicated && !g.AutoFailover:
		return Decision{Action: "none", Reason: "the owner is not reachable; automatic failover is off for this replicated group (changes since the last replication would be lost). Take over manually after making sure the owner is off"}
	case !v.AutoFailover:
		return Decision{Action: "none", Reason: "the owner is not reachable; automatic failover is off: " + v.AutoFailoverReason + ". Take over manually after making sure the owner is off"}
	case !v.FenceOK:
		return Decision{Action: "none", Reason: "the owner is not reachable; automatic failover needs a fencing method: " + v.FenceReason}
	}
	for _, c := range g.Candidates {
		if c == g.Owner || !contains2(v.Online, c) {
			continue
		}
		if c != v.Self {
			return Decision{Action: "none", Reason: "the owner is not reachable; another candidate with higher priority takes over"}
		}
		break
	}
	if lostSince.IsZero() {
		lostSince = now
	}
	if until := lostSince.Add(v.FenceDelay); now.Before(until) {
		return Decision{Action: "wait", WaitUntil: until,
			Reason: fmt.Sprintf("the owner is not reachable; taking over at %s, after its watchdog has reset it", until.Format("15:04:05"))}
	}
	return Decision{Action: "takeover", Reason: "the owner left the quorate partition and the fencing delay has passed"}
}

// FailoverTick evaluates every group and takes over where planFailover says
// so. Run periodically.
func (m *Manager) FailoverTick() {
	gs, err := List(m.db)
	if err != nil {
		log.Printf("GROUPS: failover: %v", err)
		return
	}
	v, err := m.currentView()
	if err != nil {
		log.Printf("GROUPS: failover: %v", err)
		return
	}
	now := time.Now()
	m.fmu.Lock()
	if m.lostSince == nil {
		m.lostSince = map[string]time.Time{}
		m.decisions = map[string]Decision{}
	}
	var todo []Group
	for _, g := range gs {
		if g.Owner == v.Self || contains2(v.Online, g.Owner) || !v.Quorum {
			delete(m.lostSince, g.Name)
		} else if _, ok := m.lostSince[g.Name]; !ok {
			m.lostSince[g.Name] = now
			log.Printf("GROUPS: owner of %s left this partition", g.Name)
		}
		d := planFailover(g, v, m.lostSince[g.Name], now)
		m.decisions[g.Name] = d
		if d.Action == "takeover" {
			todo = append(todo, g)
		}
	}
	m.fmu.Unlock()
	for _, g := range todo {
		if m.Fence != nil {
			if err := m.Fence(g.Owner); err != nil {
				if m.WatchdogFencing == nil || !m.WatchdogFencing() {
					log.Printf("GROUPS: %s: fencing the previous owner failed and no watchdog fencing: not taking over: %v", g.Name, err)
					m.fmu.Lock()
					m.decisions[g.Name] = Decision{Action: "none", Reason: "power fencing of the previous owner failed: " + err.Error()}
					m.fmu.Unlock()
					continue
				}
				log.Printf("GROUPS: %s: power fencing failed (%v); relying on the watchdog", g.Name, err)
			}
		}
		if err := m.takeover(g.Name, "automatic failover"); err != nil {
			log.Printf("GROUPS: automatic takeover of %s failed: %v", g.Name, err)
			m.fmu.Lock()
			m.decisions[g.Name] = Decision{Action: "none", Reason: "automatic takeover failed: " + err.Error()}
			m.fmu.Unlock()
		}
	}
}

// decision returns the last failover decision for a group.
func (m *Manager) decision(name string) Decision {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	return m.decisions[name]
}

// StartFailover runs FailoverTick every interval.
func (m *Manager) StartFailover(interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			m.FailoverTick()
		}
	}()
}

// ErrOwnerOnline: a takeover was requested while the owner is reachable.
var ErrOwnerOnline = errors.New("the owner is online in this cluster; use Move on the owner instead")

// Takeover makes this node the owner of a group whose owner is gone: the
// manual action when automatic failover is not possible (two votes, no
// fencing). The operator confirms that the owner is off; ZFS multihost still
// refuses the import if it is not.
func (m *Manager) Takeover(name string) (*Group, error) {
	g, err := Get(m.db, name)
	if err != nil {
		return nil, err
	}
	v, err := m.currentView()
	if err != nil {
		return nil, err
	}
	if g.Owner == v.Self {
		// Import here: the owner imports its pools on request (it waits for
		// the other candidates on its own; the operator knows they are off).
		missing := false
		for _, p := range g.Pools {
			if !v.ImportedHere[p.Name] {
				missing = true
			}
		}
		if !missing || g.Topology != Shared {
			return nil, errors.New("this node already owns the group")
		}
		log.Printf("GROUPS: %s: Import here requested by the operator", name)
		if err := m.importOwned(*g); err != nil {
			return nil, err
		}
		go m.ActivateTick()
		return Get(m.db, name)
	}
	if !g.IsCandidate(v.Self) {
		return nil, errors.New("this node is not a candidate of the group")
	}
	if v.Quorum && contains2(v.Online, g.Owner) {
		return nil, ErrOwnerOnline
	}
	if err := m.takeover(name, "manual takeover"); err != nil {
		return nil, err
	}
	return Get(m.db, name)
}

func (m *Manager) takeover(name, why string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := Get(m.db, name)
	if err != nil {
		return err
	}
	self := m.self()
	imported, err := m.ops.ImportedPools()
	if err != nil {
		return err
	}
	if g.Topology == Replicated {
		// The copy here becomes the group's data: changes made on the old
		// owner since the last replication are not in it (the GUI shows when
		// the last replication was).
		for _, p := range g.Pools {
			if _, ok := imported[p.Name]; !ok {
				return fmt.Errorf("pool %s (this node's copy) is not imported here", p.Name)
			}
			if err := m.repl.SetReadonly(p.Name, false); err != nil {
				return err
			}
		}
		ng := *g
		ng.Owner, ng.Epoch, ng.UpdatedAt, ng.UpdatedBy = self, g.Epoch+1, time.Now(), self
		if err := put(m.db, ng); err != nil {
			return err
		}
		log.Printf("GROUPS: %s: %s, now owned by this node (epoch %d)", name, why, ng.Epoch)
		if err := m.materialize(ng); err != nil {
			log.Printf("GROUPS: %s: %v", name, err)
		}
		go m.ActivateTick()
		for _, e := range m.pushAll(ng, g.Owner) {
			log.Printf("GROUPS: %s: %v (retried by pulling)", name, e)
		}
		return nil
	}
	var done []PoolRef
	for _, p := range g.Pools {
		if _, ok := imported[p.Name]; ok {
			continue
		}
		if err := m.ops.ImportForce(p.GUID); err != nil {
			for _, d := range done {
				_ = m.ops.Export(d.Name)
			}
			return fmt.Errorf("importing %s: %w (if the old owner is still running, ZFS multihost refuses the import: that is the protection working)", p.Name, err)
		}
		done = append(done, p)
	}
	ng := *g
	ng.Owner, ng.Epoch, ng.UpdatedAt, ng.UpdatedBy = self, g.Epoch+1, time.Now(), self
	if err := put(m.db, ng); err != nil {
		return err
	}
	log.Printf("GROUPS: %s: %s, now owned by this node (epoch %d)", name, why, ng.Epoch)
	if err := m.materialize(ng); err != nil {
		log.Printf("GROUPS: %s: %v", name, err)
	}
	go m.ActivateTick() // start the group's stacks and exports here
	// The old owner is unreachable; it learns the new epoch when it returns.
	for _, e := range m.pushAll(ng, g.Owner) {
		log.Printf("GROUPS: %s: %v (retried by pulling)", name, e)
	}
	return nil
}
