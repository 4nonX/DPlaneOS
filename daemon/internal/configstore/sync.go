package configstore

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"dplaned/internal/gitops"
	"dplaned/internal/secrets"
)

// Revision exchange between nodes (Design 0001, Phase 2).
//
// Each node pulls the revisions of every peer it is paired with and merges
// them per resource (merge.go): changes the peer made are applied here through
// the GitOps engine, changes made on both sides become conflicts for the
// operator. Pulling is the only transport; a node announces a change by asking
// its peers to pull ("notify"), so a peer that cannot be reached simply
// catches up later. Nothing becomes read-only while peers are unreachable.

// SyncKinds are exchanged between nodes.
//
// Not exchanged (yet):
//   - node scope: hostname and network interfaces (KindSystem), SMART tasks,
//     stacks without pool volumes: they belong to one node;
//   - stacks with pool volumes and NVMe-oF exports: they belong to their
//     pool, but must run only on the node that owns the pool; synchronising
//     them needs pool ownership (phase 3), otherwise a replicated standby
//     would start them against its replica;
//   - LDAP, ACME, certificates: their secrets are only fingerprinted in the
//     history (group secrets keys, phase 3); pools come only from state.yaml.
var SyncKinds = map[string]bool{
	KindDataset: true, KindShare: true, KindNFS: true,
	KindUser: true, KindGroup: true, KindReplication: true,
	KindSettings: true,
}

func syncKindList() []string {
	out := make([]string, 0, len(SyncKinds))
	for k := range SyncKinds {
		out = append(out, k)
	}
	return out
}

// Errors returned to the GUI.
var (
	ErrSameNode     = errors.New("this is the same node, or both nodes share one database (HA pair on a shared database), so their configuration is already shared")
	ErrInvalidToken = errors.New("the join token is invalid, expired or already used")
	ErrUnknownPeer  = errors.New("unknown peer")
)

// Peer status values of config_peer_revisions.
const (
	peerPending    = "pending"
	peerAdopted    = "adopted"
	peerSuperseded = "superseded"
	peerConflict   = "conflict"
	peerWaiting    = "waiting"
	peerBlocked    = "blocked"
	// converging: same state on both nodes; the node with the lower id records
	// the merge revision and this node adopts it (one merge, not one per side).
	peerConverging = "converging"
)

const pageSize = 500

// Peer is a paired node.
type Peer struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	URL              string     `json:"url"`
	Fingerprint      string     `json:"fingerprint"`
	Cursor           int64      `json:"-"`
	ServedCursor     int64      `json:"-"`
	LastContact      *time.Time `json:"last_contact"`
	UnreachableSince *time.Time `json:"unreachable_since"`
	LastError        string     `json:"last_error"`
	secret           string
}

