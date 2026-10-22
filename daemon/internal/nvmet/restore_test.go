package nvmet

import "testing"

// Only exports whose zvol is on this node are restored: on an HA node the
// pool of a group owned elsewhere is not imported.
func TestAvailableExports(t *testing.T) {
	exports := []Export{{SubsystemNQN: "nqn.a", Zvol: "tank/vm1"}, {SubsystemNQN: "nqn.b", Zvol: "shared/vm2"}}
	got := availableExports(exports, func(dev string) bool { return dev == "/dev/zvol/tank/vm1" })
	if len(got) != 1 || got[0].SubsystemNQN != "nqn.a" {
		t.Errorf("got %+v", got)
	}
}

// No saved exports: nothing happens (and nothing blocks).
func TestRestoreAtStartupWithoutFile(t *testing.T) {
	RestoreAtStartup(t.TempDir()+"/none.json", 0)
}
