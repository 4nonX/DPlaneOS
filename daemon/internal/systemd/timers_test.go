package systemd

import (
	"strings"
	"testing"
)

func TestRenderUnits(t *testing.T) {
	svc, timer := renderUnits(TimerConfig{
		Description: "ZFS Scrub for pool tank",
		Command:     "/run/current-system/sw/bin/zpool scrub tank",
		OnCalendar:  "Sun *-*-* 03:00:00",
		Persistent:  true,
		After:       []string{"zfs.target"},
	}, "dplaneos-scrub-tank", "/nix/store/x-zfs/bin:/nix/store/y-curl/bin")

	for _, want := range []string{
		"ExecStart=/run/current-system/sw/bin/zpool scrub tank\n",
		`Environment="PATH=/nix/store/x-zfs/bin:/nix/store/y-curl/bin"`,
		"After=zfs.target network.target",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("service unit missing %q:\n%s", want, svc)
		}
	}
	for _, want := range []string{"OnCalendar=Sun *-*-* 03:00:00", "Persistent=true", "Unit=dplaneos-scrub-tank.service"} {
		if !strings.Contains(timer, want) {
			t.Errorf("timer unit missing %q:\n%s", want, timer)
		}
	}

	svc, _ = renderUnits(TimerConfig{Description: "x", Command: "/bin/true", OnCalendar: "daily"}, "dplaneos-x", "")
	if strings.Contains(svc, "Environment=") {
		t.Errorf("no PATH given, no Environment line expected:\n%s", svc)
	}
}

func TestResolveCommandKeepsAbsoluteAndUnknown(t *testing.T) {
	if got := resolveCommand("/usr/bin/env true"); got != "/usr/bin/env true" {
		t.Errorf("absolute command changed: %q", got)
	}
	if got := resolveCommand("definitely-not-a-real-binary-xyz arg"); got != "definitely-not-a-real-binary-xyz arg" {
		t.Errorf("unresolvable command changed: %q", got)
	}
}