func loadPeers(db *sql.DB, id string) ([]Peer, error) {
	rows, err := db.Query(`SELECT id, name, url, secret, tls_fingerprint, cursor, served_cursor,
		last_contact, unreachable_since, last_error FROM config_peers
		WHERE ($1 = '' OR id = $1) ORDER BY name, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		var p Peer
		var sealed string
		var lc, us sql.NullTime
		if err := rows.Scan(&p.ID, &p.Name, &p.URL, &sealed, &p.Fingerprint, &p.Cursor, &p.ServedCursor,
			&lc, &us, &p.LastError); err != nil {
			return nil, err
		}
		if lc.Valid {
			p.LastContact = &lc.Time
		}
		if us.Valid {
			p.UnreachableSince = &us.Time
		}
		if p.secret, err = secrets.Open(sealed); err != nil {
			return nil, fmt.Errorf("peer %s: opening secret: %w", p.ID, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// ── Syncer ────────────────────────────────────────────────────────────────────

// Syncer pulls and merges revisions from all peers.
type Syncer struct {
	db      *sql.DB
	ctx     gitops.ApplyContext
	trigger chan bool // true = full re-evaluation
	roundMu sync.Mutex

	notifyMu    sync.Mutex
	notifyTimer *time.Timer

	evalMu   sync.Mutex
	lastFull map[string]time.Time
}

// NewSyncer creates the syncer; ctx is used to apply changes received from peers.
func NewSyncer(db *sql.DB, ctx gitops.ApplyContext) *Syncer {
	return &Syncer{db: db, ctx: ctx, trigger: make(chan bool, 1), lastFull: map[string]time.Time{}}
}

// Start runs a sync round every interval and whenever triggered, and asks
// peers to pull after local changes.
func (s *Syncer) Start(interval time.Duration) {
	OnChange(s.notifyPeersSoon)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			full := false
			select {
			case <-t.C:
			case full = <-s.trigger:
			}
			if err := s.Round(full); err != nil {
				log.Printf("CONFIG SYNC: %v", err)
			}
		}
	}()
}

// Trigger requests a sync round soon.
func (s *Syncer) Trigger(full bool) {
	select {
	case s.trigger <- full:
	default:
	}
}

// Round pulls from every peer and merges. Errors of individual peers are
// recorded on the peer (status); the first one is returned.
func (s *Syncer) Round(full bool) error {
	if ok, _ := gitops.IsWriter(); !ok {
		return nil
	}
	s.roundMu.Lock()
	defer s.roundMu.Unlock()
	peers, err := loadPeers(s.db, "")
	if err != nil {
		return err
	}
	var first error
	for _, p := range peers {
		if err := s.syncPeer(p, full); err != nil && first == nil {
			first = fmt.Errorf("peer %s (%s): %w", p.Name, p.URL, err)
		}
	}
	return first
}

type revisionsResponse struct {
	NodeID    string     `json:"node_id"`
	Name      string     `json:"name"`
	Revisions []Revision `json:"revisions"`
}

// fetchRevisions pulls every revision after cursor from a peer.
func fetchRevisions(call *peerCall, cursor int64) (*revisionsResponse, error) {
	all := &revisionsResponse{}
	for {
		var resp revisionsResponse
		if err := call.do("GET", fmt.Sprintf("/api/config/sync/peer/revisions?since=%d&limit=%d", cursor, pageSize), nil, &resp); err != nil {
			return nil, err
		}
		all.NodeID, all.Name = resp.NodeID, resp.Name
		all.Revisions = append(all.Revisions, resp.Revisions...)
		if len(resp.Revisions) < pageSize {
			return all, nil
		}
		cursor = resp.Revisions[len(resp.Revisions)-1].ID
	}
}

func (s *Syncer) syncPeer(p Peer, full bool) error {
	self, err := NodeID(s.db)
	if err != nil {
		return err
	}
	call := &peerCall{baseURL: p.URL, pin: p.Fingerprint, headers: map[string]string{hdrNode: self, hdrSecret: p.secret}}
	resp, err := fetchRevisions(call, p.Cursor)
	if err == nil && resp.NodeID != p.ID {
		err = fmt.Errorf("the node at %s is %s, not the paired node %s", p.URL, resp.NodeID, p.ID)
	}
	if err != nil {
		_, _ = s.db.Exec(`UPDATE config_peers SET unreachable_since = COALESCE(unreachable_since, NOW()),
			last_error = $2 WHERE id = $1`, p.ID, err.Error())
		return err
	}
	newData, err := storeReceived(s.db, p.ID, resp.Revisions)
	if err != nil {
		return err
	}
	cursor := p.Cursor
	if n := len(resp.Revisions); n > 0 {
		cursor = resp.Revisions[n-1].ID
	}
	if _, err := s.db.Exec(`UPDATE config_peers SET cursor = $2, last_contact = NOW(), unreachable_since = NULL,
		last_error = '', name = $3, tls_fingerprint = CASE WHEN tls_fingerprint = '' THEN $4 ELSE tls_fingerprint END
		WHERE id = $1`, p.ID, cursor, resp.Name, call.seen); err != nil {
		return err
	}
	p.Name = resp.Name

	// Received revisions waiting for a pool, blocked by the engine or in
	// conflict are re-evaluated every few minutes, new ones right away.
	s.evalMu.Lock()
	due := full || newData || time.Since(s.lastFull[p.ID]) > 5*time.Minute
	if due {
		s.lastFull[p.ID] = time.Now()
	}
	s.evalMu.Unlock()
	if !due {
		return nil
	}
	return s.evaluate(p)
}

// storeReceived keeps the peer's revisions that are not in this node's log.
func storeReceived(db *sql.DB, peerID string, revs []Revision) (bool, error) {
	added := false
	for _, r := range revs {
		if !SyncKinds[r.Kind] || r.Scope == ScopeNode || r.UID == "" {
			continue
		}
		var payload any
		if r.Payload != nil {
			raw, err := json.Marshal(r.Payload)
			if err != nil {
				return added, err
			}
			payload = raw
		}
		res, err := db.Exec(`INSERT INTO config_peer_revisions
			(uid, peer_id, peer_rev_id, base_uid, merge_uid, scope_type, scope_id, resource_kind, resource_key,
			 payload, origin, origin_node, author, note, created_at, status)
			SELECT $1::uuid, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			       CASE WHEN EXISTS (SELECT 1 FROM config_revisions WHERE uid = $1::uuid) THEN 'adopted' ELSE 'pending' END
			ON CONFLICT (uid) DO NOTHING`,
			r.UID, peerID, r.ID, r.BaseUID, r.MergeUID, r.Scope, r.ScopeID, r.Kind, r.Key,
			payload, r.Origin, r.OriginNode, r.Author, r.Note, r.CreatedAt)
		if err != nil {
			return added, fmt.Errorf("storing revision %s: %w", r.UID, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added = true
		}
	}
	return added, nil
}

const peerRevisionColumns = `uid::text, COALESCE(base_uid::text, ''), COALESCE(merge_uid::text, ''), peer_rev_id,
	scope_type, scope_id, resource_kind, resource_key, payload, origin, origin_node, author, note, created_at, status, status_detail`

type peerRevision struct {
	Revision
	Status string
	Detail string
}

func scanPeerRevisions(rows *sql.Rows) ([]peerRevision, error) {
	defer rows.Close()
	var out []peerRevision
	for rows.Next() {
		var r peerRevision
		var payload []byte
		if err := rows.Scan(&r.UID, &r.BaseUID, &r.MergeUID, &r.ID, &r.Scope, &r.ScopeID, &r.Kind, &r.Key,
			&payload, &r.Origin, &r.OriginNode, &r.Author, &r.Note, &r.CreatedAt, &r.Status, &r.Detail); err != nil {
			return nil, err
		}
		if payload != nil {
			if err := json.Unmarshal(payload, &r.Payload); err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// loadAncestry reads the parents of every known revision, local and received.
func loadAncestry(db *sql.DB) (Ancestry, error) {
	anc := Ancestry{}
	rows, err := db.Query(`SELECT uid::text, COALESCE(base_uid::text, ''), COALESCE(merge_uid::text, '') FROM config_revisions
		UNION ALL SELECT uid::text, COALESCE(base_uid::text, ''), COALESCE(merge_uid::text, '') FROM config_peer_revisions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u, b, m string
		if err := rows.Scan(&u, &b, &m); err != nil {
			return nil, err
		}
		anc[u] = [2]string{b, m}
	}
	return anc, rows.Err()
}

