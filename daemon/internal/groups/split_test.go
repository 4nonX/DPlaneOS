package groups

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestToKeepAndSplitName(t *testing.T) {
	ds := []SplitDataset{
		{"tank/data", 0},
		{"tank", 4096},
		{"tank/new", -1},
		{"tank/dplane-split-20261015-010203-from-b", 512}, // an earlier kept copy
		{"tank/dplane-split-20261015-010203-from-b/data", 512},
		{"tankx", 10}, // another pool with the same prefix
		{"tank/data/sub", 100},
	}
	var names []string
	for _, d := range toKeep("tank", ds) {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "tank,tank/data/sub,tank/new" {
		t.Fatalf("toKeep: %v", names)
	}
	n := splitName(time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC), "NAS_b.lan")
	if n != "dplane-split-20261015-010203-from-nas-b-lan" || !splitNameRe.MatchString(n) {
		t.Fatalf("name %q", n)
	}
}

// splitTransport answers snapshot listings and records kept datasets.
type splitTransport struct {
	fakeTransport
	mu     sync.Mutex
	remote []string
	sent   []string // "node pool name rel base"
}

func (s *splitTransport) RemoteSnapshots(string, string, string) ([]string, error) {
	return s.remote, nil
}
func (s *splitTransport) SendSplit(node, group, pool, name, rel, base string, r io.Reader) error {
	_, _ = io.Copy(io.Discard, r)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, strings.Join([]string{node, pool, name, rel, base}, " "))
	return nil
}

func splitManager(t *testing.T, self, owner string, tr Transport) (*Manager, *[]string) {
	t.Helper()
	db := groupsTestDB(t)
	m := NewManager(db, Ops{}, tr, func() string { return self }, func() View { return View{} })
	m.repl.Snapshots = func(string) ([]string, error) { return []string{"dplane-1-1", "dplane-1-2"}, nil }
	var calls []string
	var mu sync.Mutex
	rec := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, s)
	}
	m.split = SplitOps{
		Snapshot: func(pool, snap string) error { rec("snapshot " + pool + "@" + snap); return nil },
		Changed: func(pool, base string) ([]SplitDataset, error) {
			rec("changed " + pool + " " + base)
			return []SplitDataset{{"tank", 4096}, {"tank/data", 0}, {"tank/new", -1}}, nil
		},
		Send: func(_ context.Context, ds, base, snap string) (io.ReadCloser, func() error, error) {
			rec("send " + ds + " " + base + " " + snap)
			return io.NopCloser(strings.NewReader("stream")), func() error { return nil }, nil
		},
		Recv: func(target, origin string, r io.Reader) error {
			_, _ = io.Copy(io.Discard, r)
			rec("recv " + target + " " + origin)
			return nil
		},
		Create:  func(ds string) error { rec("create " + ds); return nil },
		List:    func(string) ([]SplitCopy, error) { return []SplitCopy{}, nil },
		Destroy: func(pool, name string) error { rec("destroy " + pool + "/" + name); return nil },
	}
	g := Group{Name: "data", Topology: Replicated, Pools: []PoolRef{{Name: "tank", GUID: "11"}}, Candidates: []string{"ka", "kb"},
		Owner: owner, Epoch: 2, Version: 1, UpdatedAt: time.Now(), UpdatedBy: owner, IntervalSecs: 3600}
	if err := put(db, g); err != nil {
		t.Fatal(err)
	}
	return m, &calls
}

func discardOK(t *testing.T, m *Manager) bool {
	t.Helper()
	var ok bool
	_ = m.db.QueryRow(`SELECT COALESCE(bool_or(discard_ok), FALSE) FROM group_replication WHERE group_name = 'data'`).Scan(&ok)
	return ok
}

