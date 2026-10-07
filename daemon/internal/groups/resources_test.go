package groups

import (
	"reflect"
	"testing"

	"dplaned/internal/nvmet"
)

func TestStackPools(t *testing.T) {
	yaml := `services:
  plex:
    image: plex
    volumes:
      - /mnt/tank/media:/data
      - "/mnt/fast/cache:/cache"
      - /var/lib/plex:/config
`
	if got := stackPools(yaml); !reflect.DeepEqual(got, []string{"fast", "tank"}) {
		t.Errorf("got %v", got)
	}
	if got := stackPools("services: {a: {image: x}}"); len(got) != 0 {
		t.Errorf("no pools: %v", got)
	}
}

func TestFilterResources(t *testing.T) {
	g := shared() // pool tank
	stacks := []StackDef{
		{Name: "plex", YAML: "volumes: [/mnt/tank/media:/data]"},
		{Name: "monitor", YAML: "volumes: [/proc:/host/proc]"},
		{Name: "other", YAML: "volumes: [/mnt/fast/x:/x]"},
	}
	exports := []nvmet.Export{
		{SubsystemNQN: "nqn.2026-10.io.dplaneos:vm1", Zvol: "tank/vm1"},
		{SubsystemNQN: "nqn.2026-10.io.dplaneos:vm2", Zvol: "/dev/zvol/fast/vm2"},
	}
	r := filterResources(g, stacks, exports)
	if len(r.Stacks) != 1 || r.Stacks[0].Name != "plex" {
		t.Errorf("stacks: %+v", r.Stacks)
	}
	if len(r.NVMe) != 1 || r.NVMe[0].Zvol != "tank/vm1" {
		t.Errorf("exports: %+v", r.NVMe)
	}
}

func TestAppliedExports(t *testing.T) {
	g := shared() // tank, owner a
	local := []nvmet.Export{
		{SubsystemNQN: "nqn.x:tank-vm", Zvol: "tank/vm"},
		{SubsystemNQN: "nqn.x:fast-vm", Zvol: "fast/vm"}, // pool in no group: always applied
	}
	// Owner a applies both.
	if got := appliedExports(local, []Group{g}, "a", nil); len(got) != 2 {
		t.Errorf("owner: %+v", got)
	}
	// Standby b leaves the group's export out.
	got := appliedExports(local, []Group{g}, "b", nil)
	if len(got) != 1 || got[0].SubsystemNQN != "nqn.x:fast-vm" {
		t.Errorf("standby: %+v", got)
	}
	// A new owner applies an export it only knows from the previous owner.
	known := map[string][]nvmet.Export{"data": {{SubsystemNQN: "nqn.x:tank-only-there", Zvol: "tank/vm2"}}}
	got = appliedExports(nil, []Group{g}, "a", known)
	if len(got) != 1 || got[0].SubsystemNQN != "nqn.x:tank-only-there" {
		t.Errorf("new owner: %+v", got)
	}
}
