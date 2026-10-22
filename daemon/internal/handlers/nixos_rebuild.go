package handlers

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"dplaned/internal/cmdutil"
)

// runInOwnUnit runs a system command (nixos-rebuild, switch-to-configuration)
// in a transient systemd unit and waits for it, its output piped back. As a
// child of dplaned it lived in dplaned's cgroup: a switch that changes
// dplaned.service restarts dplaned, and systemd killed the switch with it,
// halfway through the activation; and it counted against dplaned's memory
// limit (evaluating nixpkgs takes more than a GiB). In its own unit it runs
// to the end even when dplaned restarts underneath it.
//
// Without systemd-run (not systemd) the command runs directly, as before.
func runInOwnUnit(timeout time.Duration, name string, args ...string) ([]byte, error) {
	bin, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%s not found: %w", name, err)
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return cmdutil.Run(timeout, bin, args...)
	}
	return cmdutil.Run(timeout, "systemd-run", ownUnitArgs(time.Now(), bin, args)...)
}

// ownUnitArgs is the systemd-run command line of runInOwnUnit. The unit gets
// dplaned's PATH (NixOS puts nix, git and nixos-rebuild there for the
// service) and NIX_PATH; --collect removes it once finished.
func ownUnitArgs(now time.Time, bin string, args []string) []string {
	a := []string{
		"--unit", fmt.Sprintf("dplaneos-system-change-%d", now.UnixNano()),
		"--description", "DPlaneOS: " + bin,
		"--collect", "--wait", "--pipe", "--quiet",
		"--setenv=PATH=" + os.Getenv("PATH"),
	}
	if np := os.Getenv("NIX_PATH"); np != "" {
		a = append(a, "--setenv=NIX_PATH="+np)
	}
	a = append(a, "--", bin)
	return append(a, args...)
}

// runNixosRebuild runs nixos-rebuild with args in its own unit.
func runNixosRebuild(args ...string) ([]byte, error) {
	return runInOwnUnit(cmdutil.TimeoutExtreme, "nixos-rebuild", args...)
}
