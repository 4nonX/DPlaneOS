package gitops

import (
	"strings"
	"testing"
)

func TestShareOptionsRoundTrip(t *testing.T) {
	live := LiveShare{
		Name: "macs", Path: "/tank/macs", Comment: "Time Machine", Enabled: true,
		TimeMachine: true, TimeMachineQuota: "500G", ShadowCopy: true,
		HostsAllow: "192.168.1.0/24",
	}
	yaml := GenerateStateYAML(&LiveState{Shares: []LiveShare{live}})
	for _, want := range []string{`comment: "Time Machine"`, "time_machine: true", `time_machine_quota: "500G"`, "shadow_copy: true", `hosts_allow: "192.168.1.0/24"`} {
		if !strings.Contains(yaml, want) {
			t.Errorf("generated state.yaml missing %q:\n%s", want, yaml)
		}
	}
	if strings.Contains(yaml, "recycle_bin") || strings.Contains(yaml, "hosts_deny") {
		t.Errorf("default-valued options should be omitted:\n%s", yaml)
	}

	desired, err := ParseStateYAML(yaml)
	if err != nil {
		t.Fatalf("ParseStateYAML: %v", err)
	}
	if len(desired.Shares) != 1 {
		t.Fatalf("want 1 share, got %d", len(desired.Shares))
	}
	if changes := diffShare(desired.Shares[0], live); len(changes) != 0 {
		t.Errorf("round trip should plan no changes, got %v", changes)
	}
}

func TestShareOptionsOmittedAreUnmanaged(t *testing.T) {
	// A state.yaml written before per-share options existed.
	desired, err := ParseStateYAML("version: 1\nshares:\n  - name: data\n    path: /tank/data\n")
	if err != nil {
		t.Fatalf("ParseStateYAML: %v", err)
	}
	live := LiveShare{Name: "data", Path: "/tank/data", TimeMachine: true, RecycleBin: true, HostsDeny: "10.0.0.9"}
	if changes := diffShare(desired.Shares[0], live); len(changes) != 0 {
		t.Errorf("options absent from state.yaml must not be planned, got %v", changes)
	}

	off := false
	d := desired.Shares[0]
	d.TimeMachine = &off
	changes := diffShare(d, live)
	if len(changes) != 1 || !strings.HasPrefix(changes[0], "time_machine: true → false") {
		t.Errorf("declared option should be diffed, got %v", changes)
	}
}
