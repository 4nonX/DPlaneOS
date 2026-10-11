package nixwriter

import (
	"fmt"
	"regexp"
)

// UPSConfig is how the system is connected to its UPS and what it does when
// the battery runs low.
type UPSConfig struct {
	// Mode: "" (no UPS), "usb" (a UPS on a USB port), "driver" (a UPS on a
	// serial port or one that needs a particular driver), "remote" (a UPS
	// attached to another machine that runs a NUT server).
	Mode string
	// Name of the UPS: a label for "usb" and "driver", the name on the
	// server for "remote".
	Name string
	// Driver and Port, for "driver" (e.g. apcsmart on /dev/ttyS0).
	Driver string
	Port   string
	// Host ("nas2" or "nas2:3493"), User and Password, for "remote".
	// The password is kept in the state file (readable by root only), like
	// the NUT configuration written from it.
	Host     string
	User     string
	Password string

	Action     string // "shutdown" or "hibernate"
	LowBattery int    // percent at which the battery counts as low (1-99)
	FinalDelay int    // seconds between the warning and the shutdown (0-600)
}

var (
	upsNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
	upsDriverRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,31}$`)
	upsPortRe   = regexp.MustCompile(`^(auto|/dev/[A-Za-z0-9/_.:-]{1,64}|/var/lib/dplaneos/nut/[A-Za-z0-9_.-]{1,64}|[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?)$`)
	upsHostRe   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?(:[0-9]{1,5})?$`)
	upsUserRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
)

// Normalize validates the configuration and fills in what a mode implies.
func (c UPSConfig) Normalize() (UPSConfig, error) {
	if c.Action == "" {
		c.Action = "shutdown"
	}
	if c.Action != "shutdown" && c.Action != "hibernate" {
		return c, fmt.Errorf("invalid UPS action %q", c.Action)
	}
	if c.LowBattery == 0 {
		c.LowBattery = 20
	}
	if c.LowBattery < 1 || c.LowBattery > 99 {
		return c, fmt.Errorf("low battery level must be 1-99 percent")
	}
	if c.FinalDelay < 0 || c.FinalDelay > 600 {
		return c, fmt.Errorf("delay must be 0-600 seconds")
	}
	if c.Mode == "" {
		c.Name, c.Driver, c.Port, c.Host, c.User, c.Password = "", "", "", "", "", ""
		return c, nil
	}
	if c.Name == "" {
		c.Name = "ups"
	}
	if !upsNameRe.MatchString(c.Name) {
		return c, fmt.Errorf("UPS name %q: letters, digits, - and _ only", c.Name)
	}
	switch c.Mode {
	case "usb":
		c.Driver, c.Port = "usbhid-ups", "auto"
		c.Host, c.User, c.Password = "", "", ""
	case "driver":
		if !upsDriverRe.MatchString(c.Driver) {
			return c, fmt.Errorf("invalid UPS driver %q", c.Driver)
		}
		if c.Port == "" {
			c.Port = "auto"
		}
		if !upsPortRe.MatchString(c.Port) {
			return c, fmt.Errorf("invalid UPS port %q", c.Port)
		}
		c.Host, c.User, c.Password = "", "", ""
	case "remote":
		if !upsHostRe.MatchString(c.Host) {
			return c, fmt.Errorf("invalid NUT server %q", c.Host)
		}
		if !upsUserRe.MatchString(c.User) {
			return c, fmt.Errorf("invalid NUT user name %q", c.User)
		}
		if c.Password == "" {
			return c, fmt.Errorf("the NUT server's password for %s is required", c.User)
		}
		c.Driver, c.Port = "", ""
	default:
		return c, fmt.Errorf("invalid UPS connection %q", c.Mode)
	}
	return c, nil
}

// UPS returns the UPS configuration from the state.
func (s DPlaneState) UPS() UPSConfig {
	return UPSConfig{
		Mode: s.UPSMode, Name: s.UPSName, Driver: s.UPSDriver, Port: s.UPSPort,
		Host: s.UPSHost, User: s.UPSUser, Password: s.UPSPassword,
		Action: s.UPSAction, LowBattery: s.UPSLowBattery, FinalDelay: s.UPSFinalDelay,
	}
}

// SetUPS records the UPS connection and shutdown policy.
func (w *Writer) SetUPS(c UPSConfig) error {
	c, err := c.Normalize()
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.UPSMode, w.state.UPSName, w.state.UPSDriver, w.state.UPSPort = c.Mode, c.Name, c.Driver, c.Port
	w.state.UPSHost, w.state.UPSUser, w.state.UPSPassword = c.Host, c.User, c.Password
	w.state.UPSAction, w.state.UPSLowBattery, w.state.UPSFinalDelay = c.Action, c.LowBattery, c.FinalDelay
	return w.flushLocked()
}
