package configstore

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"dplaned/internal/gitops"
)

// Fleet-scope configuration from the fleet manager (Design 0001 sections
// 5.2, 5.6 and 5.7, phase 6).
//
// The fleet manager sends fleet-wide resources (users, groups, shared
// settings) to a node with the node's API token. On the node they are
// cluster-scope resources: once applied they reach the node's cluster peers
// and its Git repository like any change. The fleet's version wins (design
// 5.6): if the resource was changed on the node since the fleet last set it,
// that change is kept as a proposal on the node, never dropped.
//
// Passwords are set only when the fleet creates a user (a bcrypt hash in
// password_hash); an existing user's password is the node's business.

// OriginOverlay marks revisions applied from the fleet manager.
const OriginOverlay = "overlay"

// FleetPeerID names the fleet manager in proposals.
const FleetPeerID = "fleet"

// OverlayKinds are the kinds the fleet manager may set.
var OverlayKinds = map[string]bool{KindUser: true, KindGroup: true, KindSettings: true}

// OverlayResult is what became of one resource on this node.
type OverlayResult struct {
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	Result string `json:"result"` // applied, unchanged, failed
	Detail string `json:"detail,omitempty"`
	// Proposal: this node's own change was replaced and kept as a proposal.
	Proposal bool `json:"proposal,omitempty"`
}

// ErrOverlayKind: the fleet manager sent a kind it may not set.
var ErrOverlayKind = errors.New("the fleet manager may set users, groups and shared settings only")

// overlayView strips what the fleet does not set from a payload, for
// comparing the fleet's version with this node's.
func overlayView(r Resource) map[string]any {
	if r.Payload == nil {
		return nil
	}
	p := map[string]any{}
	for k, v := range r.Payload {
		if r.Kind == KindUser && (k == "credentials" || k == "password_hash") {
			continue
		}
		p[k] = v
	}
	return normalize(p)
}

// lastOverlay returns this node's newest revision of a resource made by the
// fleet manager (nil if none).
func lastOverlay(db *sql.DB, kind, key string) (*Revision, error) {
	rows, err := db.Query(`SELECT `+revisionColumns+` FROM config_revisions
		WHERE resource_kind = $1 AND resource_key = $2 AND origin = $3 ORDER BY id DESC LIMIT 1`, kind, key, OriginOverlay)
	if err != nil {
		return nil, err
	}
	revs, err := scanRevisions(rows)
	if err != nil || len(revs) == 0 {
		return nil, err
	}
	return &revs[0], nil
}

