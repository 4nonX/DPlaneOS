package gitops

import (
	"strings"
	"testing"

	"dplaned/internal/nixwriter"
)

func TestGenerateStateYAML_System(t *testing.T) {
	state := &LiveState{
		System: &nixwriter.DPlaneState{
			Hostname:   "dplane-test",
			Timezone:   "Europe/London",
			DNSServers: []string{"8.8.8.8", "1.1.1.1"},
			NTPServers: []string{"pool.ntp.org"},
			FirewallTCP: []int{22, 80, 443},
			FirewallUDP: []int{53},
			NetworkStatics: map[string]nixwriter.NetworkStaticEntry{
				"eth0": {CIDR: "192.168.1.10/24", Gateway: "192.168.1.1"},
			},
			SambaWorkgroup:    "WORKGROUP",
			SambaServerString: "D-PlaneOS Test",
			SambaTimeMachine:  true,
		},
	}

	yaml := GenerateStateYAML(state)

	// Verify top-level
	if !strings.Contains(yaml, "version: 1") {
		t.Error("missing version: 1")
	}

	// Verify system block
	if !strings.Contains(yaml, "system:") {
		t.Error("missing system: block")
	}
	if !strings.Contains(yaml, "hostname: dplane-test") {
		t.Error("missing hostname")
	}
	if !strings.Contains(yaml, "timezone: Europe/London") {
		t.Error("missing timezone")
	}
	if !strings.Contains(yaml, "dns_servers: [\"8.8.8.8\", \"1.1.1.1\"]") {
		t.Error("missing or malformed dns_servers")
	}
	if !strings.Contains(yaml, "tcp: [22, 80, 443]") {
		t.Error("missing or malformed firewall tcp")
	}
	if !strings.Contains(yaml, "statics:") || !strings.Contains(yaml, "eth0:") {
		t.Error("missing networking statics")
	}
	if !strings.Contains(yaml, "samba:") || !strings.Contains(yaml, "workgroup: \"WORKGROUP\"") {
		t.Error("missing or malformed samba block")
	}
	if !strings.Contains(yaml, "time_machine: true") {
		t.Error("missing time_machine toggle")
	}
}

func TestGenerateStateYAML_NoSystem(t *testing.T) {
	state := &LiveState{
		System: nil,
	}

	yaml := GenerateStateYAML(state)

	if strings.Contains(yaml, "system:") {
		t.Error("system: block should be missing when state.System is nil")
	}
}
