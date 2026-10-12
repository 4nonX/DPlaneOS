package groups

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"dplaned/internal/iscsit"
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
	targets := []iscsit.Target{
		{IQN: "iqn.2026-10.io.dplaneos:vm1", Zvol: "tank/vm1"},
		{IQN: "iqn.2026-10.io.dplaneos:vm2", Zvol: "fast/vm2"},
	}
	r := filterResources(g, stacks, exports, targets)
	if len(r.Stacks) != 1 || r.Stacks[0].Name != "plex" {
		t.Errorf("stacks: %+v", r.Stacks)
	}
	if len(r.ISCSI) != 1 || r.ISCSI[0].Zvol != "tank/vm1" {
		t.Errorf("targets: %+v", r.ISCSI)
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

func TestAppliedTargets(t *testing.T) {
	g := shared() // tank, owner a
	local := []iscsit.Target{
		{IQN: "iqn.2026-10.lan.nas:tank-vm", Zvol: "tank/vm"},
		{IQN: "iqn.2026-10.lan.nas:fast-vm", Zvol: "fast/vm"}, // pool in no group: always served
	}
	if got := appliedTargets(local, []Group{g}, "a", nil); len(got) != 2 {
		t.Errorf("owner: %+v", got)
	}
	got := appliedTargets(local, []Group{g}, "b", nil)
	if len(got) != 1 || got[0].IQN != "iqn.2026-10.lan.nas:fast-vm" {
		t.Errorf("standby: %+v", got)
	}
	// A new owner serves what the previous one published, once.
	known := map[string][]iscsit.Target{"data": {local[0], {IQN: "iqn.2026-10.lan.nas:tank-db", Zvol: "tank/db"}}}
	if got := appliedTargets(local, []Group{g}, "a", known); len(got) != 3 {
		t.Errorf("owner with published targets: %+v", got)
	}
}

// blockRes makes a manager's iSCSI targets and NVMe-oF exports lists in
// memory, and records what is applied (in the recorder, next to the pool
// operations, so that their order can be checked).
type blockRes struct {
	targets []iscsit.Target
	exports []nvmet.Export
}

func (b *blockRes) wire(m *Manager, rec *recorder) {
	names := func(n []string) string { return "[" + strings.Join(n, " ") + "]" }
	m.res.LocalTargets = func() ([]iscsit.Target, error) { return append([]iscsit.Target(nil), b.targets...), nil }
	m.res.SaveTargets = func(l []iscsit.Target) error { b.targets = append([]iscsit.Target(nil), l...); return nil }
	m.res.ApplyTargets = func(l []iscsit.Target) error {
		var n []string
		for _, t := range l {
			n = append(n, t.Zvol)
		}
		rec.add("targets " + names(n))
		return nil
	}
	m.res.LocalExports = func() ([]nvmet.Export, error) { return append([]nvmet.Export(nil), b.exports...), nil }
	m.res.SaveExports = func(l []nvmet.Export) error { b.exports = append([]nvmet.Export(nil), l...); return nil }
	m.res.ApplyExports = func(l []nvmet.Export) error {
		var n []string
		for _, e := range l {
			n = append(n, e.Zvol)
		}
		rec.add("exports " + names(n))
		return nil
	}
}

func indexOf(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}
	return -1
}

func lastWith(events []string, prefix string) string {
	for i := len(events) - 1; i >= 0; i-- {
		if strings.HasPrefix(events[i], prefix) {
			return events[i]
		}
	}
	return ""
}

