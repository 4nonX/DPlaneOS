package configstore

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"dplaned/internal/database"
)

func storeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set: database-backed test")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "c_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.RunMigrations(db); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return db
}

// conflictPair records this node's revision of a dataset and a peer's
// revision of the same dataset, both made against nothing in common.
func conflictPair(t *testing.T, db *sql.DB, localEpoch, remoteEpoch int64) (Revision, Revision) {
	t.Helper()
	local, err := insertRevision(db, Revision{Scope: ScopeGroup, ScopeID: "tank", Kind: KindDataset, Key: "tank/media",
		Payload: map[string]any{"name": "tank/media", "quota": "1T"}, Origin: OriginGUI, Epoch: localEpoch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	remote := Revision{UID: "00000000-0000-4000-8000-000000000001", Scope: ScopeGroup, ScopeID: "tank", Kind: KindDataset,
		Key: "tank/media", Payload: map[string]any{"name": "tank/media", "quota": "2T"}, Epoch: remoteEpoch, Origin: OriginGUI}
	raw, _ := json.Marshal(remote.Payload)
	if _, err := db.Exec(`INSERT INTO config_peer_revisions (uid, peer_id, peer_rev_id, scope_type, scope_id,
		resource_kind, resource_key, payload, origin, created_at, epoch)
		VALUES ($1::uuid, 'kb', 1, $2, $3, $4, $5, $6, $7, NOW(), $8)`,
		remote.UID, remote.Scope, remote.ScopeID, remote.Kind, remote.Key, raw, remote.Origin, remote.Epoch); err != nil {
		t.Fatal(err)
	}
	return *local, remote
}

func openConflicts(t *testing.T, db *sql.DB) []ConflictView {
	t.Helper()
	c, _, err := Conflicts(db)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func peerStatus(t *testing.T, db *sql.DB, uid string) string {
	t.Helper()
	var st string
	if err := db.QueryRow(`SELECT status FROM config_peer_revisions WHERE uid = $1::uuid`, uid).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

var peerB = Peer{ID: "kb", Name: "nas2"}

func TestSettleConflictLocalWinsRecordsResolution(t *testing.T) {
	db := storeTestDB(t)
	local, remote := conflictPair(t, db, 4, 3)
	s := &Syncer{db: db}
	if err := s.settleConflict(peerB, local, remote); err != nil {
		t.Fatal(err)
	}
	head, ok := mustLatest(t, db)["dataset/tank/media"]
	if !ok || head.Origin != OriginMerge || head.BaseUID != local.UID || head.MergeUID != remote.UID {
		t.Fatalf("resolution: %+v", head)
	}
	if head.Payload["quota"] != "1T" {
		t.Errorf("the owner's version must stand: %v", head.Payload)
	}
	if len(openConflicts(t, db)) != 0 {
		t.Error("no conflict may stay open on the winning node")
	}
	if st := peerStatus(t, db, remote.UID); st != peerSuperseded {
		t.Errorf("peer revision status %q", st)
	}
	if ps, _ := Proposals(db); len(ps) != 0 {
		t.Errorf("the winning node keeps no proposal: %+v", ps)
	}
}

func TestSettleConflictRemoteWinsKeepsProposal(t *testing.T) {
	db := storeTestDB(t)
	local, remote := conflictPair(t, db, 3, 4)
	s := &Syncer{db: db}
	if err := s.settleConflict(peerB, local, remote); err != nil {
		t.Fatal(err)
	}
	// The other node records the resolution; this one waits for it.
	if head := mustLatest(t, db)["dataset/tank/media"]; head.UID != local.UID {
		t.Fatalf("this node must not record a resolution: %+v", head)
	}
	cs := openConflicts(t, db)
	if len(cs) != 1 || cs[0].AutoWinner != "kb" || cs[0].AutoReason == "" {
		t.Fatalf("conflict: %+v", cs)
	}
	ps, err := Proposals(db)
	if err != nil || len(ps) != 1 {
		t.Fatalf("proposals: %v %+v", err, ps)
	}
	p := ps[0]
	if p.SourceUID != local.UID || p.WinnerUID != remote.UID || p.PeerID != "kb" || p.Payload["quota"] != "1T" {
		t.Errorf("proposal: %+v", p)
	}
	// Seen again (next round): still one proposal.
	if err := s.settleConflict(peerB, local, remote); err != nil {
		t.Fatal(err)
	}
	if ps, _ := Proposals(db); len(ps) != 1 {
		t.Fatalf("duplicate proposal: %+v", ps)
	}
	if err := DismissProposal(db, p.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := DismissProposal(db, p.ID, "admin"); err != ErrProposalClosed {
		t.Fatalf("second dismiss: %v", err)
	}
	if ps, _ := Proposals(db); len(ps) != 0 {
		t.Fatalf("dismissed proposal still open: %+v", ps)
	}
}

func TestSettleConflictReviewPolicyWaitsForOperator(t *testing.T) {
	db := storeTestDB(t)
	if err := SetPolicy(db, ScopeGroup, PolicyReview); err != nil {
		t.Fatal(err)
	}
	local, remote := conflictPair(t, db, 4, 3)
	s := &Syncer{db: db}
	if err := s.settleConflict(peerB, local, remote); err != nil {
		t.Fatal(err)
	}
	cs := openConflicts(t, db)
	if len(cs) != 1 || cs[0].AutoWinner != "" {
		t.Fatalf("review policy: %+v", cs)
	}
	if head := mustLatest(t, db)["dataset/tank/media"]; head.UID != local.UID {
		t.Fatalf("no resolution may be recorded: %+v", head)
	}
	if Policies(db)[ScopeCluster] != PolicyAuto {
		t.Error("the cluster scope keeps its own policy")
	}
	if err := SetPolicy(db, ScopeNode, PolicyAuto); err == nil {
		t.Error("node scope has no rule")
	}
}

func TestDropProposalAfterKeepingLocalVersion(t *testing.T) {
	db := storeTestDB(t)
	local, remote := conflictPair(t, db, 3, 4)
	if err := keepAsProposal(db, "kb", local, remote.UID, "test"); err != nil {
		t.Fatal(err)
	}
	if err := dropProposal(db, local.UID, "admin"); err != nil {
		t.Fatal(err)
	}
	if ps, _ := Proposals(db); len(ps) != 0 {
		t.Fatalf("proposal must be closed: %+v", ps)
	}
}

func mustLatest(t *testing.T, db *sql.DB) map[string]Revision {
	t.Helper()
	l, err := Latest(db)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
