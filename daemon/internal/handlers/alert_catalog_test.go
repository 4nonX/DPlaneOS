package handlers

import "testing"

func TestAlertSubscriptions(t *testing.T) {
	for _, c := range []struct {
		sub, event string
		want       bool
	}{
		{"pool.critical", "pool.critical", true},
		{"pool.critical", "pool.degraded", false},
		{"zfs.*", "zfs.critical.statechange", true},
		{"zfs.*", "zfsx.critical", false},
		{"*", "anything", true},
		{"ups.on_battery", "ups.online", false},
	} {
		if got := alertEventMatches(c.sub, c.event); got != c.want {
			t.Errorf("alertEventMatches(%q, %q) = %v", c.sub, c.event, got)
		}
	}
	if !webhookSubscribed(`["zfs.*","ups.low_battery"]`, "zfs.warning.checksum") ||
		webhookSubscribed(`["zfs.*"]`, "ups.low_battery") || !webhookSubscribed(`[]`, "x") {
		t.Error("webhookSubscribed")
	}
}

func TestUPSAlerts(t *testing.T) {
	for _, c := range []struct{ prev, cur, event string }{
		{"", "OL", ""},        // first look: nothing to report
		{"OL", "OL CHRG", ""}, // still on mains
		{"OL", "OB DISCHRG", EventUPSOnBattery},
		{"OB DISCHRG", "OB DISCHRG", ""}, // said once
		{"OB DISCHRG", "OB LB", EventUPSLowBattery},
		{"OB LB", "OL CHRG", EventUPSOnline},
		{"OL", "unreachable", EventUPSUnreachable},
		{"unreachable", "unreachable", ""},
		{"unreachable", "OL", EventUPSOnline},
		{"", "unreachable", ""},           // never seen: a UPS that is being set up
		{"", "OB LB", EventUPSLowBattery}, // started during an outage
	} {
		if _, event, _ := upsAlertFor(c.prev, c.cur); event != c.event {
			t.Errorf("%q -> %q: %q, want %q", c.prev, c.cur, event, c.event)
		}
	}
}

// Every alert has a level the channels know and a description for the page.
func TestAlertCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range AlertCatalog {
		if seen[e.Event] || e.Description == "" || (e.Level != "info" && e.Level != "warning" && e.Level != "critical") {
			t.Errorf("catalog entry %+v", e)
		}
		seen[e.Event] = true
	}
}
