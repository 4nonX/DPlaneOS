package configstore

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// The Git repository as a peer (Design 0001 section 5.6: one merge engine
// for reconnects, GUI versus Git, and pull requests).
//
// The Git backend (internal/gitsync) turns what changed in the repository
// into revisions of this store, with the revision the repository's version
// was based on as their base. They are merged like a paired node's: a
// change made only in the repository is applied (fast-forward), a resource
// changed both here and in the repository becomes a conflict in the review
// screen. The default conflict rules (coordinator, group owner) do not apply
// to the repository: those conflicts always wait for the operator. Secrets
// never come from the repository; resources keep this node's.

// GitPeerID is the peer id of the repository in config_peer_revisions and
// config_conflicts.
const GitPeerID = "git"

// gitPeer is the repository as a Peer (it has no URL and no secret).
var gitPeer = Peer{ID: GitPeerID, Name: "the Git repository"}

// StoreGitRevisions records changes read from the repository as pending
// peer revisions (a uid already known is kept as it is).
func StoreGitRevisions(db *sql.DB, revs []Revision) error {
	for i, r := range revs {
		if r.UID == "" {
			return fmt.Errorf("%s/%s: a revision from the repository needs a uid", r.Kind, r.Key)
		}
		var payload any
		if r.Payload != nil {
			raw, err := json.Marshal(r.Payload)
			if err != nil {
				return err
			}
			payload = raw
		}
		if r.CreatedAt.IsZero() {
			r.CreatedAt = time.Now()
		}
		// peer_rev_id orders a peer's revisions of a resource: the newest wins.
		seq := time.Now().UnixNano() + int64(i)
		if _, err := db.Exec(`INSERT INTO config_peer_revisions
			(uid, peer_id, peer_rev_id, base_uid, merge_uid, scope_type, scope_id, resource_kind, resource_key,
			 payload, origin, origin_node, author, note, created_at, status, epoch)
			VALUES ($1::uuid, $2, $3, NULLIF($4, '')::uuid, NULL, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'pending', 0)
			ON CONFLICT (uid) DO NOTHING`,
			r.UID, GitPeerID, seq, r.BaseUID, r.Scope, r.ScopeID, r.Kind, r.Key, payload, OriginGit, r.OriginNode,
			r.Author, r.Note, r.CreatedAt); err != nil {
			return fmt.Errorf("storing %s/%s from the repository: %w", r.Kind, r.Key, err)
		}
	}
	return nil
}

// OriginGit marks revisions that came from the Git repository.
const OriginGit = "git"

// EvaluateGit merges the pending revisions from the repository.
func (s *Syncer) EvaluateGit() error {
	return s.evaluate(gitPeer)
}

// GitRevisionState is what became of a revision from the repository.
type GitRevisionState struct {
	Status string // pending, adopted, superseded, conflict, waiting, blocked
	Detail string
}

// GitRevisionStates returns the state of the given revisions from the repository.
func GitRevisionStates(db *sql.DB, uids []string) (map[string]GitRevisionState, error) {
	out := map[string]GitRevisionState{}
	if len(uids) == 0 {
		return out, nil
	}
	rows, err := db.Query(`SELECT uid::text, status, status_detail FROM config_peer_revisions
		WHERE peer_id = $1 AND uid::text = ANY($2)`, GitPeerID, uids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var uid string
		var st GitRevisionState
		if err := rows.Scan(&uid, &st.Status, &st.Detail); err != nil {
			return nil, err
		}
		out[uid] = st
	}
	return out, rows.Err()
}