func (s *Syncer) setPeerStatus(uid, status, detail string) {
	if _, err := s.db.Exec(`UPDATE config_peer_revisions SET status = $2, status_detail = $3 WHERE uid = $1::uuid`,
		uid, status, detail); err != nil {
		log.Printf("CONFIG SYNC: status of %s: %v", uid, err)
	}
}

func (s *Syncer) closeConflict(peerID, kind, key, resolution string) {
	_, _ = s.db.Exec(`UPDATE config_conflicts SET status = 'resolved', resolution = $4, resolved_at = NOW()
		WHERE peer_id = $1 AND resource_kind = $2 AND resource_key = $3 AND status = 'open'`, peerID, kind, key, resolution)
}

// adopt records a peer revision in this node's log under its own uid.
func adopt(db *sql.DB, r Revision, local *Revision) error {
	_, err := insertRevision(db, Revision{
		UID: r.UID, BaseUID: r.BaseUID, MergeUID: r.MergeUID,
		Scope: r.Scope, ScopeID: r.ScopeID, Kind: r.Kind, Key: r.Key, Payload: r.Payload,
		Origin: OriginPeer, OriginNode: r.OriginNode, Author: r.Author, Note: r.Note,
	}, local)
	return err
}

// evaluate merges the peer's newest revision of every resource.
func (s *Syncer) evaluate(p Peer) error {
	if !gitops.TryLock() {
		return nil // an apply or rollback is running; next round
	}
	defer gitops.Unlock()
	captureMu.Lock()
	defer captureMu.Unlock()

	// Local heads must reflect the live system before comparing.
	if _, err := captureLocked(s.db, OriginDetected, "", ""); err != nil {
		return fmt.Errorf("capture before merge: %w", err)
	}
	rows, err := s.db.Query(`SELECT DISTINCT ON (resource_kind, resource_key) `+peerRevisionColumns+`
		FROM config_peer_revisions WHERE peer_id = $1
		ORDER BY resource_kind, resource_key, peer_rev_id DESC`, p.ID)
	if err != nil {
		return err
	}
	heads, err := scanPeerRevisions(rows)
	if err != nil {
		return err
	}
	latest, err := Latest(s.db)
	if err != nil {
		return err
	}
	anc, err := loadAncestry(s.db)
	if err != nil {
		return err
	}
	live, err := gitops.ReadLiveState(s.db)
	if err != nil {
		return fmt.Errorf("reading live state: %w", err)
	}
	imported := importedPools(live)
	self, err := NodeID(s.db)
	if err != nil {
		return err
	}

	applied := false
	for _, h := range heads {
		if h.Status == peerAdopted || h.Status == peerSuperseded {
			continue
		}
		r := h.Revision
		var local *Revision
		if l, ok := latest[r.resource().ID()]; ok {
			local = &l
		}
		switch Classify(local, &r, anc) {
		case MergeNone:
			s.setPeerStatus(r.UID, peerSuperseded, "")
			s.closeConflict(p.ID, r.Kind, r.Key, "this node is ahead")
		case MergeRecord:
			if err := adopt(s.db, r, local); err != nil {
				return err
			}
			s.setPeerStatus(r.UID, peerAdopted, "")
			s.closeConflict(p.ID, r.Kind, r.Key, "resolved on "+p.Name)
		case MergeFastForward:
			if r.Scope == ScopeGroup && r.ScopeID != "" && !imported[r.ScopeID] {
				s.setPeerStatus(r.UID, peerWaiting, fmt.Sprintf("pool %s is not imported on this node", r.ScopeID))
				continue
			}
			detail := ""
			if r.Kind == KindUser && r.Payload != nil && !liveHasUser(live, r.Key) {
				detail = "created without a password: passwords are not synchronised; set one on this node"
			}
			if _, err := applyOne(s.ctx, r.resource(), live); err != nil {
				s.setPeerStatus(r.UID, peerBlocked, err.Error())
				continue
			}
			if err := adopt(s.db, r, local); err != nil {
				return err
			}
			applied = true
			s.setPeerStatus(r.UID, peerAdopted, detail)
			s.closeConflict(p.ID, r.Kind, r.Key, "resolved on "+p.Name)
			if live, err = gitops.ReadLiveState(s.db); err != nil {
				return fmt.Errorf("reading live state: %w", err)
			}
		case MergeConverged:
			if self > p.ID {
				s.setPeerStatus(r.UID, peerConverging, "same state on both nodes")
				continue
			}
			if _, err := insertRevision(s.db, Revision{
				BaseUID: local.UID, MergeUID: r.UID,
				Scope: local.Scope, ScopeID: local.ScopeID, Kind: local.Kind, Key: local.Key, Payload: local.Payload,
				Origin: OriginMerge, Note: "same state on " + p.Name,
			}, local); err != nil {
				return err
			}
			s.setPeerStatus(r.UID, peerAdopted, "merged: same state on both nodes")
			s.closeConflict(p.ID, r.Kind, r.Key, "both nodes reached the same state")
		case MergeConflict:
			if _, err := s.db.Exec(`INSERT INTO config_conflicts (peer_id, resource_kind, resource_key, local_uid, remote_uid)
				VALUES ($1, $2, $3, $4::uuid, $5::uuid)
				ON CONFLICT (peer_id, resource_kind, resource_key) WHERE status = 'open'
				DO UPDATE SET local_uid = EXCLUDED.local_uid, remote_uid = EXCLUDED.remote_uid`,
				p.ID, r.Kind, r.Key, local.UID, r.UID); err != nil {
				return err
			}
			s.setPeerStatus(r.UID, peerConflict, "changed on both nodes")
		}
	}
	// Older revisions of the same resources are history of the peer.
	if _, err := s.db.Exec(`UPDATE config_peer_revisions SET status = $2 WHERE peer_id = $1 AND status = $3`,
		p.ID, peerSuperseded, peerPending); err != nil {
		return err
	}
	if applied {
		gitops.CommitAllAsync(s.db)
	}
	return nil
}

