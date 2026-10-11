package groups

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"dplaned/internal/database"
	"dplaned/internal/diskres"
	"dplaned/internal/nvmet"
)

// groupsTestDB opens a migrated schema of its own, or skips the test.
func groupsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set: database-backed test")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "t_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatalf("creating test schema: %v", err)
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

// recorder notes storage operations in the order they happen.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}

func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

type fakeReserver struct {
	rec        *recorder
	acquireErr error
	lost       bool
}

func (f *fakeReserver) Acquire(guid string) (diskres.Report, error) {
	f.rec.add("reserve " + guid)
	return diskres.Report{}, f.acquireErr
}
func (f *fakeReserver) Release(guid string) error {
	f.rec.add("release " + guid)
	return nil
}
func (f *fakeReserver) Verify(guid string) (diskres.Report, bool, error) {
	f.rec.add("verify " + guid)
	return diskres.Report{}, f.lost, nil
}
func (f *fakeReserver) Status(guid string) (diskres.Report, error) {
	return diskres.Report{Disks: []diskres.Disk{{Device: "/dev/sda", State: diskres.Held}}}, nil
}

type fakeTransport struct {
	rec     *recorder
	pushErr error
}

func (f *fakeTransport) Push(node string, u Update) error {
	f.rec.add("push " + node)
	return f.pushErr
}
func (f *fakeTransport) Fetch(string) (PeerView, error) { return PeerView{}, errors.New("unreachable") }
func (f *fakeTransport) Members() []string              { return []string{"a", "b"} }
func (f *fakeTransport) RemoteSnapshots(string, string, string) ([]string, error) {
	return nil, nil
}
func (f *fakeTransport) SendReplica(string, string, string, int64, string, string, io.Reader) error {
	return nil
}
func (f *fakeTransport) FetchResources(string, string) (Resources, error) { return Resources{}, nil }

