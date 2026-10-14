package gitsync

import (
	"strings"
	"testing"

	"dplaned/internal/configstore"
)

func TestClassify(t *testing.T) {
	b := &Base{UID: "u1", Hash: "h1"}
	cases := []struct {
		base          *Base
		local, remote string
		want          Action
	}{
		{b, "h1", "h1", InSync},
		{b, "h2", "h1", Push},     // changed here
		{b, "h1", "h2", Pull},     // changed in the repository
		{b, "h2", "h3", Conflict}, // changed on both
		{b, "h2", "h2", InSync},   // same change on both
		{b, "", "h1", Push},       // deleted here
		{b, "h1", "", Pull},       // deleted in the repository
		{nil, "h1", "", Push},     // new here
		{nil, "", "h1", Pull},     // new in the repository
		{nil, "h1", "h2", Conflict},
	}
	for i, c := range cases {
		if got := Classify(c.base, c.local, c.remote); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
}

func rev(kind, key string, payload map[string]any) configstore.Revision {
	return configstore.Revision{UID: kind + "/" + key + "-uid", Kind: kind, Key: key, Scope: configstore.ScopeCluster, Payload: payload}
}

func res(kind, key string, payload map[string]any) configstore.Resource {
	return configstore.Resource{Kind: kind, Key: key, Scope: configstore.ScopeCluster, Payload: payload}
}

func baseOf(rs ...configstore.Resource) map[string]Base {
	out := map[string]Base{}
	for _, r := range rs {
		out[r.ID()] = Base{UID: r.ID() + "-base", Hash: Hash(r)}
	}
	return out
}

func TestCompareScope(t *testing.T) {
	alice := map[string]any{"username": "alice", "email": "a@x", "credentials": "sha256:aaa"}
	alice2 := map[string]any{"username": "alice", "email": "a2@x", "credentials": "sha256:aaa"}
	bob := map[string]any{"username": "bob", "email": "b@x", "credentials": "sha256:bbb"}
	bobGit := map[string]any{"username": "bob", "email": "b@git", "password_hash": "secret://user/bob/password"}
	carol := map[string]any{"username": "carol", "email": "c@x"}
	staff := map[string]any{"name": "staff", "gid": 3000}

	base := baseOf(res("user", "alice", alice), res("user", "bob", bob), res("group", "staff", staff))
	local := map[string]configstore.Revision{
		"user/alice":  rev("user", "alice", alice2), // changed here
		"user/bob":    rev("user", "bob", bob),      // unchanged here
		"user/carol":  rev("user", "carol", carol),  // new here
		"group/staff": rev("group", "staff", staff),
	}
	remote := []configstore.Resource{
		res("user", "alice", map[string]any{"username": "alice", "email": "a@x", "password_hash": "secret://user/alice/password"}),
		res("user", "bob", bobGit), // changed in the repository
		// staff deleted in the repository
	}
	p := Compare(local, remote, base)
	if strings.Join(p.Push, ",") != "user/alice,user/carol" {
		t.Errorf("push: %v", p.Push)
	}
	if len(p.Pull) != 2 || p.Pull[0].ID != "group/staff" || !p.Pull[0].Delete || p.Pull[1].ID != "user/bob" {
		t.Fatalf("pull: %+v", p.Pull)
	}
	// The repository's bob keeps this node's credentials (the file only refers to them).
	if got := p.Pull[1].Remote.Payload; got["credentials"] != "sha256:bbb" || got["password_hash"] != nil || got["email"] != "b@git" {
		t.Errorf("bob from the repository: %v", got)
	}
	// Built on this node's current revision: a fast-forward in the merge engine.
	if p.Pull[1].BaseUID != "user/bob-uid" {
		t.Errorf("base uid: %q", p.Pull[1].BaseUID)
	}
	if len(p.Conflicts) != 0 || len(p.InSync) != 0 {
		t.Errorf("conflicts %v in sync %v", p.Conflicts, p.InSync)
	}
	// The only group is deleted by the repository: guard.
	if strings.Join(p.EmptyKinds, ",") != "group" {
		t.Errorf("empty kinds: %v", p.EmptyKinds)
	}
}

func TestCompareSecretsNeverCount(t *testing.T) {
	// The password changed on this node: the repository only holds a
	// reference, so there is nothing to push.
	before := map[string]any{"username": "alice", "email": "a@x", "credentials": "sha256:old"}
	after := map[string]any{"username": "alice", "email": "a@x", "credentials": "sha256:new"}
	base := baseOf(res("user", "alice", before))
	remote := []configstore.Resource{res("user", "alice", map[string]any{"username": "alice", "email": "a@x", "password_hash": "secret://user/alice/password"})}
	p := Compare(map[string]configstore.Revision{"user/alice": rev("user", "alice", after)}, remote, base)
	if len(p.InSync) != 1 || len(p.Push)+len(p.Pull)+len(p.Conflicts) != 0 {
		t.Fatalf("plan: %+v", p)
	}
}

func TestCompareConflict(t *testing.T) {
	v1 := map[string]any{"name": "media", "path": "/mnt/tank/media", "comment": "v1"}
	here := map[string]any{"name": "media", "path": "/mnt/tank/media", "comment": "here"}
	there := map[string]any{"name": "media", "path": "/mnt/tank/media", "comment": "there"}
	base := baseOf(res("share", "media", v1))
	p := Compare(map[string]configstore.Revision{"share/media": rev("share", "media", here)}, []configstore.Resource{res("share", "media", there)}, base)
	if len(p.Conflicts) != 1 || p.Conflicts[0].Remote.Payload["comment"] != "there" {
		t.Fatalf("conflict: %+v", p)
	}
	if len(p.EmptyKinds) != 0 {
		t.Errorf("guard: %v", p.EmptyKinds)
	}
}

func TestScopePath(t *testing.T) {
	for _, c := range []struct{ layout, statePath, scope, want string }{
		{LayoutSingle, "", ScopeAll, "state.yaml"},
		{LayoutSingle, "infra/nas.yaml", ScopeAll, "infra/nas.yaml"},
		{LayoutScoped, "", ScopeCluster, "cluster/state.yaml"},
		{LayoutScoped, "", GroupScope("data"), "groups/data/state.yaml"},
		{LayoutScoped, "", NodeScope("nas1"), "nodes/nas1/state.yaml"},
	} {
		got, err := ScopePath(c.layout, c.statePath, c.scope)
		if err != nil || got != c.want {
			t.Errorf("%+v: %q %v", c, got, err)
		}
	}
	for _, bad := range []struct{ layout, statePath, scope string }{
		{LayoutSingle, "../etc/x.yaml", ScopeAll},
		{LayoutSingle, "/abs.yaml", ScopeAll},
		{LayoutSingle, "state.txt", ScopeAll},
		{LayoutSingle, "", ScopeCluster},
		{LayoutScoped, "", "group:../x"},
		{LayoutScoped, "", "node:"},
	} {
		if _, err := ScopePath(bad.layout, bad.statePath, bad.scope); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestScopeOf(t *testing.T) {
	groupOf := func(pool string) string {
		if pool == "tank" {
			return "data"
		}
		return ""
	}
	for _, c := range []struct {
		r    configstore.Resource
		want string
	}{
		{configstore.Resource{Kind: "user", Scope: configstore.ScopeCluster}, ScopeCluster},
		{configstore.Resource{Kind: "dataset", Scope: configstore.ScopeGroup, ScopeID: "tank"}, GroupScope("data")},
		{configstore.Resource{Kind: "dataset", Scope: configstore.ScopeGroup, ScopeID: "scratch"}, NodeScope("nas1")},
		{configstore.Resource{Kind: "system", Scope: configstore.ScopeNode}, NodeScope("nas1")},
	} {
		if got := ScopeOf(LayoutScoped, c.r, groupOf, "nas1"); got != c.want {
			t.Errorf("%+v: %s, want %s", c.r, got, c.want)
		}
	}
	if got := ScopeOf(LayoutSingle, configstore.Resource{Kind: "user", Scope: configstore.ScopeCluster}, groupOf, "nas1"); got != ScopeAll {
		t.Errorf("single: %s", got)
	}
}

func TestRenderParseRoundTripKeepsSecretsOut(t *testing.T) {
	alice := configstore.Resource{Kind: "user", Key: "alice", Scope: configstore.ScopeCluster,
		Payload: map[string]any{"username": "alice", "email": "a@x", "role": "admin", "active": true, "credentials": "sha256:secretfp"}}
	staff := configstore.Resource{Kind: "group", Key: "staff", Scope: configstore.ScopeCluster,
		Payload: map[string]any{"name": "staff", "description": "", "gid": 3000, "members": []any{"alice"}}}
	out, err := Render([]configstore.Resource{staff, alice})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "secretfp") || !strings.Contains(out, "secret://user/alice/password") {
		t.Fatalf("rendered file:\n%s", out)
	}
	back, err := Parse(out, "nas1")
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	byID := map[string]configstore.Resource{}
	for _, r := range back {
		byID[r.ID()] = r
	}
	got := FromGit(byID["user/alice"], alice.Payload)
	if Hash(got) != Hash(alice) || got.Payload["credentials"] != "sha256:secretfp" {
		t.Errorf("alice after the round trip: %v", got.Payload)
	}
	if Hash(byID["group/staff"]) != Hash(staff) {
		t.Errorf("staff after the round trip: %v vs %v", byID["group/staff"].Payload, staff.Payload)
	}
}

func TestManagedCategories(t *testing.T) {
	cats := map[string]bool{CatIdentity: true}
	if !Managed("user", cats) || Managed("dataset", cats) || Managed("unknown", cats) {
		t.Error("categories")
	}
}

func TestCheckRoundTrip(t *testing.T) {
	acme := configstore.Resource{Kind: "acme", Key: "acme", Scope: configstore.ScopeCluster,
		Payload: map[string]any{"email": "ops@x.lan", "server": "https://acme", "domains": []any{"nas.x.lan"},
			"resolver": "http", "dns_config": map[string]any{}, "enabled": false}}
	out, err := Render([]configstore.Resource{acme})
	if err != nil {
		t.Fatal(err)
	}
	// A disabled ACME configuration is still written and read back.
	if err := CheckRoundTrip(out, "nas1", []configstore.Resource{acme}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := CheckRoundTrip("version: \"1\"\n", "nas1", []configstore.Resource{acme}); err == nil {
		t.Error("a file that lost the resource must be refused")
	}
}

// A resource read from the repository and written back unchanged (it rides
// along in a file with another resource's change) keeps its reference.
func TestToGitKeepsReferencesOfRepositoryVersions(t *testing.T) {
	fromRepo := configstore.Resource{Kind: "user", Key: "admin", Scope: configstore.ScopeCluster,
		Payload: map[string]any{"username": "admin", "email": "a@x", "password_hash": "secret://user/admin/password"}}
	if got := ToGit(fromRepo).Payload["password_hash"]; got != "secret://user/admin/password" {
		t.Fatalf("reference lost: %v", got)
	}
	local := configstore.Resource{Kind: "user", Key: "admin", Scope: configstore.ScopeCluster,
		Payload: map[string]any{"username": "admin", "email": "a@x", "password_hash": "", "credentials": "sha256:fp"}}
	if Hash(FromGit(fromRepo, local.Payload)) != Hash(local) {
		t.Fatal("the repository's version must equal this node's")
	}
}