// notifyPeersSoon asks every peer to pull, debounced.
func (s *Syncer) notifyPeersSoon() {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	if s.notifyTimer != nil {
		s.notifyTimer.Stop()
	}
	s.notifyTimer = time.AfterFunc(2*time.Second, func() {
		self, err := NodeID(s.db)
		if err != nil {
			return
		}
		peers, err := loadPeers(s.db, "")
		if err != nil {
			return
		}
		for _, p := range peers {
			call := &peerCall{baseURL: p.URL, pin: p.Fingerprint, timeout: 5 * time.Second,
				headers: map[string]string{hdrNode: self, hdrSecret: p.secret}}
			_ = call.do("POST", "/api/config/sync/peer/notify", map[string]any{}, nil)
		}
	})
}

// ── Serving peers ─────────────────────────────────────────────────────────────

// AuthenticatePeer checks the credentials a peer presented.
func AuthenticatePeer(db *sql.DB, nodeID, secret string) (*Peer, error) {
	if nodeID == "" || secret == "" {
		return nil, ErrUnknownPeer
	}
	peers, err := loadPeers(db, nodeID)
	if err != nil {
		return nil, err
	}
	if len(peers) == 0 || subtle.ConstantTimeCompare([]byte(peers[0].secret), []byte(secret)) != 1 {
		return nil, ErrUnknownPeer
	}
	return &peers[0], nil
}

// ValidJoinToken reports whether token is unused and unexpired.
func ValidJoinToken(db *sql.DB, token string) bool {
	if token == "" {
		return false
	}
	var ok bool
	_ = db.QueryRow(`SELECT EXISTS (SELECT 1 FROM config_join_tokens
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > NOW())`, hashToken(token)).Scan(&ok)
	return ok
}

// ServeRevisions returns this node's revisions after since, for a peer
// (peer != nil: records how far it has pulled) or a join preview.
func ServeRevisions(db *sql.DB, peer *Peer, since int64, limit int) (*revisionsResponse, error) {
	if limit <= 0 || limit > pageSize {
		limit = pageSize
	}
	self, err := NodeID(db)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT `+revisionColumns+` FROM config_revisions
		WHERE id > $1 AND scope_type <> 'node' AND resource_kind = ANY($2)
		ORDER BY id LIMIT $3`, since, syncKindList(), limit)
	if err != nil {
		return nil, err
	}
	revs, err := scanRevisions(rows)
	if err != nil {
		return nil, err
	}
	if peer != nil {
		_, _ = db.Exec(`UPDATE config_peers SET served_cursor = GREATEST(served_cursor, $2) WHERE id = $1`, peer.ID, since)
	}
	if revs == nil {
		revs = []Revision{}
	}
	return &revisionsResponse{NodeID: self, Name: nodeName(), Revisions: revs}, nil
}

// ── Pairing ───────────────────────────────────────────────────────────────────

// CreateJoinToken returns a single-use token valid for 15 minutes.
func CreateJoinToken(db *sql.DB) (string, time.Time, error) {
	token := "dpj_" + randomHex(24)
	expires := time.Now().Add(15 * time.Minute)
	_, _ = db.Exec(`DELETE FROM config_join_tokens WHERE expires_at < NOW() - INTERVAL '1 day'`)
	_, err := db.Exec(`INSERT INTO config_join_tokens (token_hash, expires_at) VALUES ($1, $2)`, hashToken(token), expires)
	return token, expires, err
}

// JoinRequest is sent by the joining node.
type JoinRequest struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
}

// JoinResponse is the accepting node's answer.
type JoinResponse struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
	Secret string `json:"secret"`
}

func savePeer(db *sql.DB, id, name, url, secret, fingerprint string) error {
	sealed, err := secrets.Seal(secret)
	if err != nil {
		return fmt.Errorf("sealing peer secret: %w", err)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A re-pairing starts over: cursors reset (the other database may be
	// new), earlier received revisions and conflicts are dropped.
	if _, err := tx.Exec(`INSERT INTO config_peers (id, name, url, secret, tls_fingerprint)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, url = EXCLUDED.url, secret = EXCLUDED.secret,
			tls_fingerprint = EXCLUDED.tls_fingerprint, cursor = 0, served_cursor = 0,
			unreachable_since = NULL, last_error = ''`, id, name, url, sealed, fingerprint); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM config_peer_revisions WHERE peer_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE config_conflicts SET status = 'dismissed', resolved_at = NOW()
		WHERE peer_id = $1 AND status = 'open'`, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return setSetting(db, settingNodeMode, "")
}

