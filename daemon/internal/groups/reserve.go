package groups

import (
	"fmt"
	"log"
	"time"

	"dplaned/internal/diskres"
)

// Reserver keeps the disks of a shared pool reserved for the node that
// serves it (internal/diskres): the disks themselves then refuse writes
// from every other node.
type Reserver interface {
	Acquire(guid string) (diskres.Report, error)
	Release(guid string) error
	Verify(guid string) (rep diskres.Report, lost bool, err error)
	Status(guid string) (diskres.Report, error)
}

// reserve takes the disks of a shared group's pool for this node. It is
// called at the moments this node becomes the one that writes to the pool:
// before every import, and when a pool imported here is put into a group.
// An error means disks that have reservations could not be taken; the pool
// must not be imported then.
func (m *Manager) reserve(g Group, p PoolRef) error {
	if m.Reservations == nil || g.Topology != Shared || p.GUID == "" {
		return nil
	}
	if _, err := m.Reservations.Acquire(p.GUID); err != nil {
		return fmt.Errorf("reserving the disks of %s: %w", p.Name, err)
	}
	return nil
}

// unreserve gives the disks up after this node exported the pool on
// purpose (a planned move, a removed group). Failing to is not fatal: the
// next owner takes the disks anyway.
func (m *Manager) unreserve(g Group, p PoolRef) {
	if m.Reservations == nil || g.Topology != Shared || p.GUID == "" {
		return
	}
	if err := m.Reservations.Release(p.GUID); err != nil {
		log.Printf("GROUPS: %s: releasing the disks of %s: %v (the next owner takes them)", g.Name, p.Name, err)
	}
}

// PoolReservation is the reservation state of one pool of a shared group.
type PoolReservation struct {
	Group string         `json:"group"`
	Pool  string         `json:"pool"`
	Owner string         `json:"owner"`
	Disks []diskres.Disk `json:"disks"`
	Error string         `json:"error,omitempty"`
}

// ReservationStatus reports the disks of every shared group's pools as
// this node sees them.
func (m *Manager) ReservationStatus() ([]PoolReservation, error) {
	out := []PoolReservation{}
	if m.Reservations == nil {
		return out, nil
	}
	gs, err := List(m.db)
	if err != nil {
		return nil, err
	}
	for _, g := range gs {
		if g.Topology != Shared {
			continue
		}
		for _, p := range g.Pools {
			pr := PoolReservation{Group: g.Name, Pool: p.Name, Owner: g.Owner, Disks: []diskres.Disk{}}
			rep, err := m.Reservations.Status(p.GUID)
			if err != nil {
				pr.Error = err.Error()
			} else if rep.Disks != nil {
				pr.Disks = rep.Disks
			}
			out = append(out, pr)
		}
	}
	return out, nil
}

// ReservationTick checks the disks of the shared pools this node serves:
// disks added to a pool are reserved, and a node that finds another node's
// reservation on its disks has been shut out and says so (once).
func (m *Manager) ReservationTick() {
	if m.Reservations == nil {
		return
	}
	gs, err := List(m.db)
	if err != nil {
		return
	}
	self := m.self()
	imported, err := m.ops.ImportedPools()
	if err != nil {
		return
	}
	for _, g := range gs {
		if g.Topology != Shared || g.Owner != self || m.isMoving(g.Name) {
			continue
		}
		for _, p := range g.Pools {
			if _, here := imported[p.Name]; !here {
				continue
			}
			_, lost, err := m.Reservations.Verify(p.GUID)
			if err != nil {
				log.Printf("GROUPS: %s: checking the disks of %s: %v", g.Name, p.Name, err)
				continue
			}
			m.fmu.Lock()
			if m.shutOut == nil {
				m.shutOut = map[string]bool{}
			}
			was := m.shutOut[p.Name]
			m.shutOut[p.Name] = lost
			m.fmu.Unlock()
			if lost && !was {
				msg := fmt.Sprintf("Another node has taken the disks of pool %s (storage group %s). This node has been shut out: "+
					"the disks refuse its writes and the pool is stopped here. Restart this node: it then follows the new owner.", p.Name, g.Name)
				log.Printf("GROUPS: %s", msg)
				m.notify("critical", "ha.fenced", g.Name, msg)
			}
		}
	}
}

// StartReservations runs ReservationTick periodically.
func (m *Manager) StartReservations(interval time.Duration) {
	if m.Reservations == nil {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			m.ReservationTick()
		}
	}()
}
