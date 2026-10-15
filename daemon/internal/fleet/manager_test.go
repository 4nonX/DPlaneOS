package fleet

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"dplaned/internal/database"
	"dplaned/internal/secrets"
)

func managerDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set: database-backed test")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "fl_" + hex.EncodeToString(b)
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
	u, _ := url.Parse(dsn)
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
	if err := secrets.Init(filepath.Join(t.TempDir(), "secrets.key")); err != nil {
		t.Fatal(err)
	}
	return db
}

// fakeRemote answers like a node: inventory and apply. fail makes apply fail.
type fakeRemote struct {
	srv     *httptest.Server
	name    string
	cluster string
	coord   bool
	mu      sync.Mutex
	applied []string // rollout tags
	fail    bool
}

func newFakeRemote(t *testing.T, name string) *fakeRemote {
	f := &fakeRemote{name: name}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-"+name {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/api/fleet/node-status":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": NodeStatus{
				NodeID: "id-" + name, Name: name, Mode: "standalone", Cluster: f.cluster, Coordinator: f.coord, Quorate: true}})
		case "/api/fleet/apply":
			var b struct {
				Rollout string `json:"rollout"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			if f.fail {
				_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "disk full"})
				return
			}
			f.applied = append(f.applied, b.Rollout)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "results": []ApplyResult{{Kind: "user", Key: "carol", Result: "applied"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRemote) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied)
}

func rolloutState(t *testing.T, db *sql.DB, id int64) Rollout {
	t.Helper()
	rs, err := Rollouts(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("rollout %d not found", id)
	return Rollout{}
}

func TestManagerRollsOutWaveByWave(t *testing.T) {
	db := managerDB(t)
	if err := SaveSettings(db, Settings{Enabled: true, Name: "acme"}); err != nil {
		t.Fatal(err)
	}
	canary, b, c := newFakeRemote(t, "canary"), newFakeRemote(t, "b"), newFakeRemote(t, "c")
	ids := map[string]int64{}
	for _, f := range []*fakeRemote{canary, b, c} {
		r, err := AddRemote(db, f.name, f.srv.URL, "tok-"+f.name, map[string]string{"tier": "test"})
		if err != nil {
			t.Fatalf("add %s: %v", f.name, err)
		}
		if r.Status == nil || r.Status.NodeID != "id-"+f.name {
			t.Fatalf("inventory: %+v", r)
		}
		ids[f.name] = r.ID
	}
	if _, err := AddRemote(db, "bad", b.srv.URL+"/x", "tok-b", nil); err == nil {
		t.Error("an address with a path must be refused")
	}
	if _, err := AddRemote(db, "wrongtoken", c.srv.URL+"0", "nope", nil); err == nil {
		t.Error("a remote that does not answer must be refused")
	}
	if err := PutResource(db, "user", "carol", map[string]any{"username": "carol", "role": "user", "active": true}, "admin"); err != nil {
		t.Fatal(err)
	}
	rs, _ := Resources(db)
	id, err := CreateRollout(db, "carol", []Resource{rs[0].Resource},
		[]Wave{{Name: "canary", Remotes: []int64{ids["canary"]}}, {Name: "rest", Remotes: []int64{ids["b"], ids["c"]}}}, 0, true, "admin")
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(db, func() string { return "hq1" })

	m.Tick() // wave 1: the canary
	if canary.count() != 1 || b.count()+c.count() != 0 {
		t.Fatalf("canary first: %d %d %d", canary.count(), b.count(), c.count())
	}
	m.Tick() // wave 1 complete (soak 0)
	m.Tick() // advance
	c.fail = true
	m.Tick() // wave 2: b ok, c fails (attempt 1)
	if b.count() != 1 {
		t.Fatalf("b not applied")
	}
	m.Tick() // c attempt 2
	m.Tick() // c attempt 3: failed
	m.Tick() // pause
	r := rolloutState(t, db, id)
	if r.State != RolloutPaused || r.Message == "" {
		t.Fatalf("a failing remote must pause the rollout: %+v", r)
	}
	// Fixed: continue.
	c.fail = false
	if err := Continue(db, id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4 && rolloutState(t, db, id).State == RolloutRunning; i++ {
		m.Tick()
	}
	r = rolloutState(t, db, id)
	if r.State != RolloutDone || c.count() != 1 {
		t.Fatalf("after continue: %+v (c applied %d)", r, c.count())
	}
	for _, tg := range r.Targets {
		if tg.State != TargetDone || tg.AppliedAt == nil {
			t.Errorf("target %+v", tg)
		}
	}
	if canary.applied[0] != "1 carol" {
		t.Errorf("rollout tag sent: %q", canary.applied[0])
	}
}

func TestManagerAppliesThroughTheCoordinator(t *testing.T) {
	db := managerDB(t)
	_ = SaveSettings(db, Settings{Enabled: true, Name: "acme"})
	n1, n2 := newFakeRemote(t, "nas1"), newFakeRemote(t, "nas2")
	n1.cluster, n2.cluster, n2.coord = "dplane-c1", "dplane-c1", true
	r1, _ := AddRemote(db, "nas1", n1.srv.URL, "tok-nas1", nil)
	r2, _ := AddRemote(db, "nas2", n2.srv.URL, "tok-nas2", nil)
	_ = PutResource(db, "group", "staff", map[string]any{"name": "staff", "gid": 3000}, "admin")
	rs, _ := Resources(db)
	id, err := CreateRollout(db, "staff", []Resource{rs[0].Resource}, []Wave{{Name: "all", Remotes: []int64{r1.ID, r2.ID}}}, 0, true, "admin")
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(db, func() string { return "hq1" })
	for i := 0; i < 4; i++ {
		m.Tick()
	}
	if n2.count() != 1 || n1.count() != 0 {
		t.Fatalf("applied through the coordinator only: nas1 %d nas2 %d", n1.count(), n2.count())
	}
	r := rolloutState(t, db, id)
	if r.State != RolloutDone {
		t.Fatalf("state %+v", r)
	}
	for _, tg := range r.Targets {
		if tg.Name == "nas1" && tg.Detail != "applied through nas2 (same cluster)" {
			t.Errorf("nas1: %+v", tg)
		}
	}
}

func TestRolloutValidation(t *testing.T) {
	db := managerDB(t)
	if _, err := CreateRollout(db, "x", nil, []Wave{{Name: "a", Remotes: []int64{1}}}, 0, true, "a"); err == nil {
		t.Error("no resources")
	}
	if _, err := CreateRollout(db, "x", []Resource{{Kind: "user", Key: "a"}}, []Wave{{Name: "a", Remotes: []int64{1}}, {Name: "b", Remotes: []int64{1}}}, 0, true, "a"); err == nil {
		t.Error("a remote in two waves")
	}
	if err := PutResource(db, "share", "media", map[string]any{}, "a"); err == nil {
		t.Error("shares are not fleet-wide")
	}
	if err := SetRolloutState(db, 999, RolloutPaused, ""); err == nil {
		t.Error("unknown rollout")
	}
}