// AcceptJoin pairs the node presenting a valid join token with this node.
func AcceptJoin(db *sql.DB, token string, req JoinRequest) (*JoinResponse, error) {
	self, err := NodeID(db)
	if err != nil {
		return nil, err
	}
	if req.NodeID == "" || req.URL == "" {
		return nil, errors.New("node_id and url are required")
	}
	if req.NodeID == self {
		return nil, ErrSameNode
	}
	res, err := db.Exec(`UPDATE config_join_tokens SET used_at = NOW()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > NOW()`, hashToken(token))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrInvalidToken
	}
	secret := randomHex(32)
	if err := savePeer(db, req.NodeID, req.Name, req.URL, secret, ""); err != nil {
		return nil, err
	}
	log.Printf("CONFIG SYNC: paired with %s (%s) at %s", req.Name, req.NodeID, req.URL)
	return &JoinResponse{NodeID: self, Name: nodeName(), Secret: secret}, nil
}

// Join pairs this node with the node at url, which issued token. selfURL is
// how the other node reaches this one.
func (s *Syncer) Join(url, token, selfURL string) (*Peer, error) {
	self, err := NodeID(s.db)
	if err != nil {
		return nil, err
	}
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	selfURL = strings.TrimRight(strings.TrimSpace(selfURL), "/")
	if url == "" || selfURL == "" {
		return nil, errors.New("both addresses are required")
	}
	call := &peerCall{baseURL: url, headers: map[string]string{hdrJoinToken: token}}
	var resp JoinResponse
	if err := call.do("POST", "/api/config/sync/peer/join", JoinRequest{NodeID: self, Name: nodeName(), URL: selfURL}, &resp); err != nil {
		return nil, err
	}
	if resp.NodeID == self {
		return nil, ErrSameNode
	}
	if err := savePeer(s.db, resp.NodeID, resp.Name, url, resp.Secret, call.seen); err != nil {
		return nil, err
	}
	log.Printf("CONFIG SYNC: joined %s (%s) at %s", resp.Name, resp.NodeID, url)
	s.Trigger(true)
	peers, err := loadPeers(s.db, resp.NodeID)
	if err != nil || len(peers) == 0 {
		return nil, fmt.Errorf("reading the new peer: %v", err)
	}
	return &peers[0], nil
}

// PreviewItem is what joining would do to one resource on this node.
type PreviewItem struct {
	Kind    string        `json:"kind"`
	Key     string        `json:"key"`
	Action  string        `json:"action"` // add, update, delete, conflict, send, wait, same
	Detail  string        `json:"detail,omitempty"`
	Changes []FieldChange `json:"changes,omitempty"` // this node → other node
}

// Preview is the merge preview shown before joining.
type Preview struct {
	NodeID      string         `json:"node_id"`
	Name        string         `json:"name"`
	Fingerprint string         `json:"fingerprint"`
	Counts      map[string]int `json:"counts"`
	Items       []PreviewItem  `json:"items"`
}

