package sysapply

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dplaned/internal/nixwriter"
)

// NUTDir holds the configuration of Network UPS Tools, written here from
// the UPS page. The services (dplaneos-upsdrv, dplaneos-upsd,
// dplaneos-upsmon in nixos/modules/ups.nix) read it from this directory and
// only start when their file exists.
const NUTDir = "/var/lib/dplaneos/nut"

var nutUnits = struct{ driver, server, monitor string }{
	"dplaneos-upsdrv.service", "dplaneos-upsd.service", "dplaneos-upsmon.service",
}

// ups writes the NUT configuration for the UPS and starts, restarts or
// stops the services to match.
func (e Env) ups(c nixwriter.UPSConfig) error {
	c, err := c.Normalize()
	if err != nil {
		return err
	}
	dir := e.path(NUTDir)
	files := map[string]string{} // name -> content; a missing name is removed

	local := c.Mode == "usb" || c.Mode == "driver"
	shutdown := "/run/current-system/sw/bin/systemctl poweroff"
	if c.Action == "hibernate" {
		shutdown = "/run/current-system/sw/bin/systemctl hibernate"
	}
	monitorTail := fmt.Sprintf("MINSUPPLIES 1\nSHUTDOWNCMD \"%s\"\nPOLLFREQ 5\nPOLLFREQALERT 5\nHOSTSYNC 15\nDEADTIME 15\nFINALDELAY %d\nPOWERDOWNFLAG /run/dplaneos/killpower\n", shutdown, c.FinalDelay)
	const header = "# Written by DPlaneOS (UPS page).\n"

	switch {
	case local:
		pass, err := e.localNUTPassword()
		if err != nil {
			return err
		}
		// override.battery.charge.low: the charge at which the UPS reports
		// "low battery", which is what starts the shutdown.
		files["ups.conf"] = header + fmt.Sprintf("maxretry = 3\n\n[%s]\n\tdriver = %s\n\tport = %s\n\toverride.battery.charge.low = %d\n\tignorelb\n",
			c.Name, c.Driver, c.Port, c.LowBattery)
		files["upsd.conf"] = header + "LISTEN 127.0.0.1 3493\n"
		files["upsd.users"] = header + fmt.Sprintf("[upsmon]\n\tpassword = %s\n\tupsmon primary\n", pass)
		files["upsmon.conf"] = header + fmt.Sprintf("MONITOR %s@localhost 1 upsmon %s primary\n", c.Name, pass) + monitorTail
	case c.Mode == "remote":
		if strings.ContainsAny(c.Password, " \t\n\r\"\\#") {
			return fmt.Errorf("the NUT password must not contain spaces, quotes, backslashes or #")
		}
		files["upsmon.conf"] = header + fmt.Sprintf("MONITOR %s@%s 1 %s %s secondary\n", c.Name, c.Host, c.User, c.Password) + monitorTail
	}

	changed := map[string]bool{}
	for _, name := range []string{"ups.conf", "upsd.conf", "upsd.users", "upsmon.conf"} {
		path := filepath.Join(dir, name)
		content, wanted := files[name]
		var ch bool
		if wanted {
			ch, err = writeIfChanged(path, content, 0640)
		} else {
			ch, err = removeIfPresent(path)
		}
		if err != nil {
			return err
		}
		changed[name] = ch
	}

	// Early boot: systemd starts whichever services have their file.
	if e.run("systemctl", "is-active", "--quiet", "multi-user.target") != nil {
		return nil
	}
	want := map[string]bool{nutUnits.driver: local, nutUnits.server: local, nutUnits.monitor: c.Mode != ""}
	reconfigured := map[string]bool{
		nutUnits.driver:  changed["ups.conf"],
		nutUnits.server:  changed["ups.conf"] || changed["upsd.conf"] || changed["upsd.users"],
		nutUnits.monitor: changed["upsmon.conf"] || changed["upsd.users"],
	}
	// Driver, then server, then monitor; stopped in the opposite order.
	for _, unit := range []string{nutUnits.monitor, nutUnits.server, nutUnits.driver} {
		if !want[unit] && e.run("systemctl", "is-active", "--quiet", unit) == nil {
			if err := e.run("systemctl", "stop", unit); err != nil {
				return err
			}
		}
	}
	for _, unit := range []string{nutUnits.driver, nutUnits.server, nutUnits.monitor} {
		if !want[unit] {
			continue
		}
		active := e.run("systemctl", "is-active", "--quiet", unit) == nil
		if active && !reconfigured[unit] {
			continue
		}
		if err := e.run("systemctl", "restart", unit); err != nil {
			return fmt.Errorf("%s: %w", strings.TrimSuffix(strings.TrimPrefix(unit, "dplaneos-"), ".service"), err)
		}
	}
	return nil
}

// localNUTPassword is the password the monitor uses with the local server;
// generated once.
func (e Env) localNUTPassword() (string, error) {
	path := filepath.Join(e.path(NUTDir), "upsmon.pass")
	if b, err := os.ReadFile(path); err == nil {
		if p := strings.TrimSpace(string(b)); len(p) >= 16 && !strings.ContainsAny(p, " \t\n\"\\#") {
			return p, nil
		}
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	pass := hex.EncodeToString(raw)
	if _, err := writeIfChanged(path, pass+"\n", 0600); err != nil {
		return "", err
	}
	return pass, nil
}
