package handlers

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"

	"dplaned/internal/configstore"
	"dplaned/internal/gitops"
	"dplaned/internal/secrets"
)

func fleetHandler(t *testing.T) (*FleetHandler, *sql.DB) {
	t.Helper()
	db := testDB(t)
	if err := secrets.Init(filepath.Join(t.TempDir(), "secrets.key")); err != nil {
		t.Fatal(err)
	}
	fakeCommands(t, nil) // no pools, no datasets, no containers
	return NewFleetHandler(db, nil, nil, gitops.ApplyContext{DB: db, SmbConfPath: filepath.Join(t.TempDir(), "smb.conf"),
		NFSExportsPath: filepath.Join(t.TempDir(), "exports")}), db
}

func applyFleet(t *testing.T, h *FleetHandler, rollout string, resources ...map[string]any) resp {
	t.Helper()
	return call(t, h.Apply, req{method: http.MethodPost, body: map[string]any{
		"fleet": "acme", "manager": "hq1", "rollout": rollout, "resources": resources}})
}

func email(t *testing.T, db *sql.DB, user string) string {
	t.Helper()
	var e string
	if err := db.QueryRow(`SELECT COALESCE(email, '') FROM users WHERE username = $1`, user).Scan(&e); err != nil {
		t.Fatalf("user %s: %v", user, err)
	}
	return e
}

func TestFleetApplyCreatesUpdatesAndKeepsLocalChangesAsProposals(t *testing.T) {
	h, db := fleetHandler(t)
	carol := func(mail string) map[string]any {
		return map[string]any{"kind": "user", "key": "carol",
			"payload": map[string]any{"username": "carol", "email": mail, "role": "user", "active": true}}
	}

	r := applyFleet(t, h, "1 users", carol("carol@fleet.lan"))
	if !r.ok() {
		t.Fatalf("apply: %s", r)
	}
	if email(t, db, "carol") != "carol@fleet.lan" {
		t.Fatal("carol not created")
	}
	var origin string
	if err := db.QueryRow(`SELECT origin FROM config_revisions WHERE resource_kind = 'user' AND resource_key = 'carol'
		ORDER BY id DESC LIMIT 1`).Scan(&origin); err != nil || origin != configstore.OriginOverlay {
		t.Fatalf("revision origin %q %v", origin, err)
	}
	m, _ := configstore.FleetMembership(db)
	if m.Fleet != "acme" || m.Manager != "hq1" || m.LastRollout != "1 users" {
		t.Fatalf("membership: %+v", m)
	}

	// The same version again: nothing to do.
	r = applyFleet(t, h, "2 again", carol("carol@fleet.lan"))
	if !r.ok() || r.body["results"].([]any)[0].(map[string]any)["result"] != "unchanged" {
		t.Fatalf("unchanged: %s", r)
	}

	// Changed on this node, then by the fleet: the fleet wins, the node's change is a proposal.
	if _, err := db.Exec(`UPDATE users SET email = 'carol@local.lan' WHERE username = 'carol'`); err != nil {
		t.Fatal(err)
	}
	r = applyFleet(t, h, "3 new address", carol("carol@new.lan"))
	if !r.ok() {
		t.Fatalf("apply: %s", r)
	}
	res := r.body["results"].([]any)[0].(map[string]any)
	if res["result"] != "applied" || res["proposal"] != true || email(t, db, "carol") != "carol@new.lan" {
		t.Fatalf("result %v, email %s", res, email(t, db, "carol"))
	}
	ps, err := configstore.Proposals(db)
	if err != nil || len(ps) != 1 || ps[0].Payload["email"] != "carol@local.lan" || ps[0].PeerName != "the fleet manager" {
		t.Fatalf("proposal: %v %+v", err, ps)
	}

	// Deleting through the fleet.
	r = applyFleet(t, h, "4 remove", map[string]any{"kind": "user", "key": "carol", "payload": nil})
	if !r.ok() {
		t.Fatalf("delete: %s", r)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM users WHERE username = 'carol'`).Scan(&n)
	if n != 0 {
		t.Fatal("carol not deleted")
	}
}

func TestFleetApplyRefusesOtherKindsAndPlainPasswords(t *testing.T) {
	h, _ := fleetHandler(t)
	r := applyFleet(t, h, "x", map[string]any{"kind": "share", "key": "media", "payload": map[string]any{"name": "media", "path": "/mnt/tank/media"}})
	if r.ok() {
		t.Fatalf("a share from the fleet must be refused: %s", r)
	}
	r = applyFleet(t, h, "x", map[string]any{"kind": "user", "key": "eve",
		"payload": map[string]any{"username": "eve", "password_hash": "hunter2", "role": "user", "active": true}})
	if r.ok() {
		t.Fatalf("a plain-text password must be refused: %s", r)
	}
	r = applyFleet(t, h, "x", map[string]any{"kind": "user", "key": "eve",
		"payload": map[string]any{"username": "mallory", "role": "user", "active": true}})
	if r.ok() {
		t.Fatalf("a payload for another user must be refused: %s", r)
	}
}

func TestFleetNodeStatus(t *testing.T) {
	h, _ := fleetHandler(t)
	r := call(t, h.NodeStatus, req{})
	if !r.ok() {
		t.Fatalf("%s", r)
	}
	st := r.body["status"].(map[string]any)
	if st["node_id"] == "" || st["mode"] != "standalone" {
		t.Fatalf("status: %v", st)
	}
}

// The manager's inventory call is recorded; a long silence is reported as
// "fleet manager unreachable" in the sync status, until the node leaves.
func TestFleetManagerContactAndSilence(t *testing.T) {
	h, db := fleetHandler(t)
	sync := &ConfigSyncHandler{db: db}
	fleetOf := func() map[string]any {
		r := call(t, sync.Status, req{})
		if !r.ok() {
			t.Fatalf("status: %s", r)
		}
		return r.body["fleet"].(map[string]any)
	}
	if f := fleetOf(); f["fleet"] != "" || f["unreachable"] != false {
		t.Fatalf("not in a fleet yet: %v", f)
	}

	call(t, h.NodeStatus, req{header: map[string]string{"X-DPlane-Fleet": "acme", "X-DPlane-Fleet-Manager": "hq1"}})
	if f := fleetOf(); f["fleet"] != "acme" || f["manager"] != "hq1" || f["unreachable"] != false {
		t.Fatalf("after the manager's call: %v", f)
	}

	if _, err := db.Exec(`UPDATE fleet_membership SET last_contact_at = NOW() - INTERVAL '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	if f := fleetOf(); f["unreachable"] != true {
		t.Fatalf("ten minutes of silence: %v", f)
	}

	if r := call(t, h.LeaveFleet, req{method: http.MethodDelete}); !r.ok() {
		t.Fatalf("leave: %s", r)
	}
	if f := fleetOf(); f["fleet"] != "" || f["unreachable"] != false {
		t.Fatalf("after leaving: %v", f)
	}
}