// An iSCSI target or NVMe-oF export holds its volume open: before a group's
// pool is exported, this node must have stopped serving them, and after the
// move it must not start again. The new owner serves them from what the old
// one published.
func TestBlockTargetsFollowTheGroup(t *testing.T) {
	db := groupsTestDB(t)
	a, rec, _, _ := testManager(t, db, "a", map[string]string{"pool-77": "77"})
	res := &blockRes{
		targets: []iscsit.Target{{IQN: "iqn.2026-10.lan.nas:g", Zvol: "pool-77/lun"}, {IQN: "iqn.2026-10.lan.nas:own", Zvol: "own/lun"}},
		exports: []nvmet.Export{{SubsystemNQN: "nqn.2026-10.lan.nas:g", Zvol: "pool-77/nv"}, {SubsystemNQN: "nqn.2026-10.lan.nas:own", Zvol: "own/nv"}},
	}
	res.wire(a, rec)
	if _, err := a.CreateWith("g", Shared, []string{"pool-77"}, []string{"b"}, 0, false); err != nil {
		t.Fatal(err)
	}
	a.ActivateTick()
	ev := rec.take()
	if lastWith(ev, "targets ") != "targets [pool-77/lun own/lun]" || lastWith(ev, "exports ") != "exports [pool-77/nv own/nv]" {
		t.Fatalf("the owner does not serve everything: %v", ev)
	}
	// What the owner publishes for the other candidates.
	pub, err := a.LocalResources("g")
	if err != nil || len(pub.ISCSI) != 1 || len(pub.NVMe) != 1 || pub.ISCSI[0].Zvol != "pool-77/lun" {
		t.Fatalf("published: %+v %v", pub, err)
	}

	if _, err := a.Move("g", "b"); err != nil {
		t.Fatal(err)
	}
	ev = rec.take()
	exp := indexOf(ev, "export pool-77")
	tg, ex := indexOf(ev, "targets [own/lun]"), indexOf(ev, "exports [own/nv]")
	if exp < 0 || tg < 0 || ex < 0 || tg > exp || ex > exp {
		t.Fatalf("the group's targets and exports were not stopped before the pool was exported: %v", ev)
	}
	// Owned by b now: nothing of the group comes back here.
	a.ActivateTick()
	ev = rec.take()
	for _, e := range ev {
		if strings.Contains(e, "pool-77/") {
			t.Fatalf("the old owner serves the group again: %v", ev)
		}
	}
	// Its definitions stay in this node's lists (for when the group returns).
	if len(res.targets) != 2 || len(res.exports) != 2 {
		t.Fatalf("definitions lost on the old owner: %+v %+v", res.targets, res.exports)
	}

	// The other node: it has what a published, and becomes the owner.
	dbB := groupsTestDB(t)
	b, recB, _, _ := testManager(t, dbB, "b", map[string]string{})
	resB := &blockRes{}
	resB.wire(b, recB)
	g, _ := Get(db, "g")
	before := *g
	before.Owner, before.Epoch = "a", g.Epoch-1
	if err := b.Receive(Update{Group: before}); err != nil {
		t.Fatal(err)
	}
	if err := storeResources(dbB, "g", pub); err != nil {
		t.Fatal(err)
	}
	b.ActivateTick()
	for _, e := range recB.take() {
		if strings.Contains(e, "pool-77/") {
			t.Fatalf("a candidate serves the group's targets: %s", e)
		}
	}
	if err := b.Receive(Update{Group: *g}); err != nil {
		t.Fatal(err)
	}
	b.ActivateTick()
	ev = recB.take()
	if lastWith(ev, "targets ") != "targets [pool-77/lun]" || lastWith(ev, "exports ") != "exports [pool-77/nv]" {
		t.Fatalf("the new owner does not serve the group's targets and exports: %v", ev)
	}
	if imp := indexOf(ev, "import 77"); imp < 0 || imp > indexOf(ev, "targets [pool-77/lun]") {
		t.Fatalf("served before the pool was imported: %v", ev)
	}
	if len(resB.targets) != 1 || len(resB.exports) != 1 {
		t.Fatalf("the new owner did not take the definitions over: %+v %+v", resB.targets, resB.exports)
	}
}

// A group's status goes to everyone who may look at the HA page: no CHAP
// secret and no compose file (they carry passwords) is in it.
func TestStatusDoesNotCarrySecrets(t *testing.T) {
	r := Resources{
		Stacks: []StackDef{{Name: "db", YAML: "environment: [POSTGRES_PASSWORD=hunter2hunter2]"}},
		ISCSI:  []iscsit.Target{{IQN: "iqn.2026-10.lan.nas:g", Zvol: "tank/lun", ACLs: []iscsit.ACL{{InitiatorIQN: "iqn.2026-10.lan.c:x", CHAPUser: "x", CHAPPass: "Sup3r-Secret-123"}}}},
	}
	out := fmt.Sprintf("%+v", r.summary())
	if strings.Contains(out, "Sup3r-Secret-123") || strings.Contains(out, "hunter2") {
		t.Fatalf("secrets in the summary: %s", out)
	}
	if !strings.Contains(out, "iqn.2026-10.lan.c:x") || !strings.Contains(out, "db") {
		t.Fatalf("the summary lost what it is for: %s", out)
	}
	// The definitions themselves are untouched.
	if r.ISCSI[0].ACLs[0].CHAPPass == "" || r.Stacks[0].YAML == "" {
		t.Fatal("the summary changed the definitions")
	}
}
