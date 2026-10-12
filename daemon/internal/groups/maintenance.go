package groups

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// Maintenance mode of a node.
//
// A node that is about to be worked on (firmware, a disk shelf, a restart)
// should not be serving anything and should not be given anything. In
// maintenance a node
//
//   - hands the storage groups it owns to another candidate, by the same
//     planned move the operator would do by hand;
//   - takes no group over, automatically or on request, and is not the
//     target of a move;
//   - tells the other nodes (they ask for it with the groups), so that they
//     skip it when they choose who takes over.
//
// Leaving maintenance moves nothing back: where a group runs is decided by
// the operator.

// Maintenance is the maintenance state of this node.
type Maintenance struct {
	Active bool       `json:"active"`
	Since  *time.Time `json:"since,omitempty"`
	Reason string     `json:"reason"`
	SetBy  string     `json:"set_by"`
}

// GetMaintenance reads the maintenance state of this node.
func GetMaintenance(db *sql.DB) (Maintenance, error) {
	var m Maintenance
	var since sql.NullTime
	err := db.QueryRow(`SELECT since, reason, set_by FROM node_maintenance WHERE id = 1`).Scan(&since, &m.Reason, &m.SetBy)
	if errors.Is(err, sql.ErrNoRows) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if since.Valid {
		m.Active, m.Since = true, &since.Time
	} else {
		m.Reason, m.SetBy = "", ""
	}
	return m, nil
}

func setMaintenance(db *sql.DB, on bool, reason, by string) error {
	if !on {
		_, err := db.Exec(`UPDATE node_maintenance SET since = NULL, reason = '', set_by = '' WHERE id = 1`)
		return err
	}
	_, err := db.Exec(`INSERT INTO node_maintenance (id, since, reason, set_by) VALUES (1, NOW(), $1, $2)
		ON CONFLICT (id) DO UPDATE SET since = COALESCE(node_maintenance.since, NOW()), reason = EXCLUDED.reason, set_by = EXCLUDED.set_by`,
		reason, by)
	return err
}

// inMaintenance reports whether this node is in maintenance. An unreadable
// state counts as "yes": a node that cannot tell must not take storage over.
func (m *Manager) inMaintenance() bool {
	st, err := GetMaintenance(m.db)
	if err != nil {
		log.Printf("GROUPS: maintenance state cannot be read (%v): treating this node as in maintenance", err)
		return true
	}
	return st.Active
}

// InMaintenance is the maintenance state for other packages (the cluster
// view, the peer answer).
func (m *Manager) InMaintenance() bool { return m.inMaintenance() }

// peerInMaintenance reports what a member said about itself at the last
// exchange.
func (m *Manager) peerInMaintenance(node string) bool {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	return m.peerMaint[node]
}

// PeersInMaintenance lists the members that reported maintenance.
func (m *Manager) PeersInMaintenance() []string {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	var out []string
	for n, on := range m.peerMaint {
		if on {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Manager) setPeerMaintenance(node string, on bool) {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	if m.peerMaint == nil {
		m.peerMaint = map[string]bool{}
	}
	m.peerMaint[node] = on
}

// MaintStep is what entering maintenance does with one group this node owns.
type MaintStep struct {
	Group  string `json:"group"`
	Action string `json:"action"` // move, stays, blocked
	Target string `json:"target,omitempty"`
	Detail string `json:"detail"`
	Done   bool   `json:"done"`
	Error  string `json:"error,omitempty"`
}

// planMaintenance decides, without side effects, where each group this node
// owns goes: to the first other candidate that is in this node's partition
// and not in maintenance itself.
func planMaintenance(gs []Group, v View, peerMaint func(string) bool) []MaintStep {
	var out []MaintStep
	for _, g := range gs {
		if g.Owner != v.Self {
			continue
		}
		if g.Topology == Standalone {
			out = append(out, MaintStep{Group: g.Name, Action: "stays",
				Detail: "Standalone: this group exists on this node only. It stays here and is not available while the node is down."})
			continue
		}
		var why []string
		target := ""
		for _, c := range g.Candidates {
			switch {
			case c == v.Self:
			case v.Quorum && !contains(v.Online, c):
				why = append(why, c+" is not reachable")
			case peerMaint(c):
				why = append(why, c+" is in maintenance")
			default:
				if target == "" {
					target = c
				}
			}
		}
		if target == "" {
			out = append(out, MaintStep{Group: g.Name, Action: "blocked",
				Detail: "No other node can take this group now: " + strings.Join(why, "; ") + "."})
			continue
		}
		detail := "Moves to " + target + ": stopped here, then started there."
		if g.Topology == Replicated {
			detail = "Moves to " + target + " after a last replication: stopped here, then started on the copy."
		}
		out = append(out, MaintStep{Group: g.Name, Action: "move", Target: target, Detail: detail})
	}
	return out
}

// MaintenancePlan is what entering maintenance would do now.
func (m *Manager) MaintenancePlan() ([]MaintStep, error) {
	gs, err := List(m.db)
	if err != nil {
		return nil, err
	}
	v, err := m.currentView()
	if err != nil {
		return nil, err
	}
	return planMaintenance(gs, v, m.peerInMaintenance), nil
}

// ErrMaintenanceBlocked: a group has nowhere to go.
var ErrMaintenanceBlocked = errors.New("a group cannot be moved to another node")

// EnterMaintenance puts this node into maintenance and moves its groups
// away. With a group that has nowhere to go it refuses, unless force is
// set (the group then stays here). The state is set before the moves, so
// that nothing comes back while they run; a move that fails leaves its
// group here and is reported, and the node stays in maintenance.
func (m *Manager) EnterMaintenance(reason, by string, force bool) ([]MaintStep, error) {
	plan, err := m.MaintenancePlan()
	if err != nil {
		return nil, err
	}
	if !force {
		for _, s := range plan {
			if s.Action == "blocked" {
				return plan, fmt.Errorf("%w: %s (%s)", ErrMaintenanceBlocked, s.Group, s.Detail)
			}
		}
	}
	if err := setMaintenance(m.db, true, reason, by); err != nil {
		return plan, err
	}
	log.Printf("GROUPS: this node enters maintenance (%s)", reason)
	m.notify("info", "ha.maintenance", "", "This node is in maintenance: "+reason)
	for i := range plan {
		if plan[i].Action != "move" {
			continue
		}
		if _, err := m.Move(plan[i].Group, plan[i].Target); err != nil {
			plan[i].Error = err.Error()
			log.Printf("GROUPS: maintenance: %s was not moved to %s: %v", plan[i].Group, plan[i].Target, err)
			continue
		}
		plan[i].Done = true
	}
	return plan, nil
}

// LeaveMaintenance ends maintenance. Nothing is moved back.
func (m *Manager) LeaveMaintenance() error {
	if err := setMaintenance(m.db, false, "", ""); err != nil {
		return err
	}
	log.Printf("GROUPS: this node leaves maintenance")
	m.notify("info", "ha.maintenance", "", "This node left maintenance")
	return nil
}
