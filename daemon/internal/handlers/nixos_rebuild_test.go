package handlers

import (
	"strings"
	"testing"
	"time"
)

// nixos-rebuild runs in a transient unit (not dplaned's cgroup), waits, pipes
// its output back and gets dplaned's PATH.
func TestOwnUnitArgs(t *testing.T) {
	t.Setenv("PATH", "/run/current-system/sw/bin")
	t.Setenv("NIX_PATH", "")
	a := ownUnitArgs(time.Unix(0, 42), "/run/current-system/sw/bin/nixos-rebuild", []string{"switch", "--rollback"})
	got := strings.Join(a, " ")
	for _, want := range []string{"--unit dplaneos-system-change-42", "--collect", "--wait", "--pipe",
		"--setenv=PATH=/run/current-system/sw/bin", "-- /run/current-system/sw/bin/nixos-rebuild switch --rollback"} {
		if !strings.Contains(got, want) {
			t.Errorf("systemd-run args %q lack %q", got, want)
		}
	}
	if strings.Contains(got, "NIX_PATH") {
		t.Errorf("empty NIX_PATH passed: %q", got)
	}
}