// ApplyOverlay applies the fleet's resources on this node.
func ApplyOverlay(db *sql.DB, ctx gitops.ApplyContext, fleet, manager, rollout string, rs []Resource) ([]OverlayResult, error) {
	for _, r := range rs {
		if !OverlayKinds[r.Kind] {
			return nil, fmt.Errorf("%w (got %s)", ErrOverlayKind, r.Kind)
		}
	}
	if ok, reason := gitops.IsWriter(); !ok {
		return nil, fmt.Errorf("%w: %s", gitops.ErrNotWriter, reason)
	}
	if !gitops.TryLock() {
		return nil, ErrBusy
	}
	defer gitops.Unlock()
	captureMu.Lock()
	defer captureMu.Unlock()

	if _, err := captureLocked(db, OriginDetected, "", ""); err != nil {
		return nil, fmt.Errorf("recording this node's state first: %w", err)
	}
	latest, err := Latest(db)
	if err != nil {
		return nil, err
	}
	live, err := gitops.ReadLiveState(db)
	if err != nil {
		return nil, fmt.Errorf("reading live state: %w", err)
	}
	author := "fleet " + fleet
	var out []OverlayResult
	applied := false
	for _, want := range rs {
		want.Scope, want.ScopeID = ScopeCluster, "local"
		want.Payload = normalize(want.Payload)
		res := OverlayResult{Kind: want.Kind, Key: want.Key}
		var local *Revision
		if l, ok := latest[want.ID()]; ok && l.Payload != nil {
			local = &l
		}
		if local == nil && want.Payload == nil {
			res.Result = "unchanged"
			out = append(out, res)
			continue
		}
		if local != nil && want.Payload != nil && samePayload(overlayView(local.Resource()), overlayView(want)) {
			res.Result = "unchanged"
			out = append(out, res)
			continue
		}
		// Changed on this node since the fleet last set it: the fleet's
		// version wins, the node's change stays as a proposal.
		if local != nil && local.Origin != OriginOverlay {
			prev, err := lastOverlay(db, want.Kind, want.Key)
			if err != nil {
				return out, err
			}
			if prev == nil || !samePayload(overlayView(prev.Resource()), overlayView(local.Resource())) {
				if err := keepAsProposal(db, FleetPeerID, *local, "", "the fleet's version wins (fleet "+fleet+")"); err != nil {
					return out, err
				}
				res.Proposal = true
			}
		}
		target := want
		if target.Kind == KindUser && target.Payload != nil && local != nil {
			// An existing user keeps the password this node has.
			p := copyPayload(target.Payload)
			p["password_hash"] = ""
			target.Payload = p
		}
		if _, err := applyOne(ctx, target, live); err != nil {
			res.Result, res.Detail = "failed", err.Error()
			out = append(out, res)
			continue
		}
		applied = true
		res.Result = "applied"
		out = append(out, res)
		if live, err = gitops.ReadLiveState(db); err != nil {
			return out, fmt.Errorf("reading live state: %w", err)
		}
	}
	if applied {
		// Record what the system now holds as the fleet's revisions.
		note := "from the fleet manager " + manager
		if rollout != "" {
			note += ", rollout " + rollout
		}
		if _, err := captureLocked(db, OriginOverlay, author, note); err != nil {
			return out, fmt.Errorf("applied, but recording the result failed: %w", err)
		}
		gitops.CommitAllAsync(db)
	}
	if _, err := db.Exec(`INSERT INTO fleet_membership (id, fleet, manager, last_apply_at, last_rollout, last_contact_at)
		VALUES (TRUE, $1, $2, $3, $4, $3)
		ON CONFLICT (id) DO UPDATE SET fleet = EXCLUDED.fleet, manager = EXCLUDED.manager,
			last_apply_at = EXCLUDED.last_apply_at, last_rollout = EXCLUDED.last_rollout, last_contact_at = EXCLUDED.last_contact_at`,
		fleet, manager, time.Now(), rollout); err != nil {
		return out, err
	}
	return out, nil
}

func copyPayload(p map[string]any) map[string]any {
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

// Membership is the fleet this node takes configuration from.
type Membership struct {
	Fleet         string     `json:"fleet"`
	Manager       string     `json:"manager"`
	LastApplyAt   *time.Time `json:"last_apply_at,omitempty"`
	LastRollout   string     `json:"last_rollout,omitempty"`
	LastContactAt *time.Time `json:"last_contact_at,omitempty"`
	// Unreachable: the manager has not called for ManagerSilence. The
	// node is not affected; only new fleet changes wait.
	Unreachable bool `json:"unreachable"`
}

// ManagerSilence: the manager reads the inventory every minute; this long
// without a call means it is down or cannot reach this node.
const ManagerSilence = 5 * time.Minute

// FleetMembership reads it (zero value: not managed by a fleet).
func FleetMembership(db *sql.DB) (Membership, error) {
	var m Membership
	var at, contact sql.NullTime
	err := db.QueryRow(`SELECT fleet, manager, last_apply_at, last_rollout, last_contact_at FROM fleet_membership WHERE id`).
		Scan(&m.Fleet, &m.Manager, &at, &m.LastRollout, &contact)
	if errors.Is(err, sql.ErrNoRows) {
		return Membership{}, nil
	}
	if at.Valid {
		m.LastApplyAt = &at.Time
	}
	if contact.Valid {
		m.LastContactAt = &contact.Time
	}
	m.Unreachable = m.Fleet != "" && (m.LastContactAt == nil || time.Since(*m.LastContactAt) > ManagerSilence)
	return m, err
}

// FleetContact records a call from a fleet manager (the inventory poll).
func FleetContact(db *sql.DB, fleet, manager string) error {
	_, err := db.Exec(`INSERT INTO fleet_membership (id, fleet, manager, last_contact_at) VALUES (TRUE, $1, $2, NOW())
		ON CONFLICT (id) DO UPDATE SET fleet = EXCLUDED.fleet, manager = EXCLUDED.manager, last_contact_at = NOW()`, fleet, manager)
	return err
}

// LeaveFleet forgets the membership (after the node was removed from the
// fleet). What the fleet applied stays.
func LeaveFleet(db *sql.DB) error {
	_, err := db.Exec(`DELETE FROM fleet_membership`)
	return err
}