// Preview computes, without changing anything, how this node's configuration
// and the other node's would be merged.
func (s *Syncer) Preview(url, token string) (*Preview, error) {
	self, err := NodeID(s.db)
	if err != nil {
		return nil, err
	}
	call := &peerCall{baseURL: strings.TrimRight(strings.TrimSpace(url), "/"), headers: map[string]string{hdrJoinToken: token}}
	resp, err := fetchRevisions(call, 0)
	if err != nil {
		return nil, err
	}
	if resp.NodeID == self {
		return nil, ErrSameNode
	}
	if _, err := Capture(s.db, OriginDetected, "", ""); err != nil {
		return nil, fmt.Errorf("recording the current configuration: %w", err)
	}
	latest, err := Latest(s.db)
	if err != nil {
		return nil, err
	}
	anc, err := loadAncestry(s.db)
	if err != nil {
		return nil, err
	}
	live, err := gitops.ReadLiveState(s.db)
	if err != nil {
		return nil, err
	}
	imported := importedPools(live)

	heads := map[string]Revision{}
	for _, r := range resp.Revisions {
		if !SyncKinds[r.Kind] || r.Scope == ScopeNode {
			continue
		}
		anc.Add(r)
		heads[r.resource().ID()] = r // ascending ids: the last one is the newest
	}
	pv := &Preview{NodeID: resp.NodeID, Name: resp.Name, Fingerprint: FormatFingerprint(call.seen), Counts: map[string]int{}}
	add := func(it PreviewItem) {
		pv.Items = append(pv.Items, it)
		pv.Counts[it.Action]++
	}
	for id, r := range heads {
		r := r
		var local *Revision
		if l, ok := latest[id]; ok {
			local = &l
		}
		it := PreviewItem{Kind: r.Kind, Key: r.Key}
		var localPayload map[string]any
		if local != nil {
			localPayload = local.Payload
		}
		switch Classify(local, &r, anc) {
		case MergeNone:
			if local != nil && local.Payload == nil {
				continue // deleted on both
			}
			it.Action = "send"
			if samePayload(localPayload, r.Payload) {
				it.Action = "same"
			}
		case MergeRecord, MergeConverged:
			if r.Payload == nil {
				continue
			}
			it.Action = "same"
		case MergeFastForward:
			switch {
			case r.Payload == nil:
				it.Action = "delete"
			case local == nil || local.Payload == nil:
				it.Action = "add"
			default:
				it.Action = "update"
			}
			if r.Scope == ScopeGroup && r.ScopeID != "" && !imported[r.ScopeID] {
				it.Action, it.Detail = "wait", fmt.Sprintf("pool %s is not imported on this node", r.ScopeID)
			}
			it.Changes = DiffPayloads(localPayload, r.Payload)
		case MergeConflict:
			it.Action = "conflict"
			it.Changes = DiffPayloads(localPayload, r.Payload)
		}
		add(it)
	}
	for id, l := range latest {
		if _, ok := heads[id]; ok || !SyncKinds[l.Kind] || l.Scope == ScopeNode || l.Payload == nil {
			continue
		}
		add(PreviewItem{Kind: l.Kind, Key: l.Key, Action: "send"})
	}
	return pv, nil
}

// ── Leaving ───────────────────────────────────────────────────────────────────

