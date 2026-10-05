package systemd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// SystemdPath is the unit directory on mutable-/etc distributions.
	SystemdPath = "/etc/systemd/system"
	// RuntimePath holds runtime units. On NixOS /etc/systemd/system is a
	// read-only link into the Nix store, so timers live here instead and are
	// re-installed by the daemon at every start (/run is cleared on reboot).
	RuntimePath = "/run/systemd/system"
	Prefix      = "dplaneos-"
)

// TimerConfig holds the configuration for a systemd timer
type TimerConfig struct {
	Name        string
	Description string
	Command     string
	OnCalendar  string // e.g. "Mon *-*-* 04:00:00"
	Persistent  bool
	After       []string
}

func isNixOS() bool {
	_, err := os.Stat("/etc/NIXOS")
	return err == nil
}

// UnitDir is where timer units are written on this host.
func UnitDir() string {
	if isNixOS() {
		return RuntimePath
	}
	return SystemdPath
}

// resolveCommand makes the program in ExecStart absolute using the daemon's
// own PATH. systemd only searches a fixed list of directories, which on NixOS
// contains none of the tools (bash, curl, zpool) these units run.
func resolveCommand(command string) string {
	prog, rest, _ := strings.Cut(strings.TrimSpace(command), " ")
	if strings.HasPrefix(prog, "/") {
		return command
	}
	abs, err := exec.LookPath(prog)
	if err != nil {
		return command
	}
	if rest == "" {
		return abs
	}
	return abs + " " + rest
}

// renderUnits returns the .service and .timer contents. pathEnv is passed to
// the service so commands run inside it (e.g. curl in bash -c) resolve too.
func renderUnits(cfg TimerConfig, unitName, pathEnv string) (service, timer string) {
	env := ""
	if pathEnv != "" {
		env = fmt.Sprintf("Environment=\"PATH=%s\"\n", pathEnv)
	}
	service = fmt.Sprintf(`[Unit]
Description=%s
After=%s

[Service]
Type=oneshot
%sExecStart=%s
User=root

[Install]
WantedBy=multi-user.target
`, cfg.Description, strings.Join(append(cfg.After, "network.target"), " "), env, resolveCommand(cfg.Command))

	persistentStr := "false"
	if cfg.Persistent {
		persistentStr = "true"
	}
	timer = fmt.Sprintf(`[Unit]
Description=Timer for %s

[Timer]
OnCalendar=%s
Persistent=%s
Unit=%s.service

[Install]
WantedBy=timers.target
`, cfg.Description, cfg.OnCalendar, persistentStr, unitName)
	return service, timer
}

// InstallTimer creates a .service and .timer unit file and reloads systemd
func InstallTimer(cfg TimerConfig) error {
	if cfg.Name == "" || cfg.Command == "" || cfg.OnCalendar == "" {
		return fmt.Errorf("invalid timer config: name, command, and onCalendar are required")
	}

	unitName := cfg.Name
	if !strings.HasPrefix(unitName, Prefix) {
		unitName = Prefix + unitName
	}

	serviceContent, timerContent := renderUnits(cfg, unitName, os.Getenv("PATH"))
	dir := UnitDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("unit directory %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, unitName+".service"), []byte(serviceContent), 0o644); err != nil {
		return fmt.Errorf("failed to write service unit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, unitName+".timer"), []byte(timerContent), 0o644); err != nil {
		return fmt.Errorf("failed to write timer unit: %v", err)
	}

	if err := DaemonReload(); err != nil {
		return err
	}

	// Runtime units can only be enabled at runtime (the link lives in /run too).
	args := []string{"enable", "--now"}
	if dir == RuntimePath {
		args = append(args, "--runtime")
	}
	if out, err := exec.Command("systemctl", append(args, unitName+".timer")...).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %s %s.timer: %v - %s", strings.Join(args, " "), unitName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ResolveCommand makes the program of a command line absolute (see resolveCommand).
func ResolveCommand(command string) string { return resolveCommand(command) }

// InstallService writes a service unit into UnitDir and reloads systemd. Used
// for daemon-managed services (MinIO, vsftpd) that NixOS does not declare;
// on NixOS the unit is a runtime unit and the daemon re-installs it at start.
func InstallService(unitName, content string) error {
	dir := UnitDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("unit directory %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, unitName+".service"), []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write service unit: %v", err)
	}
	return DaemonReload()
}

// UninstallTimer removes the service and timer units
func UninstallTimer(name string) error {
	unitName := name
	if !strings.HasPrefix(unitName, Prefix) {
		unitName = Prefix + unitName
	}

	exec.Command("systemctl", "stop", unitName+".timer").Run()
	exec.Command("systemctl", "disable", unitName+".timer").Run()
	exec.Command("systemctl", "disable", "--runtime", unitName+".timer").Run()

	dir := UnitDir()
	os.Remove(filepath.Join(dir, unitName+".timer"))
	os.Remove(filepath.Join(dir, unitName+".service"))

	return DaemonReload()
}

// UninstallAllWithPrefix removes all units matching a prefix (e.g. dplaneos-scrub-)
func UninstallAllWithPrefix(match string) error {
	entries, err := os.ReadDir(UnitDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, match) && strings.HasSuffix(name, ".timer") {
			UninstallTimer(strings.TrimSuffix(name, ".timer"))
		}
	}
	return nil
}

// DaemonReload reloads the systemd manager configuration
func DaemonReload() error {
	cmd := exec.Command("systemctl", "daemon-reload")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload failed: %v - %s", err, string(out))
	}
	return nil
}
