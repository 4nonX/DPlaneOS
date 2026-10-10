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
	// Bound to the resource: material for one certificate is not another's.
	sealed, _ = sealForPeer("pair-secret", KindCertificate, "web", Material{"key": "PEM"})
	if _, err := openFromPeer("pair-secret", KindCertificate, "mail", sealed); err == nil {
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
	acme := Resource{Kind: KindACME, Key: "acme", Payload: map[string]any{"dns_config": map[string]any{"CF_TOKEN": fingerprint("tok")}}}
	if materialMatches(acme, Material{"dns_config.CF_TOKEN": "tok"}) != nil {
		t.Error("ACME DNS credentials should match")
	}
	if materialMatches(acme, Material{"dns_config.CF_TOKEN": "new"}) == nil {
		t.Error("changed ACME credentials must not match")
	}
}

func TestWithMaterialAndAssemble(t *testing.T) {
	cert := Resource{Kind: KindCertificate, Key: "web", Payload: map[string]any{"name": "web", "cert": "CERT", "key": fingerprint("KEY")}}
	if ds, _ := Assemble([]Resource{cert}); len(ds.Certificates) != 0 {
		t.Error("a certificate with only a key fingerprint must not be assembled")
	}
	filled := withMaterial(cert, Material{"key": "KEY"})
	ds, err := Assemble([]Resource{filled})
	if err != nil || len(ds.Certificates) != 1 || ds.Certificates[0].Key != "KEY" {
		t.Fatalf("certificate with its key: %+v %v", ds, err)
	}
	if cert.Payload["key"] != fingerprint("KEY") {
		t.Error("withMaterial must not change the revision's payload")
	}

	acme := Resource{Kind: KindACME, Key: "acme", Payload: map[string]any{"email": "a@b", "dns_config": map[string]any{"CF_TOKEN": fingerprint("tok")}}}
	if ds, _ := Assemble([]Resource{acme}); ds.ACME == nil || ds.ACME.DNSConfig != nil {
		t.Errorf("fingerprints are not applied as credentials: %+v", ds.ACME)
	}
	ds, _ = Assemble([]Resource{withMaterial(acme, Material{"dns_config.CF_TOKEN": "tok"})})
	if ds.ACME.DNSConfig["CF_TOKEN"] != "tok" {
		t.Errorf("ACME credentials filled in: %+v", ds.ACME)
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
		{Resource{Kind: KindCertificate, Payload: map[string]any{"key": "sha256:k"}}, true},
		{Resource{Kind: KindDataset, Payload: map[string]any{"name": "tank/x"}}, false},
		{Resource{Kind: KindUser}, false}, // deletion
	} {
		if got := needsMaterial(c.r); got != c.want {
			t.Errorf("%+v: got %v", c.r, got)
		}
	}
}
