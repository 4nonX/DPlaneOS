package configstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dplaned/internal/gitops"
)

// Default conflict rules and proposals (Design 0001 section 5.6, phase 4).
//
// When the same resource was changed differently on two nodes, a rule decides
// where one applies, from the revisions alone, so both nodes reach the same
// verdict without talking to each other:
//
//   - group scope: the version written under the higher storage group epoch
//     wins (the group's owner at the time);
//   - cluster scope: the version written by the cluster coordinator in the
//     later term wins (revisions written by another node carry term 0);
//   - otherwise (node scope, equal authority, no cluster): the operator
//     decides in the review screen, as before.
//
// Only the winning node records the resolution (a merge revision with both
// versions as parents); the other node takes it over on its next pull. The
// losing node keeps its own change as a proposal: nothing is dropped.
//
// Per scope the operator can turn the rule off ("review"): every conflict of
// that scope then waits for a decision.

// Policies.
const (
	PolicyAuto   = "auto"
	PolicyReview = "review"
)

// policyScopes are the scopes a rule exists for.
var policyScopes = []string{ScopeGroup, ScopeCluster}

func policyKey(scope string) string { return "config_conflict_policy_" + scope }

// Policy returns the conflict policy of scope (auto unless set to review).
func Policy(db *sql.DB, scope string) string {
	if getSetting(db, policyKey(scope)) == PolicyReview {
		return PolicyReview
	}
	return PolicyAuto
}

// Policies returns the policy of every scope that has a rule.
func Policies(db *sql.DB) map[string]string {
	out := map[string]string{}
	for _, s := range policyScopes {
		out[s] = Policy(db, s)
	}
	return out
}

// SetPolicy sets the conflict policy of scope.
func SetPolicy(db *sql.DB, scope, policy string) error {
	ok := false
	for _, s := range policyScopes {
		ok = ok || s == scope
	}
	if !ok {
		return fmt.Errorf("no conflict rule exists for scope %q", scope)
	}
	if policy != PolicyAuto && policy != PolicyReview {
		return fmt.Errorf("policy must be %s or %s", PolicyAuto, PolicyReview)
	}
	return setSetting(db, policyKey(scope), policy)
}

// Winner of a conflict under the default rules.
const (
	WinnerNone   = ""
	WinnerLocal  = "local"
	WinnerRemote = "remote"
)

// DefaultWinner decides a conflict between this node's head and a peer's
// head of the same resource. It depends only on the two revisions, so the
// peer computes the mirror verdict. reason explains it in the GUI.
func DefaultWinner(local, remote Revision) (winner, reason string) {
	if local.Scope != remote.Scope || local.Epoch == remote.Epoch {
		return WinnerNone, ""
	}
	hi := max(local.Epoch, remote.Epoch)
	switch local.Scope {
	case ScopeGroup:
		reason = fmt.Sprintf("the version written by the storage group's owner (epoch %d) wins", hi)
	case ScopeCluster:
		reason = fmt.Sprintf("the version written by the cluster coordinator (term %d) wins", hi)
	default:
		return WinnerNone, ""
	}
	if local.Epoch > remote.Epoch {
		return WinnerLocal, reason
	}
	return WinnerRemote, reason
}

// ── Proposals ─────────────────────────────────────────────────────────────────

