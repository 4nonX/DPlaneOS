package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dplaned/internal/gitops"
	"dplaned/internal/reconciler"
	"dplaned/internal/secrets"
)

func TestAutoApplyVerdict(t *testing.T) {
	item := func(kind gitops.ResourceKind, action gitops.DiffAction, risk string) gitops.DiffItem {
		return gitops.DiffItem{Kind: kind, Name: "x", Action: action, RiskLevel: risk, BlockReason: "in use"}
	}
	for _, c := range []struct {
		name  string
		items []gitops.DiffItem
		ok    bool
		says  string
	}{
		{"nothing to do", []gitops.DiffItem{item(gitops.KindShare, gitops.ActionNOP, "low")}, true, ""},
		{"a new share and a changed dataset", []gitops.DiffItem{item(gitops.KindShare, gitops.ActionCreate, "low"), item(gitops.KindDataset, gitops.ActionModify, "medium")}, true, ""},
		{"a deletion", []gitops.DiffItem{item(gitops.KindShare, gitops.ActionDelete, "low")}, false, "delete"},
		{"something that needs an approval", []gitops.DiffItem{item(gitops.KindDataset, gitops.ActionBlocked, "high")}, false, "approval"},
		{"an ambiguous state", []gitops.DiffItem{item(gitops.KindPool, gitops.ActionAmbiguous, "high")}, false, "by hand"},
		{"a manual step", []gitops.DiffItem{item(gitops.KindDataset, gitops.ActionManual, "low")}, false, "manual"},
		{"a network change", []gitops.DiffItem{item(gitops.KindSystem, gitops.ActionModify, "high")}, false, "high-risk"},
		{"a new pool", []gitops.DiffItem{item(gitops.KindPool, gitops.ActionCreate, "low")}, false, "pools"},
		{"a pool reshape", []gitops.DiffItem{item(gitops.KindPool, gitops.ActionReshape, "medium")}, false, "reshape"},
		{"one bad item among good ones", []gitops.DiffItem{item(gitops.KindShare, gitops.ActionCreate, "low"), item(gitops.KindUser, gitops.ActionDelete, "low")}, false, "user"},
	} {
		ok, reasons := autoApplyVerdict(&gitops.Plan{Items: c.items})
		if ok != c.ok || (!ok && !strings.Contains(strings.Join(reasons, "\n"), c.says)) {
			t.Errorf("%s: %v %v", c.name, ok, reasons)
		}
	}
}

