package bmc

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ipmiRun executes ipmitool with LAN+ interface. Password is passed via the
// IPMI_PASSWORD environment variable, never in command-line arguments.
// timeoutSeconds is the maximum execution time in seconds.
func ipmiRun(timeoutSeconds int, host, user, password string, args []string) error {
	_, err := ipmiOutput(timeoutSeconds, host, user, password, args)
	return err
}

// ipmiOutput executes ipmitool and returns stdout.
// password is the plaintext password (loaded from the secure file by the caller).
func ipmiOutput(timeoutSeconds int, host, user, password string, args []string) (string, error) {
	baseArgs := []string{"-I", "lanplus", "-H", host}
	if user != "" {
		baseArgs = append(baseArgs, "-U", user, "-E")
	}
	fullArgs := append(baseArgs, args...)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ipmitool", fullArgs...)

	if password != "" {
		cmd.Env = append(cmd.Environ(), "IPMI_PASSWORD="+password)
	}

	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// GetHealthIPMI fetches sensor data via ipmitool sdr.
func GetHealthIPMI(host, user, password string) (Health, error) {
	out, err := ipmiOutput(30, host, user, password, []string{"sdr", "elist", "all"})
	if err != nil {
		return Health{}, fmt.Errorf("ipmitool sdr: %w", err)
	}

	h := Health{CollectedAt: time.Now(), OverallStatus: "ok", PowerState: PowerUnknown}

	for _, line := range strings.Split(out, "\n") {
		name, valueStr, statusStr, ok := parseSDRLine(line)
		if !ok {
			continue
		}

		if name == "" || valueStr == "na" || valueStr == "No Reading" {
			continue
		}

		var value float64
		var unit string
		valueParts := strings.Fields(valueStr)
		if len(valueParts) >= 2 {
			fmt.Sscanf(valueParts[0], "%f", &value)
			unit = valueParts[1]
		} else if len(valueParts) == 1 {
			fmt.Sscanf(valueParts[0], "%f", &value)
		}

		category := classifyIPMISensor(name, unit)
		status := ipmiSensorStatus(statusStr)

		h.Sensors = append(h.Sensors, Sensor{
			Name:     name,
			Value:    value,
			Unit:     unit,
			Status:   status,
			Category: category,
		})

		if status == "critical" {
			h.OverallStatus = "critical"
		} else if status == "warning" && h.OverallStatus == "ok" {
			h.OverallStatus = "warning"
		}
	}

	// Get chassis power state
	if powerOut, err := ipmiOutput(10, host, user, password,
		[]string{"chassis", "power", "status"}); err == nil {
		if strings.Contains(powerOut, "on") {
			h.PowerState = PowerOn
		} else if strings.Contains(powerOut, "off") {
			h.PowerState = PowerOff
		}
	}

	return h, nil
}

// parseSDRLine reads one line of `ipmitool sdr elist` (name | sensor id |
// status | entity | reading) or `sdr list` (name | reading | status). The
// former parser read every line as the latter, so with elist (the command
// used) a value came from the sensor id ("30h") and the status from the
// reading.
func parseSDRLine(line string) (name, reading, status string, ok bool) {
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	switch len(parts) {
	case 5:
		name, status, reading = parts[0], parts[2], parts[4]
	case 3:
		name, reading, status = parts[0], parts[1], parts[2]
	default:
		return "", "", "", false
	}
	return name, reading, strings.ToLower(status), name != ""
}

// GetEventsIPMI fetches the BMC System Event Log via ipmitool sel.
func GetEventsIPMI(host, user, password string, limit int) ([]Event, error) {
	args := []string{"sel", "list"}
	if limit > 0 {
		args = []string{"sel", "list", "last", fmt.Sprintf("%d", limit)}
	}
	out, err := ipmiOutput(30, host, user, password, args)
	if err != nil {
		return nil, fmt.Errorf("ipmitool sel: %w", err)
	}

	var events []Event
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// IPMI SEL format: "ID | Date/Time | Sensor | Event | Direction | Value"
		parts := strings.Split(line, " | ")
		if len(parts) < 4 {
			continue
		}
		msg := strings.Join(parts[2:], " - ")
		events = append(events, Event{
			ID:        strings.TrimSpace(parts[0]),
			Timestamp: parseIPMITime(strings.TrimSpace(parts[1])),
			Severity:  SeverityInfo,
			Message:   msg,
		})
	}
	return events, nil
}

// PowerActionIPMI sends a chassis power command via ipmitool.
func PowerActionIPMI(host, user, password string, action PowerAction) error {
	cmd := ipmiPowerCmd(action)
	if cmd == "" {
		return fmt.Errorf("unsupported power action: %s", action)
	}
	return ipmiRun(30, host, user, password, []string{"chassis", "power", cmd})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func classifyIPMISensor(name, unit string) string {
	nameLow := strings.ToLower(name)
	unitLow := strings.ToLower(unit)
	switch {
	case strings.Contains(nameLow, "temp") || strings.Contains(unitLow, "celsius") || strings.Contains(unitLow, "degrees"):
		return "temperature"
	case strings.Contains(nameLow, "fan") || strings.Contains(unitLow, "rpm"):
		return "fan"
	case strings.Contains(nameLow, "power") || strings.Contains(nameLow, "psu") || strings.Contains(unitLow, "watts"):
		return "power"
	case strings.Contains(nameLow, "volt") || strings.Contains(unitLow, "volts"):
		return "voltage"
	default:
		return "other"
	}
}

func ipmiSensorStatus(s string) string {
	// ipmitool's sdr elist states: ok, nc/lnc/unc (non-critical), cr/lcr/ucr
	// (critical), nr/lnr/unr (non-recoverable), ns (no reading).
	switch s {
	case "ok":
		return "ok"
	case "nc", "lnc", "unc":
		return "warning"
	case "cr", "lcr", "ucr", "nr", "lnr", "unr":
		return "critical"
	case "ns":
		return "unknown"
	}
	switch {
	case strings.Contains(s, "ok") || strings.Contains(s, "nominal"):
		return "ok"
	case strings.Contains(s, "warn") || strings.Contains(s, "upper non-critical") || strings.Contains(s, "lower non-critical"):
		return "warning"
	case strings.Contains(s, "crit") || strings.Contains(s, "upper critical") || strings.Contains(s, "lower critical") || strings.Contains(s, "fail"):
		return "critical"
	default:
		return "unknown"
	}
}

func ipmiPowerCmd(action PowerAction) string {
	switch action {
	case PowerActionOff:
		return "off"
	case PowerActionGracefulOff:
		return "soft"
	case PowerActionOn:
		return "on"
	case PowerActionReset:
		return "reset"
	case PowerActionGracefulReset:
		return "reset"
	default:
		return ""
	}
}

func parseIPMITime(s string) time.Time {
	t, err := time.Parse("01/02/2006 15:04:05", s)
	if err != nil {
		return time.Time{}
	}
	return t
}
