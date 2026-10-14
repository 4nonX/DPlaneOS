package groups

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// Maintenance mode of a node (Design 0001 section 5.10, phase 4).
//
// A node in maintenance can be updated, rebooted or rewired without the
// cluster sending storage its way: it is never chosen by automatic failover,
// a planned move refuses it as target, and it does not take groups over on
// its own. Entering maintenance first moves the shared and replicated groups
// it owns to other candidates; standalone groups have nowhere to go and stay.
// Leaving maintenance moves nothing back (there is no automatic failback).
//
// Every member learns the others' maintenance from the group sync (PeerView).

// Maintenance is this node's maintenance state.
type Maintenance struct {
	Enabled bool       `json:"enabled"`
	Reason  string     `json:"reason,omitempty"`
	SetBy   string     `json:"set_by,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
}

// GetMaintenance reads this node's maintenance state.
func GetMaintenance(db *sql.DB) (Maintenance, error) {
	var m Maintenance
	var since sql.NullTime
	err := db.QueryRow(`SELECT enabled, reason, set_by, since FROM node_maintenance WHERE id`).Scan(&m.Enabled, &m.Reason, &m.SetBy, &since)
	if errors.Is(err, sql.ErrNoRows) {
		return Maintenance{}, nil
	}
	if since.Valid && m.Enabled {
		m.Since = &since.Time
	}
	return m, err
}

func setMaintenance(db *sql.DB, on bool, reason, by string) error {
	_, err := db.Exec(`INSERT INTO node_maintenance (id, enabled, reason, set_by, since) VALUES (TRUE, $1, $2, $3, NOW())
		ON CONFLICT (id) DO UPDATE SET enabled = EXCLUDED.enabled, reason = EXCLUDED.reason,
			set_by = EXCLUDED.set_by, since = EXCLUDED.since`, on, reason, by)
	return err
}

// inMaintenance reports this node's flag (false if it cannot be read).
func (m *Manager) inMaintenance() bool {
	if m.db == nil {
		return false
	}
	mt, err := GetMaintenance(m.db)
	return err == nil && mt.Enabled
}

// Maintenances returns the nodes known to be in maintenance: this node from
// its database, the others as last reported by the group sync.
func (m *Manager) Maintenances() map[string]bool {
	out := map[string]bool{}
	m.fmu.Lock()
	for k, v := range m.peerMaint {
		if v {
			out[k] = true
		}
	}
	m.fmu.Unlock()
	if m.inMaintenance() {
		out[m.self()] = true
	}
	return out
}

func (m *Manager) notePeerMaintenance(node string, on bool) {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	if m.peerMaint == nil {
		m.peerMaint = map[string]bool{}
	}
	m.peerMaint[node] = on
}

// MaintenanceMove is what entering maintenance did with one group.
type MaintenanceMove struct {
	Group  string `json:"group"`
	Target string `json:"target,omitempty"`
	Result string `json:"result"` // moved, stays, failed
	Detail string `json:"detail,omitempty"`
}

// planMaintenance picks, for every group this node owns, where it goes: the
// first other candidate (priority order) that is online and not in
// maintenance. Standalone groups stay. ok is false if a shared or replicated
// group has no such candidate.
func planMaintenance(gs []Group, v View) (moves []MaintenanceMove, ok bool) {
	ok = true
	for _, g := range gs {
		if g.Owner != v.Self {
			continue
		}
		if g.Topology == Standalone {
			moves = append(moves, MaintenanceMove{Group: g.Name, Result: "stays", Detail: "standalone group: it has no other node"})
			continue
		}
		target := ""
		for _, c := range g.Candidates {
			if c == v.Self || v.Maintenance[c] {
				continue
			}
			if v.Quorum && !contains(v.Online, c) {
				continue
			}
			target = c
			break
		}
		if target == "" {
			ok = false
			moves = append(moves, MaintenanceMove{Group: g.Name, Result: "failed",
				Detail: "no other candidate is online and out of maintenance"})
			continue
		}
		moves = append(moves, MaintenanceMove{Group: g.Name, Target: target, Result: "planned"})
	}
	return moves, ok
}

// ErrNoMaintenanceTarget: a group this node owns could not be moved anywhere.
var ErrNoMaintenanceTarget = errors.New("cannot enter maintenance: a group this node owns has no other node to move to")

// EnterMaintenance moves this node's shared and replicated groups to other
// candidates and sets the maintenance flag. Nothing changes unless every such
// group has a target. If a move then fails, the flag stays set (no new group
// comes here) and the result says which group is still here.
func (m *Manager) EnterMaintenance(reason, by string) ([]MaintenanceMove, error) {
	gs, err := List(m.db)
	if err != nil {
		return nil, err
	}
	v, err := m.currentView()
	if err != nil {
		return nil, err
	}
	moves, ok := planMaintenance(gs, v)
	if !ok {
		var stuck []string
		for _, mv := range moves {
			if mv.Result == "failed" {
				stuck = append(stuck, mv.Group)
			}
		}
		return moves, fmt.Errorf("%w (%s); bring another candidate online or move the group by hand", ErrNoMaintenanceTarget, strings.Join(stuck, ", "))
	}
	// Set the flag first: from now on no failover or move picks this node.
	if err := setMaintenance(m.db, true, reason, by); err != nil {
		return nil, err
	}
	log.Printf("GROUPS: maintenance mode on (%s)", reason)
	var failed []string
	for i, mv := range moves {
		if mv.Result != "planned" {
			continue
		}
		if _, err := m.Move(mv.Group, mv.Target); err != nil {
			moves[i].Result, moves[i].Detail = "failed", err.Error()
			failed = append(failed, mv.Group)
			continue
		}
		moves[i].Result = "moved"
	}
	if len(failed) > 0 {
		return moves, fmt.Errorf("maintenance mode is on, but these groups are still owned by this node: %s", strings.Join(failed, ", "))
	}
	return moves, nil
}

// ExitMaintenance clears the flag. Groups stay where they are.
func (m *Manager) ExitMaintenance(by string) error {
	if err := setMaintenance(m.db, false, "", by); err != nil {
		return err
	}
	log.Printf("GROUPS: maintenance mode off")
	return nil
}
