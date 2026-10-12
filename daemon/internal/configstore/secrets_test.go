package configstore

import (
	"errors"
	"testing"
)

func TestMaterialTransport(t *testing.T) {
	m := Material{"bind_password": "s3cret", "x": "y"}
	sealed, err := sealForPeer("pair-secret", KindLDAP, "ldap", m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := openFromPeer("pair-secret", KindLDAP, "ldap", sealed)
	if err != nil || got["bind_password"] != "s3cret" || got["x"] != "y" {
		t.Fatalf("round trip: %v %v", got, err)
	}
	if _, err := openFromPeer("other-secret", KindLDAP, "ldap", sealed); err == nil {
		t.Error("another pair's secret must not open it")
	}
	// Bound to the resource: material for one user is not another's.
	sealed, _ = sealForPeer("pair-secret", KindUser, "alice", Material{"password_hash": "$2a$10$abc"})
	if _, err := openFromPeer("pair-secret", KindUser, "bob", sealed); err == nil {
		t.Error("material must not open as another resource")
	}
}

func TestMaterialMatches(t *testing.T) {
	creds := Material{"password_hash": "$2a$10$abc", "scram_salt": "s", "totp_secret": "JBSWY3DP"}
	user := Resource{Kind: KindUser, Key: "alice", Payload: map[string]any{"credentials": credentialsFingerprint(creds)}}
	if err := materialMatches(user, creds); err != nil {
		t.Errorf("same credentials: %v", err)
	}
	changed := Material{"password_hash": "$2a$10$xyz", "scram_salt": "s", "totp_secret": "JBSWY3DP"}
	if err := materialMatches(user, changed); !errors.Is(err, ErrSecretChanged) {
		t.Errorf("changed password must not match: %v", err)
	}
	ldap := Resource{Kind: KindLDAP, Key: "ldap", Payload: map[string]any{"bind_password": fingerprint("pw")}}
	if materialMatches(ldap, Material{"bind_password": "pw"}) != nil || materialMatches(ldap, Material{"bind_password": "other"}) == nil {
		t.Error("LDAP bind password fingerprint check")
	}
}

func TestWithMaterialAndAssemble(t *testing.T) {
	ldap := Resource{Kind: KindLDAP, Key: "ldap", Payload: map[string]any{"server": "ldap", "bind_password": fingerprint("pw")}}
	l := withMaterial(ldap, Material{"bind_password": "pw"})
	if l.Payload["bind_password"] != "" {
		t.Errorf("the bind password is written outside the engine: %+v", l.Payload)
	}
	if ldap.Payload["bind_password"] != fingerprint("pw") {
		t.Error("withMaterial must not change the revision's payload")
	}
	if ds, err := Assemble([]Resource{l}); err != nil || ds.LDAP == nil || ds.LDAP.BindPassword != "" {
		t.Fatalf("assembled LDAP settings: %+v %v", ds, err)
	}

	user := Resource{Kind: KindUser, Key: "alice", Payload: map[string]any{"username": "alice", "credentials": "sha256:x"}}
	u := withMaterial(user, Material{"password_hash": "$2a$10$abc"})
	if _, ok := u.Payload["credentials"]; ok || u.Payload["password_hash"] != "" {
		t.Errorf("user credentials are written outside the engine: %+v", u.Payload)
	}
}

func TestNeedsMaterial(t *testing.T) {
	for _, c := range []struct {
		r    Resource
		want bool
	}{
		{Resource{Kind: KindUser, Payload: map[string]any{"credentials": "sha256:a"}}, true},
		{Resource{Kind: KindUser, Payload: map[string]any{"credentials": ""}}, false}, // no local password (LDAP user)
		{Resource{Kind: KindLDAP, Payload: map[string]any{"bind_password": ""}}, false},
		{Resource{Kind: KindLDAP, Payload: map[string]any{"bind_password": "sha256:k"}}, true},
		{Resource{Kind: KindDataset, Payload: map[string]any{"name": "tank/x"}}, false},
		{Resource{Kind: KindUser}, false}, // deletion
	} {
		if got := needsMaterial(c.r); got != c.want {
			t.Errorf("%+v: got %v", c.r, got)
		}
	}
}
