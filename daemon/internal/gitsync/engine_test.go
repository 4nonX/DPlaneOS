package gitsync

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"dplaned/internal/configstore"
	"dplaned/internal/database"
	"dplaned/internal/gitops"
	"dplaned/internal/secrets"
)

func engineDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set: database-backed test")
	}
	needGit(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "gs_" + hex.EncodeToString(b)
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
	if _, err := db.Exec(`INSERT INTO gitops_config (id, enabled) VALUES (1, 0) ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Init(filepath.Join(t.TempDir(), "secrets.key")); err != nil {
		t.Fatal(err)
	}
	return db
}

// record inserts a revision of this node (as a capture would).
func record(t *testing.T, db *sql.DB, kind, key, scope string, payload map[string]any) {
	t.Helper()
	var raw any
	if payload != nil {
		b, _ := json.Marshal(payload)
		raw = b
	}
	if _, err := db.Exec(`INSERT INTO config_revisions (changeset, scope_type, scope_id, resource_kind, resource_key, payload, origin, origin_node, author)
		VALUES ('t', $1, '', $2, $3, $4, 'gui', 'nas1', 'admin')`, scope, kind, key, raw); err != nil {
		t.Fatal(err)
	}
}

type testEngine struct {
	*Engine
	db     *sql.DB
	remote string
	owner  bool
	mu     sync.Mutex
	alerts []string
}

func newTestEngine(t *testing.T, mutate func(*Settings)) *testEngine {
	db := engineDB(t)
	te := &testEngine{db: db, remote: bare(t), owner: true}
	s, _ := Load(db)
	s.Enabled, s.Layout, s.Branch, s.Policy, s.Provider, s.IntervalSecs = true, LayoutScoped, "main", PolicyDirect, ProviderNone, 300
	s.Remotes = []RemoteSetting{{URL: "file://" + te.remote}}
	s.Categories = map[string]bool{CatStorage: true, CatAccess: true, CatIdentity: true, CatSystem: true, CatApps: true, CatProtect: true}
	if mutate != nil {
		mutate(s)
	}
	if err := Save(db, s, "nas1"); err != nil {
		t.Fatal(err)
	}
	te.Engine = New(Env{
		DB: db, Syncer: configstore.NewSyncer(db, gitops.ApplyContext{DB: db}), Dir: t.TempDir(),
		NodeName: func() string { return "nas1" },
		IsOwner:  func(string) (bool, string) { return te.owner, "test" },
		GroupOf:  func(string) string { return "" },
		Groups:   func() []string { return nil },
		Paired:   func() bool { return false },
		Alert: func(level, event, resource, msg string) {
			te.mu.Lock()
			te.alerts = append(te.alerts, msg)
			te.mu.Unlock()
		},
	})
	return te
}

func (te *testEngine) scope(t *testing.T, sc string) ScopeStatus {
	t.Helper()
	for _, s := range te.Status().Scopes {
		if s.Scope == sc {
			return s
		}
	}
	t.Fatalf("no scope %s in %+v", sc, te.Status())
	return ScopeStatus{}
}

// remoteFile reads a file at the remote branch head.
func remoteFile(t *testing.T, remote, path string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", remote, "show", "main:"+path).CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

// commitRemote writes a file on the remote branch as someone else would.
func commitRemote(t *testing.T, remote, path, content string) string {
	t.Helper()
	c := &Clone{Dir: filepath.Join(t.TempDir(), "other"), Remotes: []Remote{{Name: "r0", URL: remote}}}
	if err := c.Ensure(); err != nil {
		t.Fatal(err)
	}
	head, _, err := c.FetchBranch("main")
	if err != nil {
		t.Fatal(err)
	}
	sha, err := c.Commit(head, map[string]string{path: content}, "edited in the repository", Identity{Name: "ops", Email: "ops@x"})
	if err != nil {
		t.Fatal(err)
	}
	if r := c.Push(sha, "main", false); !r[0].OK {
		t.Fatalf("push: %+v", r)
	}
	return sha
}

var alice = map[string]any{"username": "alice", "email": "a@x.lan", "role": "admin", "active": true, "credentials": "sha256:alicefp"}
var bob = map[string]any{"username": "bob", "email": "b@x.lan", "role": "user", "active": true}

func TestEnginePushesLocalChangesWithoutSecrets(t *testing.T) {
	te := newTestEngine(t, nil)
	record(t, te.db, "user", "alice", "cluster", alice)
	record(t, te.db, "user", "bob", "cluster", bob)
	if err := te.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	file := remoteFile(t, te.remote, "cluster/state.yaml")
	if !strings.Contains(file, `username: "alice"`) || !strings.Contains(file, `username: "bob"`) {
		t.Fatalf("pushed file:\n%s", file)
	}
	if strings.Contains(file, "alicefp") || !strings.Contains(file, "secret://user/alice/password") {
		t.Fatalf("the secret must stay on the node:\n%s", file)
	}
	sc := te.scope(t, ScopeCluster)
	if len(sc.Push) != 0 || sc.SyncedSHA == "" || sc.Error != "" {
		t.Fatalf("after the push: %+v", sc)
	}
	// Nothing changed: no new commit.
	before := sc.SyncedSHA
	if err := te.Run(); err != nil {
		t.Fatal(err)
	}
	if te.scope(t, ScopeCluster).SyncedSHA != before {
		t.Error("a cycle without changes must not commit")
	}
	// A password change on this node is not a change for the repository.
	changed := map[string]any{}
	for k, v := range alice {
		changed[k] = v
	}
	changed["credentials"] = "sha256:newfp"
	record(t, te.db, "user", "alice", "cluster", changed)
	if err := te.Run(); err != nil {
		t.Fatal(err)
	}
	if sc := te.scope(t, ScopeCluster); sc.SyncedSHA != before || len(sc.Push) != 0 {
		t.Fatalf("password change pushed: %+v", sc)
	}
}

func TestEngineRepositoryChangeWaitsForApproval(t *testing.T) {
	te := newTestEngine(t, nil)
	record(t, te.db, "user", "bob", "cluster", bob)
	if err := te.Run(); err != nil {
		t.Fatal(err)
	}
	file := remoteFile(t, te.remote, "cluster/state.yaml")
	head := commitRemote(t, te.remote, "cluster/state.yaml", strings.Replace(file, "b@x.lan", "bob@ops.lan", 1))
	if err := te.Run(); err != nil {
		t.Fatal(err)
	}
	sc := te.scope(t, ScopeCluster)
	if len(sc.Pull) != 1 || sc.Pull[0].ID != "user/bob" || sc.Pull[0].Remote.Payload["email"] != "bob@ops.lan" {
		t.Fatalf("pending change: %+v", sc.Pull)
	}
	if sc.SyncedSHA == head || len(sc.Applied) != 0 {
		t.Fatalf("nothing may be applied without approval: %+v", sc)
	}
	if te.Status().Head != head {
		t.Errorf("head %s, want %s", te.Status().Head, head)
	}
	if err := te.Approve(ScopeCluster, "0000000000000000000000000000000000000000"); err == nil {
		t.Error("approving another commit than the one shown must fail")
	}
}

func TestEngineEmptyStateGuardHolds(t *testing.T) {
	te := newTestEngine(t, func(s *Settings) { s.AutoApply, s.Prune = true, true })
	record(t, te.db, "user", "alice", "cluster", alice)
	record(t, te.db, "user", "bob", "cluster", bob)
	if err := te.Run(); err != nil {
		t.Fatal(err)
	}
	good := te.scope(t, ScopeCluster).SyncedSHA
	commitRemote(t, te.remote, "cluster/state.yaml", "version: \"1\"\n")
	_ = te.Run()
	sc := te.scope(t, ScopeCluster)
	if sc.Hold == nil || !strings.Contains(sc.Hold.Reason, "empty-state guard") || sc.Hold.GoodSHA != good {
		t.Fatalf("hold: %+v", sc.Hold)
	}
	if len(sc.Applied) != 0 || len(sc.Pull) != 2 {
		t.Fatalf("nothing may be deleted: %+v", sc)
	}
	if len(te.alerts) != 1 {
		t.Errorf("alerts: %v", te.alerts)
	}
	// Revert writes the good version back and lifts the hold.
	if err := te.Revert(ScopeCluster, "admin"); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if f := remoteFile(t, te.remote, "cluster/state.yaml"); !strings.Contains(f, `username: "bob"`) {
		t.Fatalf("after revert:\n%s", f)
	}
	sc = te.scope(t, ScopeCluster)
	if sc.Hold != nil || len(sc.Pull) != 0 {
		t.Fatalf("after revert: %+v", sc)
	}
	if h, _ := OpenHold(te.db, ScopeCluster); h != nil {
		t.Errorf("hold still open: %+v", h)
	}
}

func TestEngineRefusesUnsignedCommits(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	key := filepath.Join(t.TempDir(), "k")
	exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).Run()
	pub, _ := os.ReadFile(key + ".pub")
	te := newTestEngine(t, func(s *Settings) {
		s.AutoApply, s.VerifySignatures, s.SignCommits = true, true, true
		s.AllowedSigners = string(pub)
	})
	// This node signs its own commits with its key: allow it too.
	pk, err := te.signingKey()
	if err != nil {
		t.Fatal(err)
	}
	s, _ := Load(te.db)
	s.AllowedSigners += "\n" + pk
	if err := Save(te.db, s, "nas1"); err != nil {
		t.Fatal(err)
	}
	record(t, te.db, "user", "bob", "cluster", bob)
	if err := te.Run(); err != nil {
		t.Fatalf("own signed commit: %v", err)
	}
	file := remoteFile(t, te.remote, "cluster/state.yaml")
	commitRemote(t, te.remote, "cluster/state.yaml", strings.Replace(file, "b@x.lan", "evil@x.lan", 1))
	_ = te.Run()
	sc := te.scope(t, ScopeCluster)
	if sc.Hold == nil || !strings.Contains(sc.Hold.Reason, "not signed by an allowed key") {
		t.Fatalf("unsigned commit taken: %+v", sc)
	}
}

func TestEngineOnlyTheOwnerWrites(t *testing.T) {
	te := newTestEngine(t, nil)
	te.owner = false
	record(t, te.db, "user", "bob", "cluster", bob)
	if err := te.Run(); err != nil {
		t.Fatal(err)
	}
	if f := remoteFile(t, te.remote, "cluster/state.yaml"); f != "" {
		t.Fatalf("a node that does not own the scope pushed:\n%s", f)
	}
	if sc := te.scope(t, ScopeCluster); sc.Owner || sc.OwnerReason == "" {
		t.Fatalf("owner: %+v", sc)
	}
}

func TestEnginePullRequestDeclinedIsNotReopened(t *testing.T) {
	var mu sync.Mutex
	state, created := "open", 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/repos/acme/nas/pulls":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == "POST" && r.URL.Path == "/api/v1/repos/acme/nas/pulls":
			created++
			_ = json.NewEncoder(w).Encode(map[string]any{"number": created, "html_url": "u", "state": "open"})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/repos/acme/nas/pulls/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"number": created, "state": state, "merged": false})
		default:
			w.WriteHeader(201)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer api.Close()
	te := newTestEngine(t, func(s *Settings) {
		s.Policy, s.Provider, s.ProviderAPIURL, s.ProviderRepo, s.ProviderCred = PolicyPullRequest, ProviderGitea, api.URL+"/api/v1", "acme/nas", "forge"
	})
	if err := SaveCredential(te.db, &Credential{Name: "forge", AuthType: "token", Token: "t0k"}); err != nil {
		t.Fatal(err)
	}
	record(t, te.db, "user", "bob", "cluster", bob)
	if err := te.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	sc := te.scope(t, ScopeCluster)
	if sc.PR == nil || sc.PR.Number != 1 || len(sc.Push) != 1 {
		t.Fatalf("pull request: %+v", sc)
	}
	if f := remoteFile(t, te.remote, "cluster/state.yaml"); f != "" {
		t.Fatalf("the branch itself must not change under the pull request policy:\n%s", f)
	}
	out, _ := exec.Command("git", "--git-dir", te.remote, "show", "dplaneos/cluster:cluster/state.yaml").CombinedOutput()
	if !strings.Contains(string(out), `username: "bob"`) {
		t.Fatalf("pull request branch:\n%s", out)
	}
	// Reviewers close it without merging: not proposed again.
	mu.Lock()
	state = "closed"
	mu.Unlock()
	_ = te.Run()
	_ = te.Run()
	sc = te.scope(t, ScopeCluster)
	if !sc.Declined || created != 1 {
		t.Fatalf("declined: %+v, created %d", sc, created)
	}
}