func TestDiscardKeepsTheChangesOnTheOwner(t *testing.T) {
	tr := &splitTransport{remote: []string{"dplane-1-1", "dplane-1-2"}}
	m, calls := splitManager(t, "ka", "kb", tr)
	kept, err := m.DiscardDivergent("data", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || !strings.HasPrefix(kept[0], "tank/dplane-split-") {
		t.Fatalf("kept %v", kept)
	}
	name := strings.TrimPrefix(kept[0], "tank/")
	// The root changed since the common snapshot: sent as changes from it;
	// tank/new did not exist then: sent whole; tank/data is unchanged.
	want := []string{"kb tank " + name + "  dplane-1-2", "kb tank " + name + " /new "}
	if strings.Join(tr.sent, "|") != strings.Join(want, "|") {
		t.Fatalf("sent %v, want %v", tr.sent, want)
	}
	if !strings.HasPrefix((*calls)[0], "changed tank dplane-1-2") || !strings.HasPrefix((*calls)[1], "snapshot tank@split-") {
		t.Fatalf("calls %v", *calls)
	}
	if !discardOK(t, m) {
		t.Fatal("the copy must be released for the rollback once the changes are kept")
	}
}

// This node's newest snapshot never reached the owner (taken just before
// it failed): the changes are kept from the newest snapshot both have.
func TestDiscardKeepsTheChangesFromTheNewestCommonSnapshot(t *testing.T) {
	tr := &splitTransport{remote: []string{"dplane-1-1", "dplane-2-3"}}
	m, calls := splitManager(t, "ka", "kb", tr)
	if _, err := m.DiscardDivergent("data", true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix((*calls)[0], "changed tank dplane-1-1") || len(tr.sent) != 2 || !strings.HasSuffix(tr.sent[0], " dplane-1-1") {
		t.Fatalf("calls %v, sent %v", *calls, tr.sent)
	}
}

func TestDiscardDoesNothingWhenTheChangesCannotBeKept(t *testing.T) {
	tr := &splitTransport{remote: []string{"dplane-2-3"}} // the owner pruned every common snapshot
	m, _ := splitManager(t, "ka", "kb", tr)
	_, err := m.DiscardDivergent("data", true)
	if !errors.Is(err, ErrCannotKeep) || len(tr.sent) != 0 || discardOK(t, m) {
		t.Fatalf("err %v, sent %v, discard %v", err, tr.sent, discardOK(t, m))
	}
	// The operator may still discard without keeping.
	if _, err := m.DiscardDivergent("data", false); err != nil || !discardOK(t, m) {
		t.Fatalf("discard without keeping: %v", err)
	}
}

func TestReceiveSplitOnTheOwner(t *testing.T) {
	m, calls := splitManager(t, "kb", "kb", &splitTransport{})
	name := "dplane-split-20261015-010203-from-a"
	for _, c := range []struct{ sender, name, rel, base string }{
		{"kz", name, "", "dplane-1-2"},          // not a node of the group
		{"ka", "../etc", "", "dplane-1-2"},      // invalid name
		{"ka", name, "/../x", "dplane-1-2"},     // invalid path
		{"ka", name, "", "split-20261015-0102"}, // not a replication snapshot
	} {
		if err := m.ReceiveSplit(c.sender, "data", "tank", c.name, c.rel, c.base, strings.NewReader("")); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	if err := m.ReceiveSplit("ka", "data", "tank", name, "/data/sub", "dplane-1-2", strings.NewReader("s")); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(*calls, "|")
	want := "create tank/" + name + "/data|recv tank/" + name + "/data/sub tank/data/sub@dplane-1-2"
	if got != want {
		t.Fatalf("calls %s, want %s", got, want)
	}
	// Only the owner deletes kept copies.
	if err := m.DeleteSplit("data", "tank", name); err != nil {
		t.Fatal(err)
	}
	other, _ := splitManager(t, "ka", "kb", &splitTransport{})
	if err := other.DeleteSplit("data", "tank", name); err == nil {
		t.Error("a non-owner deleted a kept copy")
	}
}

func TestPruningSkipsTheOriginOfAKeptCopy(t *testing.T) {
	if !isDependentClones(errors.New("destroy snapshot: exit status 1: cannot destroy 'tank@dplane-1-2': snapshot has dependent clones")) {
		t.Fatal("not recognised")
	}
	if isDependentClones(errors.New("dataset is busy")) || isDependentClones(nil) {
		t.Fatal("false positive")
	}
}

// Two replication runs of the same group never overlap: the second waits.
func TestReplicationRunsOneAtATime(t *testing.T) {
	m, _ := splitManager(t, "kb", "kb", &splitTransport{})
	var mu sync.Mutex
	running, overlap := 0, false
	m.repl.Snapshot = func(string, string) error {
		mu.Lock()
		running++
		if running > 1 {
			overlap = true
		}
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return nil
	}
	g, err := Get(m.db, "data")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = m.replicate(*g, []string{}) }()
	}
	wg.Wait()
	if overlap {
		t.Fatal("replication runs overlapped")
	}
}

// unreachableTransport: a copy that does not answer.
type unreachableTransport struct{ splitTransport }

func (u *unreachableTransport) RemoteSnapshots(string, string, string) ([]string, error) {
	return nil, errors.New("connection refused")
}

// A copy that is down for many intervals keeps its last known snapshot on
// the owner: here, the one this node received from it when it owned the
// group (failover), so the split there can be kept when it comes back.
func TestPruningKeepsTheSnapshotAnUnreachableCopyHas(t *testing.T) {
	m, _ := splitManager(t, "kb", "kb", &unreachableTransport{})
	local := []string{"dplane-1-1", "dplane-1-2", "dplane-2-3", "dplane-2-4", "dplane-2-5", "dplane-2-6"}
	m.repl.Snapshot = func(string, string) error { return nil }
	m.repl.Snapshots = func(string) ([]string, error) { return local, nil }
	var destroyed []string
	m.repl.Destroy = func(_, s string) error { destroyed = append(destroyed, s); return nil }
	recordRepl(m.db, "data", "in", "ka", "tank", "dplane-1-2", nil)
	g, err := Get(m.db, "data")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.replicate(*g, nil); err == nil {
		t.Fatal("replication to a copy that does not answer must fail")
	}
	if strings.Join(destroyed, ",") != "dplane-1-1,dplane-2-3" {
		t.Fatalf("destroyed %v: dplane-1-2 (ka's) and the newest three stay", destroyed)
	}
}
