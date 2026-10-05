package configstore

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"dplaned/internal/gitops"
)

// roundTrip stores resources the way the database does (JSON) and reads them back.
func roundTrip(t *testing.T, rs []Resource) []Resource {
	t.Helper()
	out := make([]Resource, len(rs))
	for i, r := range rs {
		raw, err := json.Marshal(r.Payload)
		if err != nil {
			t.Fatal(err)
		}
		var back map[string]any
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		r.Payload = back
		out[i] = r
	}
	return out
}

// Phase 1 exit criterion: state.yaml → revisions → state.yaml is identical
// (apart from ignore_extraneous, which an export always sets to true).
func TestStateYAMLRoundTrip(t *testing.T) {
	content, err := os.ReadFile("testdata/full-state.yaml")
	if err != nil {
		t.Fatal(err)
	}
	original, err := gitops.ParseStateYAML(string(content))
	if err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}

	resources := roundTrip(t, Extract(original, "node-a"))
	assembled, err := Assemble(resources)
	if err != nil {
		t.Fatal(err)
	}
	exported := gitops.PrintStateYAML(assembled)
	if _, err := gitops.ParseStateYAML(exported); err != nil {
		t.Fatalf("exported state.yaml does not validate: %v\n%s", err, exported)
	}

	want := canonical(*original)
	want.Version = "1"
	want.IgnoreExtraneous = true
	if got, exp := exported, gitops.PrintStateYAML(&want); got != exp {
		t.Errorf("round trip changed the state: %s", firstDiff(exp, got))
	}

	// Every kind in the fixture produced resources.
	kinds := map[string]bool{}
	for _, r := range resources {
		kinds[r.Kind] = true
	}
	for _, k := range []string{KindPool, KindDataset, KindShare, KindNFS, KindStack, KindSystem, KindUser, KindGroup, KindReplication, KindLDAP, KindSMART} {
		if !kinds[k] {
			t.Errorf("no %s resource extracted from the fixture", k)
		}
	}
}

func TestSecretsAreFingerprintedNotStored(t *testing.T) {
	ds := &gitops.DesiredState{
		Version: "1",
		LDAP:    &gitops.DesiredLDAP{Server: "ldap", BindPassword: "s3cret"},
		ACME:    &gitops.DesiredACME{Email: "a@b", DNSConfig: map[string]string{"CF_API_TOKEN": "tok"}},
		Certificates: []gitops.DesiredCertificate{
			{Name: "web", Cert: "-----BEGIN CERTIFICATE-----", Key: "-----BEGIN PRIVATE KEY-----"},
		},
	}
	rs := Extract(ds, "n")
	raw, _ := json.Marshal(rs)
	for _, secret := range []string{"s3cret", "\"tok\"", "PRIVATE KEY"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("secret %q stored in the history: %s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), "sha256:") {
		t.Error("secret changes should stay visible as fingerprints")
	}

	assembled, err := Assemble(roundTrip(t, rs))
	if err != nil {
		t.Fatal(err)
	}
	if assembled.LDAP.BindPassword != "" || assembled.ACME.DNSConfig != nil || len(assembled.Certificates) != 0 {
		t.Errorf("export must not carry secrets or fingerprints: %+v %+v %+v", assembled.LDAP, assembled.ACME, assembled.Certificates)
	}
}

func TestScopes(t *testing.T) {
	ds := &gitops.DesiredState{
		Datasets: []gitops.DesiredDataset{{Name: "tank/media"}},
		Shares:   []gitops.DesiredShare{{Name: "media", Path: "/mnt/tank/media"}},
		Users:    []gitops.DesiredUser{{Username: "alice"}},
		System:   &gitops.DesiredSystem{},
	}
	got := map[string]string{}
	for _, r := range Extract(ds, "node-a") {
		got[r.ID()] = r.Scope + ":" + r.ScopeID
	}
	want := map[string]string{
		"dataset/tank/media": "group:tank",
		"share/media":        "group:tank",
		"user/alice":         "cluster:local",
		"system/system":      "node:node-a",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: scope %q, want %q", id, got[id], w)
		}
	}
}

// firstDiff describes the first differing line of two texts.
func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d: want %q, got %q", i+1, wl, gl)
		}
	}
	return "identical"
}

// canonical sorts every list by its identity, the order an export uses.
// Written independently of Assemble so the test does not check the code
// against itself.
func canonical(ds gitops.DesiredState) gitops.DesiredState {
	sort.Slice(ds.Pools, func(i, j int) bool { return ds.Pools[i].Name < ds.Pools[j].Name })
	sort.Slice(ds.Datasets, func(i, j int) bool { return ds.Datasets[i].Name < ds.Datasets[j].Name })
	sort.Slice(ds.Shares, func(i, j int) bool { return ds.Shares[i].Name < ds.Shares[j].Name })
	sort.Slice(ds.NFS, func(i, j int) bool { return ds.NFS[i].Path < ds.NFS[j].Path })
	sort.Slice(ds.Stacks, func(i, j int) bool { return ds.Stacks[i].Name < ds.Stacks[j].Name })
	sort.Slice(ds.Users, func(i, j int) bool { return ds.Users[i].Username < ds.Users[j].Username })
	sort.Slice(ds.Groups, func(i, j int) bool { return ds.Groups[i].Name < ds.Groups[j].Name })
	sort.Slice(ds.Replication, func(i, j int) bool { return ds.Replication[i].Name < ds.Replication[j].Name })
	sort.Slice(ds.SMART, func(i, j int) bool {
		return ds.SMART[i].Device+"-"+ds.SMART[i].Type < ds.SMART[j].Device+"-"+ds.SMART[j].Type
	})
	return ds
}
