package systemd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// Command is a program with fixed arguments (no shell). Either this or
	// Hook is set.
	Command string
	// Hook makes the timer call the daemon's own API.
	Hook       *Hook
	OnCalendar string // e.g. "Mon *-*-* 04:00:00"
	Persistent bool
	After      []string
}

// Hook is a request a timer makes to the daemon over its socket: a POST of
// Payload to Path, with the daemon's internal token.
//
// Nothing of it goes into the unit: the unit runs "curl -K <file>", and the
// request (with the token) is in that file and a payload file next to it,
// readable by root only. There is no shell, and no name from a request (a
// dataset, a device) is ever part of a command line. (The units used to run
// bash -c "curl ... -d '<json>'", with the token in a world-readable unit.)
type Hook struct {
	Path    string // e.g. /api/zfs/snapshots/cron-hook
	Token   string
	Payload []byte // JSON
}

// Replaced in tests.
var (
	// HookDir holds the request files of hook timers. Like the runtime
	// units it is gone after a restart; the daemon writes both again when
	// it starts.
	HookDir = "/run/dplaneos/timers"
	// Socket is the daemon's socket.
	Socket = "/run/dplaneos/dplaned.sock"
)

var (
	unitNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]{0,200}$`)
	hookPathRe = regexp.MustCompile(`^/api/[a-z0-9/_-]+$`)
	tokenRe    = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)
)

// oneLine makes a text safe as the value of a unit setting: a line break in
// it would start a setting of its own (an ExecStart, say), and "%" starts a
// specifier.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return strings.ReplaceAll(strings.TrimSpace(s), "%", "%%")
}

// writeHook writes the request files of a hook timer and returns the
// command of its unit.
func writeHook(unitName string, h *Hook) (string, error) {
	if !hookPathRe.MatchString(h.Path) {
		return "", fmt.Errorf("invalid hook path %q", h.Path)
	}
	if !tokenRe.MatchString(h.Token) {
		return "", fmt.Errorf("the internal token is missing or malformed")
	}
	if err := os.MkdirAll(HookDir, 0o700); err != nil {
		return "", err
	}
	payload := filepath.Join(HookDir, unitName+".json")
	config := filepath.Join(HookDir, unitName+".curl")
	if err := os.WriteFile(payload, h.Payload, 0o600); err != nil {
		return "", err
	}
	cfg := fmt.Sprintf(`# Written by DPlaneOS: the request of %s.timer.
silent
fail
unix-socket = "%s"
request = "POST"
url = "http://localhost%s"
header = "Content-Type: application/json"
header = "X-Internal-Token: %s"
data-binary = "@%s"
`, unitName, Socket, h.Path, h.Token, payload)
	if err := os.WriteFile(config, []byte(cfg), 0o600); err != nil {
		return "", err
	}
	return "curl -K " + config, nil
}

func removeHook(unitName string) {
	os.Remove(filepath.Join(HookDir, unitName+".json"))
	os.Remove(filepath.Join(HookDir, unitName+".curl"))
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
// contains none of the tools (bash, curl, zpool) these units run, so a program
// that cannot be resolved is an error: the unit would install but never run.
func resolveCommand(command string) (string, error) {
	prog, rest, _ := strings.Cut(strings.TrimSpace(command), " ")
	if strings.HasPrefix(prog, "/") {
		return command, nil
	}
	abs, err := exec.LookPath(prog)
	if err != nil {
		return "", fmt.Errorf("%s is not on the daemon's PATH: %w", prog, err)
	}
	if rest == "" {
		return abs, nil
	}
	return abs + " " + rest, nil
}

// renderUnits returns the .service and .timer contents. pathEnv is passed to
// the service so commands run inside it (e.g. curl in bash -c) resolve too.
func renderUnits(cfg TimerConfig, unitName, pathEnv string) (service, timer string, err error) {
	execStart, err := resolveCommand(cfg.Command)
	if err != nil {
		return "", "", err
	}
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
`, oneLine(cfg.Description), strings.Join(append(cfg.After, "network.target"), " "), env, execStart)

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
`, oneLine(cfg.Description), cfg.OnCalendar, persistentStr, unitName)
	return service, timer, nil
}

// InstallTimer creates a .service and .timer unit file and reloads systemd
func InstallTimer(cfg TimerConfig) error {
	if cfg.Name == "" || (cfg.Command == "" && cfg.Hook == nil) || cfg.OnCalendar == "" {
		return fmt.Errorf("invalid timer config: name, command, and onCalendar are required")
	}

	unitName := cfg.Name
	if !strings.HasPrefix(unitName, Prefix) {
		unitName = Prefix + unitName
	}
	// The name becomes file names, the schedule a line of the unit.
	if !unitNameRe.MatchString(unitName) {
		return fmt.Errorf("invalid timer name %q", unitName)
	}
	if strings.ContainsAny(cfg.OnCalendar, "\n\r%") || strings.TrimSpace(cfg.OnCalendar) == "" {
		return fmt.Errorf("timer %s: invalid schedule %q", unitName, cfg.OnCalendar)
	}
	if strings.ContainsAny(cfg.Command, "\n\r") {
		return fmt.Errorf("timer %s: invalid command", unitName)
	}
	if cfg.Hook != nil {
		command, err := writeHook(unitName, cfg.Hook)
		if err != nil {
			return fmt.Errorf("timer %s: %w", unitName, err)
		}
		cfg.Command = command
	}

	serviceContent, timerContent, err := renderUnits(cfg, unitName, os.Getenv("PATH"))
	if err != nil {
		return fmt.Errorf("timer %s: %w", unitName, err)
	}
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
func ResolveCommand(command string) (string, error) { return resolveCommand(command) }

// InstallService writes a service unit into UnitDir and reloads systemd. Used
// for daemon-managed services (the S3 gateway, vsftpd) that NixOS does not declare;
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
	removeHook(unitName)

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