// testManager is a Manager for node self whose storage is a map.
func testManager(t *testing.T, db *sql.DB, self string, imported map[string]string) (*Manager, *recorder, *fakeReserver, *fakeTransport) {
	t.Helper()
	rec := &recorder{}
	var mu sync.Mutex
	guidName := func(guid string) string { return "pool-" + guid }
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
		Import: func(guid string) error {
			rec.add("import " + guid)
			mu.Lock()
			imported[guidName(guid)] = guid
			mu.Unlock()
			return nil
		},
		ImportForce: func(guid string) error {
			rec.add("import -f " + guid)
			mu.Lock()
			imported[guidName(guid)] = guid
			mu.Unlock()
			return nil
		},
		Export: func(name string) error {
			rec.add("export " + name)
			mu.Lock()
			delete(imported, name)
			mu.Unlock()
			return nil
		},
		Uncache: func(string) error { return nil },
	}
	tr := &fakeTransport{rec: rec}
	m := NewManager(db, ops, tr, func() string { return self }, func() View {
		return View{Quorum: true, Quorate: true, Online: []string{self}}
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
	res := &fakeReserver{rec: rec}
	m.Reservations = res
	return m, rec, res, tr
}

// storage returns the events that concern the disks and the pool.
func storage(events []string) []string {
	var out []string
	for _, e := range events {
		if !strings.HasPrefix(e, "push ") && !strings.HasPrefix(e, "verify ") {
			out = append(out, e)
		}
	}
	return out
}

// The disks are taken before a pool is imported and given up after it was
// exported on purpose, in every path that moves a shared pool.
func TestReservationsFollowThePool(t *testing.T) {
	db := groupsTestDB(t)
	a, rec, _, tr := testManager(t, db, "a", map[string]string{"pool-77": "77"})

	// A pool put into a shared group: its disks are this node's from now on.
	if _, err := a.CreateWith("g", Shared, []string{"pool-77"}, []string{"b"}, 0, false); err != nil {
		t.Fatal(err)
	}
	if got := storage(rec.take()); !reflect.DeepEqual(got, []string{"reserve 77"}) {
		t.Errorf("creating the group: %v", got)
	}

	// Planned move: export, give the disks up, then the target is told.
	if _, err := a.Move("g", "b"); err != nil {
		t.Fatal(err)
	}
	if got := rec.take(); !reflect.DeepEqual(got[:3], []string{"export pool-77", "release 77", "push b"}) {
		t.Errorf("move: %v", got)
	}

	// The move back here, as the target sees it: take the disks, import.
	g, _ := Get(db, "g")
	back := *g
	back.Owner, back.Epoch = "a", g.Epoch+1
	if err := a.Receive(Update{Group: back}); err != nil {
		t.Fatal(err)
	}
	if got := storage(rec.take()); !reflect.DeepEqual(got, []string{"reserve 77", "import 77"}) {
		t.Errorf("becoming the owner: %v", got)
	}

	// A target that does not take over: the pool comes back here, and so
	// do the disks.
	tr.pushErr = errors.New("unreachable")
	if _, err := a.Move("g", "b"); err == nil {
		t.Fatal("move to an unreachable target succeeded")
	}
	if got := storage(rec.take()); !reflect.DeepEqual(got, []string{"export pool-77", "release 77", "reserve 77", "import 77"}) {
		t.Errorf("failed move: %v", got)
	}
	tr.pushErr = nil

	// Removing the group leaves the pool here, as an ordinary pool.
	if err := a.RemoveGroup("g"); err != nil {
		t.Fatal(err)
	}
	if got := storage(rec.take()); !reflect.DeepEqual(got, []string{"release 77"}) {
		t.Errorf("removing the group: %v", got)
	}
}

func TestTakeoverTakesTheDisksFirst(t *testing.T) {
	db := groupsTestDB(t)
	if err := put(db, Group{Name: "g", Topology: Shared, Owner: "a", Epoch: 3, Version: 1, Candidates: []string{"a", "b"},
		Pools: []PoolRef{{Name: "pool-77", GUID: "77"}}}); err != nil {
		t.Fatal(err)
	}
	b, rec, res, _ := testManager(t, db, "b", map[string]string{})

	// Disks that have reservations but cannot be taken: no import. Writing
	// to a pool another node still holds is what must never happen.
	res.acquireErr = errors.New("2 of 2 disks with reservations could not be reserved for this node")
	if _, err := b.Takeover("g"); err == nil || !strings.Contains(err.Error(), "could not be reserved") {
		t.Fatalf("takeover without the disks: %v", err)
	}
	if got := storage(rec.take()); !reflect.DeepEqual(got, []string{"reserve 77"}) {
		t.Errorf("after a refused takeover: %v", got)
	}
	if g, _ := Get(db, "g"); g.Owner != "a" || g.Epoch != 3 {
		t.Errorf("the group changed hands without the disks: %+v", g)
	}

	res.acquireErr = nil
	g, err := b.Takeover("g")
	if err != nil {
		t.Fatal(err)
	}
	if got := storage(rec.take()); !reflect.DeepEqual(got, []string{"reserve 77", "import -f 77"}) {
		t.Errorf("takeover: %v", got)
	}
	if g.Owner != "b" || g.Epoch != 4 {
		t.Errorf("after the takeover: %+v", g)
	}
}

// A node whose disks were taken by another one says so, once.
func TestShutOutNodeReportsIt(t *testing.T) {
	db := groupsTestDB(t)
	if err := put(db, Group{Name: "g", Topology: Shared, Owner: "a", Epoch: 1, Version: 1, Candidates: []string{"a", "b"},
		Pools: []PoolRef{{Name: "pool-77", GUID: "77"}}}); err != nil {
		t.Fatal(err)
	}
	a, rec, res, _ := testManager(t, db, "a", map[string]string{"pool-77": "77"})
	var alerts []string
	a.Notify = func(level, event, group, message string) { alerts = append(alerts, level+" "+event+" "+group) }

	a.ReservationTick()
	if len(alerts) != 0 {
		t.Fatalf("alert while the disks are this node's: %v", alerts)
	}
	res.lost = true
	a.ReservationTick()
	a.ReservationTick()
	if !reflect.DeepEqual(alerts, []string{"critical ha.fenced g"}) {
		t.Errorf("alerts: %v", alerts)
	}
	// Nothing is taken back.
	for _, e := range rec.take() {
		if strings.HasPrefix(e, "reserve ") {
			t.Errorf("a shut-out node reserved again: %s", e)
		}
	}

	st, err := a.ReservationStatus()
	if err != nil || len(st) != 1 || st[0].Pool != "pool-77" || len(st[0].Disks) != 1 {
		t.Errorf("status: %+v %v", st, err)
	}
}

// Groups that are not on shared disks have nothing to reserve.
func TestNoReservationsForOtherTopologies(t *testing.T) {
	db := groupsTestDB(t)
	a, rec, _, _ := testManager(t, db, "a", map[string]string{"pool-5": "5"})
	if _, err := a.CreateWith("local", Standalone, []string{"pool-5"}, nil, 0, false); err != nil {
		t.Fatal(err)
	}
	a.ReservationTick()
	if got := storage(rec.take()); len(got) != 0 {
		t.Errorf("a standalone group touched reservations: %v", got)
	}
}
