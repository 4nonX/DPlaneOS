package groups

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"dplaned/internal/database"
	"dplaned/internal/nvmet"
)

// groupsTestDB opens a migrated schema of its own (DATABASE_DSN, as in CI).
func groupsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set: database-backed test")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "g_" + hex.EncodeToString(b)
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

// fakeTransport records pushes; every push is accepted unless refuse names the node.
type fakeTransport struct {
	mu      sync.Mutex
	members []string
	pushed  []string // "node group owner epoch"
	refuse  string
}

func (f *fakeTransport) Push(node string, u Update) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if node == f.refuse {
		return errors.New("refused")
	}
	f.pushed = append(f.pushed, node+" "+u.Group.Name+" "+u.Group.Owner)
	return nil
}
func (f *fakeTransport) Fetch(string) (PeerView, error)                           { return PeerView{}, nil }
func (f *fakeTransport) Members() []string                                        { return f.members }
func (f *fakeTransport) RemoteSnapshots(string, string, string) ([]string, error) { return nil, nil }
func (f *fakeTransport) SendReplica(string, string, string, int64, string, string, io.Reader) error {
	return nil
}
func (f *fakeTransport) FetchResources(string, string) (Resources, error) { return Resources{}, nil }

func maintenanceManager(t *testing.T, db *sql.DB, tr *fakeTransport, online []string) (*Manager, map[string]string) {
	t.Helper()
	imported := map[string]string{"tank": "11", "scratch": "22"}
	var mu sync.Mutex
	ops := Ops{
		ImportedPools: func() (map[string]string, error) {
			mu.Lock()
			defer mu.Unlock()
			out := map[string]string{}
			for k, v := range imported {
				out[k] = v
			}
			return out, nil
		},
		Import:      func(string) error { return nil },
		ImportForce: func(string) error { return nil },
		Export: func(name string) error {
			mu.Lock()
			defer mu.Unlock()
			delete(imported, name)
			return nil
		},
		Uncache: func(string) error { return nil },
	}
	m := NewManager(db, ops, tr, func() string { return "ka" }, func() View {
		return View{Quorum: true, Quorate: true, Online: online, AutoFailover: true, FenceOK: true}
	})
	m.res = ResOps{
		LocalStacks:  func() ([]StackDef, error) { return nil, nil },
		WriteStack:   func(StackDef) error { return nil },
		StackRunning: func(string) (bool, error) { return false, nil },
		StackUp:      func(string) error { return nil },
		StackDown:    func(string) error { return nil },
		LocalExports: func() ([]nvmet.Export, error) { return nil, nil },
		SaveExports:  func([]nvmet.Export) error { return nil },
		ApplyExports: func([]nvmet.Export) error { return nil },
	}
	m.addr = AddrOps{
		Present:  func(string, string) (bool, error) { return false, nil },
		Add:      func(string, string) error { return nil },
		Del:      func(string, string) error { return nil },
		Announce: func(string, string) error { return nil },
	}
	return m, imported
}

func TestEnterMaintenanceMovesOwnedGroups(t *testing.T) {
	db := groupsTestDB(t)
	tr := &fakeTransport{members: []string{"ka", "kb", "kc"}}
	m, imported := maintenanceManager(t, db, tr, []string{"ka", "kb", "kc"})
	if _, err := m.CreateWith("data", Shared, []string{"tank"}, []string{"kb", "kc"}, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateWith("local", Standalone, []string{"scratch"}, nil, 0, false); err != nil {
		t.Fatal(err)
	}
	// kb is in maintenance itself: data must go to kc.
	m.notePeerMaintenance("kb", true)

	moves, err := m.EnterMaintenance("firmware update", "admin")
	if err != nil {
		t.Fatalf("enter: %v %+v", err, moves)
	}
	g, _ := Get(db, "data")
	if g.Owner != "kc" || g.Epoch != 2 {
		t.Fatalf("data: owner %s epoch %d, want kc 2", g.Owner, g.Epoch)
	}
	if _, still := imported["tank"]; still {
		t.Error("tank must be exported here")
	}
	if l, _ := Get(db, "local"); l.Owner != "ka" {
		t.Errorf("standalone group must stay: %+v", l)
	}
	mt, _ := GetMaintenance(db)
	if !mt.Enabled || mt.Reason != "firmware update" || mt.SetBy != "admin" || mt.Since == nil {
		t.Errorf("flag: %+v", mt)
	}
	got := map[string]string{}
	for _, mv := range moves {
		got[mv.Group] = mv.Result + " " + mv.Target
	}
	if got["data"] != "moved kc" || !strings.HasPrefix(got["local"], "stays") {
		t.Errorf("moves: %+v", moves)
	}

	if err := m.ExitMaintenance("admin"); err != nil {
		t.Fatal(err)
	}
	if mt, _ := GetMaintenance(db); mt.Enabled || mt.Since != nil {
		t.Errorf("after exit: %+v", mt)
	}
}

func TestEnterMaintenanceChangesNothingWithoutTarget(t *testing.T) {
	db := groupsTestDB(t)
	tr := &fakeTransport{members: []string{"ka", "kb"}}
	m, _ := maintenanceManager(t, db, tr, []string{"ka"}) // kb is not in the partition
	if _, err := m.CreateWith("data", Shared, []string{"tank"}, []string{"kb"}, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnterMaintenance("", "admin"); !errors.Is(err, ErrNoMaintenanceTarget) {
		t.Fatalf("want ErrNoMaintenanceTarget, got %v", err)
	}
	if mt, _ := GetMaintenance(db); mt.Enabled {
		t.Error("the flag must not be set when nothing could move")
	}
	if g, _ := Get(db, "data"); g.Owner != "ka" || g.Epoch != 1 {
		t.Errorf("group changed: %+v", g)
	}
}

func TestMoveRefusesTargetInMaintenance(t *testing.T) {
	db := groupsTestDB(t)
	tr := &fakeTransport{members: []string{"ka", "kb"}}
	m, _ := maintenanceManager(t, db, tr, []string{"ka", "kb"})
	if _, err := m.CreateWith("data", Shared, []string{"tank"}, []string{"kb"}, 0, false); err != nil {
		t.Fatal(err)
	}
	m.notePeerMaintenance("kb", true)
	if _, err := m.Move("data", "kb"); err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Fatalf("move to a node in maintenance: %v", err)
	}
	if g, _ := Get(db, "data"); g.Owner != "ka" {
		t.Errorf("group moved: %+v", g)
	}
}