func forgetPeer(db *sql.DB, id string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM config_peers WHERE id = $1`,
		`DELETE FROM config_peer_revisions WHERE peer_id = $1`,
		`UPDATE config_conflicts SET status = 'dismissed', resolved_at = NOW() WHERE peer_id = $1 AND status = 'open'`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PeerLeft handles a peer announcing that it detached or removed this node.
func PeerLeft(db *sql.DB, id string) error {
	log.Printf("CONFIG SYNC: peer %s left", id)
	return forgetPeer(db, id)
}

// RemovePeer stops exchanging revisions with one peer (and tells it so).
func (s *Syncer) RemovePeer(id string) error {
	peers, err := loadPeers(s.db, id)
	if err != nil {
		return err
	}
	if len(peers) == 0 {
		return ErrUnknownPeer
	}
	s.tellLeaving(peers[0])
	return forgetPeer(s.db, id)
}

func (s *Syncer) tellLeaving(p Peer) {
	self, err := NodeID(s.db)
	if err != nil {
		return
	}
	call := &peerCall{baseURL: p.URL, pin: p.Fingerprint, timeout: 5 * time.Second,
		headers: map[string]string{hdrNode: self, hdrSecret: p.secret}}
	if err := call.do("POST", "/api/config/sync/peer/leave", map[string]any{}, nil); err != nil {
		log.Printf("CONFIG SYNC: could not tell %s that this node leaves (it will see this node as unreachable): %v", p.Name, err)
	}
}

// Detach makes this node permanently independent: configuration is kept,
// every peer is removed. Rejoin later through the join flow with a preview.
func (s *Syncer) Detach() error {
	peers, err := loadPeers(s.db, "")
	if err != nil {
		return err
	}
	for _, p := range peers {
		s.tellLeaving(p)
		if err := forgetPeer(s.db, p.ID); err != nil {
			return err
		}
	}
	if err := setSetting(s.db, settingIndependentSince, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return setSetting(s.db, settingNodeMode, "independent")
}

// ── Status ────────────────────────────────────────────────────────────────────

// PeerStatus is a peer as shown in the GUI.
type PeerStatus struct {
	Peer
	Reachable          bool   `json:"reachable"`
	Waiting            int    `json:"waiting"` // local changes the peer has not pulled yet
	FingerprintDisplay string `json:"fingerprint_display"`
}

// SyncStatus is this node's state (Design 0001 section 5.5).
type SyncStatus struct {
	NodeID    string       `json:"node_id"`
	Name      string       `json:"name"`
	Mode      string       `json:"mode"` // standalone, connected, isolated, independent
	Since     *time.Time   `json:"since,omitempty"`
	Waiting   int          `json:"waiting"`
	Conflicts int          `json:"conflicts"`
	Pending   int          `json:"pending"` // received changes waiting or blocked
	Peers     []PeerStatus `json:"peers"`
	Writer    bool         `json:"writer"`
	WriterWhy string       `json:"writer_reason,omitempty"`
}

// Status reports the node state, peers and counts.
func Status(db *sql.DB) (*SyncStatus, error) {
	id, err := NodeID(db)
	if err != nil {
		return nil, err
	}
	st := &SyncStatus{NodeID: id, Name: nodeName(), Peers: []PeerStatus{}}
	st.Writer, st.WriterWhy = gitops.IsWriter()
	peers, err := loadPeers(db, "")
	if err != nil {
		return nil, err
	}
	for _, p := range peers {
		ps := PeerStatus{Peer: p, Reachable: p.UnreachableSince == nil && p.LastContact != nil,
			FingerprintDisplay: FormatFingerprint(p.Fingerprint)}
		if err := db.QueryRow(`SELECT COUNT(*) FROM config_revisions WHERE id > $1 AND scope_type <> 'node'
			AND resource_kind = ANY($2) AND origin <> 'peer'`, p.ServedCursor, syncKindList()).Scan(&ps.Waiting); err != nil {
			return nil, err
		}
		if ps.Waiting > st.Waiting {
			st.Waiting = ps.Waiting
		}
		if p.UnreachableSince != nil && (st.Since == nil || p.UnreachableSince.Before(*st.Since)) {
			st.Since = p.UnreachableSince
		}
		st.Peers = append(st.Peers, ps)
	}
	switch {
	case getSetting(db, settingNodeMode) == "independent":
		st.Mode = "independent"
		if t, err := time.Parse(time.RFC3339, getSetting(db, settingIndependentSince)); err == nil {
			st.Since = &t
		}
	case len(peers) == 0:
		st.Mode = "standalone"
	case st.Since != nil:
		st.Mode = "isolated"
	default:
		st.Mode = "connected"
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM config_conflicts WHERE status = 'open'`).Scan(&st.Conflicts); err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM config_peer_revisions WHERE status IN ('waiting', 'blocked')`).Scan(&st.Pending); err != nil {
		return nil, err
	}
	return st, nil
}

// ── Conflicts and pending changes ─────────────────────────────────────────────

// ConflictView is one open conflict with both sides.
type ConflictView struct {
	ID        int64         `json:"id"`
	PeerID    string        `json:"peer_id"`
	PeerName  string        `json:"peer_name"`
	Kind      string        `json:"kind"`
	Key       string        `json:"key"`
	Local     *Revision     `json:"local"`
	Remote    *Revision     `json:"remote"`
	Changes   []FieldChange `json:"changes"` // this node → other node
	CreatedAt time.Time     `json:"created_at"`
}

// PendingView is a received change that could not be applied yet.
type PendingView struct {
	PeerID   string   `json:"peer_id"`
	PeerName string   `json:"peer_name"`
	Kind     string   `json:"kind"`
	Key      string   `json:"key"`
	Status   string   `json:"status"`
	Detail   string   `json:"detail"`
	Revision Revision `json:"revision"`
}

func peerRevisionByUID(db *sql.DB, uid string) (*peerRevision, error) {
	rows, err := db.Query(`SELECT `+peerRevisionColumns+` FROM config_peer_revisions WHERE uid = $1::uuid`, uid)
	if err != nil {
		return nil, err
	}
	revs, err := scanPeerRevisions(rows)
	if err != nil {
		return nil, err
	}
	if len(revs) == 0 {
		return nil, sql.ErrNoRows
	}
	return &revs[0], nil
}

// Conflicts lists open conflicts and received changes that wait or were blocked.
func Conflicts(db *sql.DB) ([]ConflictView, []PendingView, error) {
	names := map[string]string{}
	if rows, err := db.Query(`SELECT id, name FROM config_peers`); err == nil {
		for rows.Next() {
			var id, n string
			if rows.Scan(&id, &n) == nil {
				names[id] = n
			}
		}
		rows.Close()
	}
	latest, err := Latest(db)
	if err != nil {
		return nil, nil, err
	}
	rows, err := db.Query(`SELECT id, peer_id, resource_kind, resource_key, remote_uid::text, created_at
		FROM config_conflicts WHERE status = 'open' ORDER BY created_at`)
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		id                    int64
		peer, kind, key, ruid string
		created               time.Time
	}
	var open []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.peer, &r.kind, &r.key, &r.ruid, &r.created); err != nil {
			rows.Close()
			return nil, nil, err
		}
		open = append(open, r)
	}
	rows.Close()
	conflicts := []ConflictView{}
	for _, c := range open {
		v := ConflictView{ID: c.id, PeerID: c.peer, PeerName: names[c.peer], Kind: c.kind, Key: c.key, CreatedAt: c.created}
		if l, ok := latest[c.kind+"/"+c.key]; ok {
			v.Local = &l
		}
		if r, err := peerRevisionByUID(db, c.ruid); err == nil {
			v.Remote = &r.Revision
		}
		var lp, rp map[string]any
		if v.Local != nil {
			lp = v.Local.Payload
		}
		if v.Remote != nil {
			rp = v.Remote.Payload
		}
		v.Changes = DiffPayloads(lp, rp)
		conflicts = append(conflicts, v)
	}

	prow, err := db.Query(`SELECT peer_id, ` + peerRevisionColumns + ` FROM config_peer_revisions
		WHERE status IN ('waiting', 'blocked') ORDER BY received_at`)
	if err != nil {
		return nil, nil, err
	}
	defer prow.Close()
	pending := []PendingView{}
	for prow.Next() {
		var peer string
		var r peerRevision
		var payload []byte
		if err := prow.Scan(&peer, &r.UID, &r.BaseUID, &r.MergeUID, &r.ID, &r.Scope, &r.ScopeID, &r.Kind, &r.Key,
			&payload, &r.Origin, &r.OriginNode, &r.Author, &r.Note, &r.CreatedAt, &r.Status, &r.Detail); err != nil {
			return nil, nil, err
		}
		if payload != nil {
			_ = json.Unmarshal(payload, &r.Payload)
		}
		pending = append(pending, PendingView{PeerID: peer, PeerName: names[peer], Kind: r.Kind, Key: r.Key,
			Status: r.Status, Detail: r.Detail, Revision: r.Revision})
	}
	return conflicts, pending, prow.Err()
}

// Resolution choices.
const (
	ResolveLocal  = "local"
	ResolveRemote = "remote"
	ResolveMerged = "merged"
)

// Resolve settles a conflict: keep this node's version, take the other
// node's, or use an edited payload. The result is a merge revision with both
// versions as parents, so the other node takes it over on its next pull.
func (s *Syncer) Resolve(id int64, choice string, edited map[string]any, author string) error {
	if ok, reason := gitops.IsWriter(); !ok {
		return fmt.Errorf("%w: %s", gitops.ErrNotWriter, reason)
	}
	if !gitops.TryLock() {
		return ErrBusy
	}
	defer gitops.Unlock()
	captureMu.Lock()
	defer captureMu.Unlock()

	var peerID, kind, key, ruid, status string
	err := s.db.QueryRow(`SELECT peer_id, resource_kind, resource_key, remote_uid::text, status
		FROM config_conflicts WHERE id = $1`, id).Scan(&peerID, &kind, &key, &ruid, &status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "open") {
		return errors.New("this conflict is no longer open; reload")
	}
	if err != nil {
		return err
	}
	if _, err := captureLocked(s.db, OriginDetected, "", ""); err != nil {
		return err
	}
	latest, err := Latest(s.db)
	if err != nil {
		return err
	}
	l, ok := latest[kind+"/"+key]
	if !ok {
		return errors.New("this node has no record of the resource; reload")
	}
	remote, err := peerRevisionByUID(s.db, ruid)
	if err != nil {
		return fmt.Errorf("the other node's version is no longer available: %w", err)
	}
	target := l.resource()
	note := ""
	peerName := peerID
	if ps, err := loadPeers(s.db, peerID); err == nil && len(ps) > 0 {
		peerName = ps[0].Name
	}
	switch choice {
	case ResolveLocal:
		note = "conflict with " + peerName + ": kept this node's version"
	case ResolveRemote:
		target.Payload = remote.Payload
		note = "conflict with " + peerName + ": took " + peerName + "'s version"
	case ResolveMerged:
		target.Payload = normalize(edited)
		// The edited version must still describe this resource.
		ds, err := Assemble([]Resource{target})
		if err != nil {
			return fmt.Errorf("edited version: %w", err)
		}
		if back := Extract(ds, nodeName()); len(back) != 1 || back[0].ID() != target.ID() {
			return errors.New("the edited version must describe the same resource (keep its name)")
		}
		target.Payload = normalize(back0(ds))
		note = "conflict with " + peerName + ": merged by hand"
	default:
		return errors.New("choice must be local, remote or merged")
	}
	if choice != ResolveLocal {
		live, err := gitops.ReadLiveState(s.db)
		if err != nil {
			return err
		}
		if target.Scope == ScopeGroup && target.ScopeID != "" && !importedPools(live)[target.ScopeID] {
			return fmt.Errorf("pool %s is not imported on this node", target.ScopeID)
		}
		if _, err := applyOne(s.ctx, target, live); err != nil {
			return err
		}
	}
	if _, err := insertRevision(s.db, Revision{
		BaseUID: l.UID, MergeUID: remote.UID,
		Scope: target.Scope, ScopeID: target.ScopeID, Kind: kind, Key: key, Payload: target.Payload,
		Origin: OriginMerge, Author: author, Note: note,
	}, &l); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE config_conflicts SET status = 'resolved', resolution = $2, resolved_by = $3, resolved_at = NOW()
		WHERE id = $1`, id, choice, author); err != nil {
		return err
	}
	s.setPeerStatus(remote.UID, peerAdopted, "conflict resolved: "+choice)
	if choice != ResolveLocal {
		gitops.CommitAllAsync(s.db)
	}
	return nil
}

// back0 returns the payload of the single resource in ds.
func back0(ds *gitops.DesiredState) map[string]any {
	rs := Extract(ds, nodeName())
	if len(rs) != 1 {
		return nil
	}
	return rs[0].Payload
}
