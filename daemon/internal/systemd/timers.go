package systemd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	SystemdPath = "/etc/systemd/system"
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

// InstallTimer creates a .service and .timer unit file and reloads systemd
func InstallTimer(cfg TimerConfig) error {
	if cfg.Name == "" || cfg.Command == "" || cfg.OnCalendar == "" {
		return fmt.Errorf("invalid timer config: name, command, and onCalendar are required")
	}

	unitName := cfg.Name
	if !strings.HasPrefix(unitName, Prefix) {
		unitName = Prefix + unitName
	}

	serviceContent := fmt.Sprintf(`[Unit]
Description=%s
After=%s

[Service]
Type=oneshot
ExecStart=%s
User=root

[Install]
WantedBy=multi-user.target
`, cfg.Description, strings.Join(append(cfg.After, "network.target"), " "), cfg.Command)

	persistentStr := "false"
	if cfg.Persistent {
		persistentStr = "true"
	}

	timerContent := fmt.Sprintf(`[Unit]
Description=Timer for %s

[Timer]
OnCalendar=%s
Persistent=%s
Unit=%s.service

[Install]
WantedBy=timers.target
`, cfg.Description, cfg.OnCalendar, persistentStr, unitName)

	servicePath := filepath.Join(SystemdPath, unitName+".service")
	timerPath := filepath.Join(SystemdPath, unitName+".timer")

	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		return fmt.Errorf("failed to write service unit: %v", err)
	}
	if err := os.WriteFile(timerPath, []byte(timerContent), 0644); err != nil {
		return fmt.Errorf("failed to write timer unit: %v", err)
	}

	// Reload and enable
	if err := DaemonReload(); err != nil {
		return err
	}

	// We don't necessarily need to 'start' it here, but enabling it ensures it runs on boot.
	exec.Command("systemctl", "enable", "--now", unitName+".timer").Run()

	return nil
}

// UninstallTimer removes the service and timer units
func UninstallTimer(name string) error {
	unitName := name
	if !strings.HasPrefix(unitName, Prefix) {
		unitName = Prefix + unitName
	}

	exec.Command("systemctl", "stop", unitName+".timer").Run()
	exec.Command("systemctl", "disable", unitName+".timer").Run()

	os.Remove(filepath.Join(SystemdPath, unitName+".timer"))
	os.Remove(filepath.Join(SystemdPath, unitName+".service"))

	return DaemonReload()
}

// UninstallAllWithPrefix removes all units matching a prefix (e.g. dplaneos-scrub-)
func UninstallAllWithPrefix(match string) error {
	entries, err := os.ReadDir(SystemdPath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, match) && (strings.HasSuffix(name, ".timer") || strings.HasSuffix(name, ".service")) {
			// Extract base name without extension
			base := strings.TrimSuffix(name, filepath.Ext(name))
			UninstallTimer(base)
		}
	}
	return nil
}

// DaemonReload reloads the systemd manager configuration
func DaemonReload() error {
	// Use the whitelisted command name for validation if needed, 
	// but here we are in internal code, we can call exec.Command directly if we are root.
	// D-PlaneOS daemon runs as root usually.
	cmd := exec.Command("systemctl", "daemon-reload")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload failed: %v - %s", err, string(out))
	}
	return nil
}