func TestWebhookSignature(t *testing.T) {
	const secret = "a-long-enough-secret"
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	hdr := func(k, v string) http.Header { h := http.Header{}; h.Set(k, v); return h }

	for _, c := range []struct {
		name string
		h    http.Header
		want bool
	}{
		{"GitHub", hdr("X-Hub-Signature-256", "sha256="+sig), true},
		{"Gitea / Forgejo", hdr("X-Gitea-Signature", sig), true},
		{"GitLab", hdr("X-Gitlab-Token", secret), true},
		{"wrong signature", hdr("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64)), false},
		{"wrong token", hdr("X-Gitlab-Token", "something else"), false},
		{"no signature", http.Header{}, false},
	} {
		if got := webhookSignatureOK(secret, body, c.h); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if webhookSignatureOK(secret, []byte("another body"), hdr("X-Hub-Signature-256", "sha256="+sig)) {
		t.Error("a signature was accepted for another body")
	}
	if webhookSignatureOK("", body, hdr("X-Gitlab-Token", "")) {
		t.Error("no secret set, and a call was accepted")
	}
}

// A commit arrives in the repository: with automatic apply on, a harmless one
// is applied, one that deletes something waits, and each is tried once.
func TestAutoApply(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	db := testDB(t)
	if err := secrets.Init(filepath.Join(t.TempDir(), "secrets.key")); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	// System commands are not run in tests, except git in this repository.
	fakeCommands(t, map[string]func([]string) ([]byte, error){
		"git": func(args []string) ([]byte, error) {
			cmd := exec.Command("git", args...)
			cmd.Dir = repo
			return cmd.Output()
		},
	})
	if err := reconciler.EnsureSchema(db); err != nil { // the network tables
		t.Fatal(err)
	}
	var alerts []string
	SetAlertDispatchers(func(level, event, res, msg string) { alerts = append(alerts, level+" "+event) }, nil, nil, nil)
	t.Cleanup(func() { SetAlertDispatchers(nil, nil, nil, nil) })

	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(state string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, "state.yaml"), []byte(state), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "state.yaml")
		git("commit", "-q", "-m", "change")
		return git("rev-parse", "HEAD")
	}
	git("init", "-q")

	h := &GitOpsHandler{db: db, stateYAMLPath: filepath.Join(repo, "state.yaml"), smbConfPath: filepath.Join(t.TempDir(), "smb.conf"), approvals: map[string]bool{}}
	h.detector = gitops.NewDriftDetector(db, h.stateYAMLPath, time.Hour, nil)
	if _, err := db.Exec(`INSERT INTO gitops_config (id, enabled) VALUES (1, 1) ON CONFLICT (id) DO UPDATE SET enabled = 1`); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		res := h.detector.CheckNow()
		if res.Error != "" {
			t.Fatalf("check: %s", res.Error)
		}
		h.autoApply(res)
	}
	groupExists := func(name string) bool {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM groups WHERE name = $1`, name).Scan(&n)
		return n == 1
	}
	settings := func() GitOpsSettings {
		t.Helper()
		r := call(t, h.GetSettings, req{})
		if !r.ok() {
			t.Fatalf("settings: %s", r)
		}
		m := r.body["settings"].(map[string]any)
		str := func(k string) string { s, _ := m[k].(string); return s }
		return GitOpsSettings{LastAutoCommit: str("last_auto_commit"), LastAutoResult: str("last_auto_result"), LastAutoMessage: str("last_auto_message")}
	}

	base := "version: \"1\"\nignore_extraneous: true\npools: []\ndatasets: []\nshares: []\n"
	first := commit(base + "groups:\n  - name: team-a\n    description: \"Team A\"\n")

	// Off: the commit is reported as a difference, and that is all.
	check()
	if groupExists("team-a") {
		t.Fatal("a commit was applied although automatic apply is off")
	}

	if _, err := db.Exec(`UPDATE gitops_config SET auto_apply = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	check()
	if !groupExists("team-a") {
		t.Fatalf("the commit was not applied: %+v, alerts %v", settings(), alerts)
	}
	if s := settings(); s.LastAutoCommit != first || s.LastAutoResult != "applied" {
		t.Errorf("recorded: %+v", s)
	}
	if got := gitops.SyncedCommit(db); got != first {
		t.Errorf("the live system is recorded at %q, the repository at %q", got, first)
	}

	// The next commit drops the group and does not let extra things stay:
	// that deletes something, so it waits for a person.
	second := commit("version: \"1\"\nignore_extraneous: false\npools: []\ndatasets: []\nshares: []\ngroups: []\n")
	alerts = nil
	check()
	if !groupExists("team-a") {
		t.Fatal("a commit that deletes was applied automatically")
	}
	s := settings()
	if s.LastAutoCommit != second || s.LastAutoResult != "held" || !strings.Contains(strings.ToLower(s.LastAutoMessage), "delete") {
		t.Errorf("recorded: %+v", s)
	}
	if len(alerts) != 1 || alerts[0] != "warning "+EventGitOpsApplyHeld {
		t.Errorf("alerts: %v", alerts)
	}
	// It is not tried, or announced, again at the next check.
	check()
	if len(alerts) != 1 {
		t.Errorf("the held commit was announced again: %v", alerts)
	}

	// A further commit is looked at afresh.
	third := commit(base + "groups:\n  - name: team-a\n    description: \"Team A\"\n  - name: team-b\n")
	check()
	if !groupExists("team-b") || settings().LastAutoCommit != third {
		t.Errorf("the next commit was not applied: %+v", settings())
	}
}

func TestGitOpsWebhook(t *testing.T) {
	db := testDB(t)
	if err := secrets.Init(filepath.Join(t.TempDir(), "secrets.key")); err != nil {
		t.Fatal(err)
	}
	fakeCommands(t, nil)
	dir := t.TempDir()
	h := &GitOpsHandler{db: db, stateYAMLPath: filepath.Join(dir, "state.yaml"), approvals: map[string]bool{}}
	h.detector = gitops.NewDriftDetector(db, h.stateYAMLPath, time.Hour, nil)
	if _, err := db.Exec(`INSERT INTO gitops_config (id, enabled) VALUES (1, 1) ON CONFLICT (id) DO UPDATE SET enabled = 1`); err != nil {
		t.Fatal(err)
	}
	body := `{"ref":"refs/heads/main"}`
	sign := func(secret string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(body))
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	post := func(sig string) resp {
		return call(t, h.Webhook, req{method: "POST", body: body, header: map[string]string{"X-Hub-Signature-256": sig}})
	}

	// No secret set: there is no webhook.
	if r := post(sign("whatever-secret-16")); r.code != http.StatusNotFound {
		t.Errorf("without a secret: %s", r)
	}
	if r := call(t, h.UpdateSettings, req{method: "PUT", body: map[string]any{"enabled": true, "state_path": "state.yaml", "webhook_secret": "short"}}); r.code != http.StatusBadRequest {
		t.Errorf("a short secret was accepted: %s", r)
	}
	const secret = "webhook-secret-0123456789"
	if r := call(t, h.UpdateSettings, req{method: "PUT", body: map[string]any{"enabled": true, "state_path": "state.yaml", "auto_apply": true, "webhook_secret": secret}}); !r.ok() {
		t.Fatalf("saving: %s", r)
	}
	var stored string
	_ = db.QueryRow(`SELECT webhook_secret FROM gitops_config WHERE id = 1`).Scan(&stored)
	if stored == "" || strings.Contains(stored, secret) {
		t.Errorf("the secret is stored as %q", stored)
	}
	r := call(t, h.GetSettings, req{})
	if strings.Contains(r.raw, secret) || !strings.Contains(r.raw, `"has_webhook_secret":true`) || !strings.Contains(r.raw, `"auto_apply":true`) {
		t.Errorf("settings: %s", r)
	}
	// Saving other settings leaves the secret alone.
	if r := call(t, h.UpdateSettings, req{method: "PUT", body: map[string]any{"enabled": true, "state_path": "state.yaml", "auto_apply": true}}); !r.ok() {
		t.Fatalf("saving: %s", r)
	}

	if r := post(sign("another-secret-0123456789")); r.code != http.StatusUnauthorized {
		t.Errorf("a wrong signature: %s", r)
	}
	if r := post(sign(secret)); r.code != http.StatusAccepted {
		t.Errorf("a signed call: %s", r)
	}
	// Switched off: no webhook.
	if _, err := db.Exec(`UPDATE gitops_config SET enabled = 0 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if r := post(sign(secret)); r.code != http.StatusNotFound {
		t.Errorf("while GitOps is off: %s", r)
	}
	time.Sleep(200 * time.Millisecond) // the check started by the accepted call
}
