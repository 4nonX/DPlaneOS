package handlers

import (
	"path/filepath"
	"strings"
	"testing"

	"dplaned/internal/secrets"
)

func TestGitSyncWebUIContract(t *testing.T) {
	db := testDB(t)
	if err := secrets.Init(filepath.Join(t.TempDir(), "secrets.key")); err != nil {
		t.Fatal(err)
	}
	h := NewGitReposHandler(db)

	// What the GitOps page sends for a credential.
	r := call(t, h.SaveCredential, req{method: "POST", body: map[string]any{"name": "gh", "host": "github.com", "auth_type": "token", "token": "ghp_x"}})
	if !r.ok() {
		t.Fatalf("save credential: %s", r)
	}
	credID := int(r.body["id"].(float64))
	list := call(t, h.ListCredentials, req{})
	if !strings.Contains(list.raw, `"auth_type":"token"`) || strings.Contains(list.raw, "ghp_x") {
		t.Errorf("credential list: %s", list)
	}

	// A repo saved without "enabled" is enabled, also after a later save.
	r = call(t, h.SaveRepo, req{method: "POST", body: map[string]any{"name": "infra", "repo_url": "https://github.com/acme/infra.git", "credential_id": credID}})
	if !r.ok() {
		t.Fatalf("save repo: %s", r)
	}
	id := int(r.body["id"].(float64))
	if r := call(t, h.SaveRepo, req{method: "POST", body: map[string]any{"id": id, "name": "infra", "repo_url": "https://github.com/acme/infra.git", "branch": "dev"}}); !r.ok() {
		t.Fatalf("update repo: %s", r)
	}
	var enabled int
	_ = db.QueryRow(`SELECT enabled FROM git_sync_repos WHERE id = $1`, id).Scan(&enabled)
	if enabled != 1 {
		t.Error("saving without enabled disabled the repository")
	}
	if r := call(t, h.SaveRepo, req{method: "POST", body: map[string]any{"id": id, "name": "infra", "repo_url": "https://github.com/acme/infra.git", "enabled": false}}); !r.ok() {
		t.Fatalf("disable: %s", r)
	}
	_ = db.QueryRow(`SELECT enabled FROM git_sync_repos WHERE id = $1`, id).Scan(&enabled)
	if enabled != 0 {
		t.Error("enabled=false not stored")
	}

	// Testing a credential with no repository linked explains what is needed.
	r2 := call(t, h.SaveCredential, req{method: "POST", body: map[string]any{"name": "unused", "auth_type": "token", "token": "x"}})
	unused := int(r2.body["id"].(float64))
	if r := call(t, h.TestCredential, req{method: "POST", body: map[string]any{"credential_id": unused}}); r.ok() || !strings.Contains(r.raw, "Link this credential") {
		t.Errorf("test without repository: %s", r)
	}
}
