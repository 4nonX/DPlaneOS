package systemd

import (
	"strings"
	"testing"
)

func TestRenderUnits(t *testing.T) {
	svc, timer, err := renderUnits(TimerConfig{
		Description: "ZFS Scrub for pool tank",
		Command:     "/run/current-system/sw/bin/zpool scrub tank",
		OnCalendar:  "Sun *-*-* 03:00:00",
		Persistent:  true,
		After:       []string{"zfs.target"},
	}, "dplaneos-scrub-tank", "/nix/store/x-zfs/bin:/nix/store/y-curl/bin")
	if err != nil {
		t.Fatal(err)
	}

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

	svc, _, _ = renderUnits(TimerConfig{Description: "x", Command: "/bin/true", OnCalendar: "daily"}, "dplaneos-x", "")
	if strings.Contains(svc, "Environment=") {
		t.Errorf("no PATH given, no Environment line expected:\n%s", svc)
	}
}

func TestResolveCommand(t *testing.T) {
	if got, err := resolveCommand("/usr/bin/env true"); err != nil || got != "/usr/bin/env true" {
		t.Errorf("absolute command changed: %q, %v", got, err)
	}
	// An unresolvable program must fail: systemd would not find it either.
	if got, err := resolveCommand("definitely-not-a-real-binary-xyz arg"); err == nil {
		t.Errorf("unresolvable command accepted as %q", got)
	}
	if _, _, err := renderUnits(TimerConfig{Command: "definitely-not-a-real-binary-xyz"}, "dplaneos-x", ""); err == nil {
		t.Error("renderUnits accepted an unresolvable command")
	}
}