// Proposal is a change of this node that lost a conflict under the default
// rules, kept for the operator.
type Proposal struct {
	ID        int64          `json:"id"`
	PeerID    string         `json:"peer_id"`
	PeerName  string         `json:"peer_name"`
	Scope     string         `json:"scope"`
	ScopeID   string         `json:"scope_id"`
	Kind      string         `json:"kind"`
	Key       string         `json:"key"`
	Payload   map[string]any `json:"payload"` // nil = the change was a deletion
	SourceUID string         `json:"source_uid"`
	WinnerUID string         `json:"winner_uid"`
	Reason    string         `json:"reason"`
	Status    string         `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	// Changes from the current state to the proposal.
	Changes []FieldChange `json:"changes"`
}

// keepAsProposal stores this node's losing revision (idempotent per revision).
func keepAsProposal(db *sql.DB, peerID string, lost Revision, winnerUID, reason string) error {
	var payload any
	if lost.Payload != nil {
		raw, err := json.Marshal(lost.Payload)
		if err != nil {
			return err
		}
		payload = raw
	}
	_, err := db.Exec(`INSERT INTO config_proposals
		(peer_id, scope_type, scope_id, resource_kind, resource_key, payload, source_uid, winner_uid, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7::uuid, $8::uuid, $9)
		ON CONFLICT (source_uid) DO UPDATE SET winner_uid = EXCLUDED.winner_uid, reason = EXCLUDED.reason`,
		peerID, lost.Scope, lost.ScopeID, lost.Kind, lost.Key, payload, lost.UID, winnerUID, reason)
	return err
}

// dropProposal closes the open proposal made from a revision (the operator
// kept that version after all, so there is nothing left to propose).
func dropProposal(db *sql.DB, sourceUID, by string) error {
	_, err := db.Exec(`UPDATE config_proposals SET status = 'dismissed', closed_by = $2, closed_at = NOW()
		WHERE source_uid = $1::uuid AND status = 'open'`, sourceUID, by)
	return err
}

// Proposals lists the open proposals with the changes they would make now.
func Proposals(db *sql.DB) ([]Proposal, error) {
	names := peerNames(db)
	latest, err := Latest(db)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, peer_id, scope_type, scope_id, resource_kind, resource_key, payload,
		source_uid::text, winner_uid::text, reason, status, created_at
		FROM config_proposals WHERE status = 'open' ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Proposal{}
	for rows.Next() {
		var p Proposal
		var payload []byte
		if err := rows.Scan(&p.ID, &p.PeerID, &p.Scope, &p.ScopeID, &p.Kind, &p.Key, &payload,
			&p.SourceUID, &p.WinnerUID, &p.Reason, &p.Status, &p.CreatedAt); err != nil {
			return nil, err
		}
		if payload != nil {
			if err := json.Unmarshal(payload, &p.Payload); err != nil {
				return nil, err
			}
		}
		p.PeerName = names[p.PeerID]
		var cur map[string]any
		if l, ok := latest[p.Kind+"/"+p.Key]; ok {
			cur = l.Payload
		}
		p.Changes = DiffPayloads(cur, p.Payload)
		out = append(out, p)
	}
	return out, rows.Err()
}

func peerNames(db *sql.DB) map[string]string {
	names := map[string]string{}
	rows, err := db.Query(`SELECT id, name FROM config_peers`)
	if err != nil {
		return names
	}
	defer rows.Close()
	for rows.Next() {
		var id, n string
		if rows.Scan(&id, &n) == nil {
			names[id] = n
		}
	}
	return names
}

// ErrProposalClosed: the proposal was applied or dismissed already.
var ErrProposalClosed = errors.New("this proposal is no longer open; reload")

func openProposal(db *sql.DB, id int64) (*Proposal, error) {
	var p Proposal
	var payload []byte
	err := db.QueryRow(`SELECT id, peer_id, scope_type, scope_id, resource_kind, resource_key, payload, source_uid::text, status
		FROM config_proposals WHERE id = $1`, id).Scan(&p.ID, &p.PeerID, &p.Scope, &p.ScopeID, &p.Kind, &p.Key, &payload, &p.SourceUID, &p.Status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && p.Status != "open") {
		return nil, ErrProposalClosed
	}
	if err != nil {
		return nil, err
	}
	if payload != nil {
		if err := json.Unmarshal(payload, &p.Payload); err != nil {
			return nil, err
		}
	}
	return &p, nil
}

// ApplyProposal makes a proposal this node's current version: it is applied
// to the live system and recorded as a new change on top of the version that
// won, so the other nodes take it over like any other change.
func (s *Syncer) ApplyProposal(id int64, author string) (*RollbackResult, error) {
	if ok, reason := gitops.IsWriter(); !ok {
		return nil, fmt.Errorf("%w: %s", gitops.ErrNotWriter, reason)
	}
	p, err := openProposal(s.db, id)
	if err != nil {
		return nil, err
	}
	var open int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM config_conflicts WHERE status = 'open'
		AND resource_kind = $1 AND resource_key = $2`, p.Kind, p.Key).Scan(&open); err != nil {
		return nil, err
	}
	if open > 0 {
		return nil, errors.New("the conflict about this resource is still open: wait until the other node has recorded its resolution (or resolve it in the review), then apply the proposal")
	}
	if !gitops.TryLock() {
		return nil, ErrBusy
	}
	defer gitops.Unlock()
	live, err := gitops.ReadLiveState(s.db)
	if err != nil {
		return nil, fmt.Errorf("reading live state: %w", err)
	}
	target := Resource{Kind: p.Kind, Key: p.Key, Scope: p.Scope, ScopeID: p.ScopeID, Payload: p.Payload}
	if target.Scope == ScopeGroup && target.ScopeID != "" && !importedPools(live)[target.ScopeID] {
		return nil, fmt.Errorf("pool %s is not imported on this node; apply the proposal on the node that owns it", target.ScopeID)
	}
	if target.Kind == KindUser && target.Payload != nil && !liveHasUser(live, target.Key) {
		return nil, fmt.Errorf("user %q was deleted meanwhile; create it again instead", target.Key)
	}
	peer := Peer{ID: p.PeerID}
	if ps, err := loadPeers(s.db, p.PeerID); err == nil && len(ps) > 0 {
		peer = ps[0]
	}
	if err := s.applyRemote(peer, target, live); err != nil {
		if errors.Is(err, errMaterialUnavailable) || errors.Is(err, ErrSecretChanged) {
			return nil, fmt.Errorf("the password or key of this change is no longer stored on this node (the other version replaced it); make the change again by hand: %w", err)
		}
		return nil, err
	}
	res := &RollbackResult{}
	cs, err := Capture(s.db, OriginGUI, author, fmt.Sprintf("proposal %d applied (change that lost a conflict with %s)", p.ID, peer.Name))
	if err != nil {
		return res, fmt.Errorf("applied, but recording the result failed: %w", err)
	}
	res.Changeset, res.NoChange = cs, cs == ""
	if _, err := s.db.Exec(`UPDATE config_proposals SET status = 'applied', closed_by = $2, closed_at = NOW()
		WHERE id = $1`, p.ID, author); err != nil {
		return res, err
	}
	gitops.CommitAllAsync(s.db)
	return res, nil
}

// DismissProposal closes a proposal without applying it.
func DismissProposal(db *sql.DB, id int64, by string) error {
	res, err := db.Exec(`UPDATE config_proposals SET status = 'dismissed', closed_by = $2, closed_at = NOW()
		WHERE id = $1 AND status = 'open'`, id, by)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrProposalClosed
	}
	return nil
}
