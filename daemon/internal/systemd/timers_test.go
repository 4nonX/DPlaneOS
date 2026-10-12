package systemd

import (
	"os"
	"path/filepath"
	"runtime"
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

// A timer that calls the daemon's API has nothing of the request in its
// unit: no shell, no token, no name that came from a request.
func TestHookTimer(t *testing.T) {
	prevDir := HookDir
	HookDir = t.TempDir()
	t.Cleanup(func() { HookDir = prevDir })

	payload := []byte(`{"dataset":"tank/it's \"quoted\" $(reboot) %n","prefix":"auto"}`)
	command, err := writeHook("dplaneos-snap-x-daily", &Hook{Path: "/api/zfs/snapshots/cron-hook", Token: "abc123DEF", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(HookDir, "dplaneos-snap-x-daily.curl")
	if command != "curl -K "+config {
		t.Fatalf("command = %q", command)
	}
	for _, bad := range []string{"bash", "sh ", "abc123DEF", "quoted", "reboot", "'", `"`} {
		if strings.Contains(command, bad) {
			t.Errorf("the unit's command contains %q: %s", bad, command)
		}
	}
	got, err := os.ReadFile(filepath.Join(HookDir, "dplaneos-snap-x-daily.json"))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("payload file: %q %v", got, err)
	}
	cfg, _ := os.ReadFile(config)
	for _, want := range []string{
		`url = "http://localhost/api/zfs/snapshots/cron-hook"`,
		`header = "X-Internal-Token: abc123DEF"`,
		`request = "POST"`,
		`data-binary = "@` + filepath.Join(HookDir, "dplaneos-snap-x-daily.json") + `"`,
	} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("request file misses %q:\n%s", want, cfg)
		}
	}
	// The payload is not in the request file either: it is read from its own.
	if strings.Contains(string(cfg), "reboot") {
		t.Errorf("the payload is part of the request file:\n%s", cfg)
	}
	if runtime.GOOS != "windows" {
		for _, f := range []string{config, filepath.Join(HookDir, "dplaneos-snap-x-daily.json")} {
			if st, err := os.Stat(f); err != nil || st.Mode().Perm() != 0o600 {
				t.Errorf("%s is readable by others: %v %v", f, st.Mode(), err)
			}
		}
	}
	removeHook("dplaneos-snap-x-daily")
	if _, err := os.Stat(config); err == nil {
		t.Error("the request file stayed after the timer was removed")
	}

	for name, h := range map[string]*Hook{
		"a path outside the API":    {Path: "/etc/passwd", Token: "abc"},
		"a path with a query":       {Path: "/api/x?y=1", Token: "abc"},
		"no token":                  {Path: "/api/x"},
		"a token with a line break": {Path: "/api/x", Token: "abc\nheader = \"X: y\""},
		"a token with a quote":      {Path: "/api/x", Token: `abc"`},
	} {
		if _, err := writeHook("dplaneos-x", h); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// What comes from a request and lands in a unit file cannot add a line to it.
func TestUnitTextCannotAddSettings(t *testing.T) {
	svc, timer, err := renderUnits(TimerConfig{
		Description: "Rsync backup: nightly\nExecStartPre=/bin/reboot\r\n100% done",
		Command:     "/bin/true",
		OnCalendar:  "daily",
	}, "dplaneos-x", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, unit := range []string{svc, timer} {
		for _, line := range strings.Split(unit, "\n") {
			if strings.HasPrefix(line, "ExecStartPre=") {
				t.Fatalf("a description added a setting:\n%s", unit)
			}
		}
		if !strings.Contains(unit, "100%% done") {
			t.Errorf("a %% in a description is not escaped:\n%s", unit)
		}
	}

	prevDir := HookDir
	HookDir = t.TempDir()
	t.Cleanup(func() { HookDir = prevDir })
	for name, cfg := range map[string]TimerConfig{
		"name with a slash":        {Name: "snap-tank/data", Command: "/bin/true", OnCalendar: "daily"},
		"name with a space":        {Name: "snap tank", Command: "/bin/true", OnCalendar: "daily"},
		"schedule with a line":     {Name: "x", Command: "/bin/true", OnCalendar: "daily\nUnit=other.service"},
		"schedule with specifier":  {Name: "x", Command: "/bin/true", OnCalendar: "%H"},
		"command with a line":      {Name: "x", Command: "/bin/true\nExecStart=/bin/reboot", OnCalendar: "daily"},
		"neither command nor hook": {Name: "x", OnCalendar: "daily"},
	} {
		if err := InstallTimer(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
