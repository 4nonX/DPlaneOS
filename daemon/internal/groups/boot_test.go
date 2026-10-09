package groups

import (
	"strings"
	"testing"
	"time"
)

func TestPlanOwnerImport(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	g := shared() // pool tank, owner a, candidates a, b
	base := View{Self: "a", Quorum: true, Quorate: true, ImportedHere: map[string]bool{},
		Online: []string{"a"}, FenceOK: true, FenceDelay: 75 * time.Second}
	exported := map[string]peerSeen{"b": {Reached: true, Imported: map[string]bool{}}}
	silent := map[string]peerSeen{"b": {}}

	cases := []struct {
		name   string
		mod    func(*View, *Group)
		peers  map[string]peerSeen
		absent time.Time
		want   string
		reason string
	}{
		{"already imported", func(v *View, _ *Group) { v.ImportedHere["tank"] = true }, exported, time.Time{}, "none", ""},
		{"not the owner", func(_ *View, g *Group) { g.Owner = "b" }, exported, time.Time{}, "none", ""},
		{"b confirms it does not use the pool", nil, exported, time.Time{}, "import", ""},
		{"b has it imported", nil, map[string]peerSeen{"b": {Reached: true, Imported: map[string]bool{"tank": true}}}, time.Time{}, "none", "b has pool tank imported"},
		{"not quorate", func(v *View, _ *Group) { v.Quorate = false }, exported, time.Time{}, "none", "not in the quorate partition"},
		{"b silent, no cluster", func(v *View, _ *Group) { v.Quorum, v.Quorate = false, false }, silent, time.Time{}, "none", "waiting for b"},
		{"b silent, no fencing", func(v *View, _ *Group) { v.FenceOK = false }, silent, time.Time{}, "none", "Import here"},
		{"b silent but online", func(v *View, _ *Group) { v.Online = []string{"a", "b"} }, silent, time.Time{}, "none", "does not answer"},
		{"b silent: wait for its watchdog", nil, silent, now.Add(-30 * time.Second), "wait", "watchdog"},
		{"b silent past the fencing delay", nil, silent, now.Add(-80 * time.Second), "import", "fencing delay"},
		{"older peer that does not report pools", nil, map[string]peerSeen{"b": {Reached: true}}, now, "wait", ""},
	}
	for _, c := range cases {
		v, gg := base, g
		v.ImportedHere = map[string]bool{}
		if c.mod != nil {
			c.mod(&v, &gg)
		}
		d := planOwnerImport(gg, v, c.peers, map[string]time.Time{"b": c.absent}, now)
		if d.Action != c.want || !strings.Contains(d.Reason, c.reason) {
			t.Errorf("%s: got %s (%q), want %s containing %q", c.name, d.Action, d.Reason, c.want, c.reason)
		}
	}
}
