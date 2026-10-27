// Package bmc provides a unified interface for out-of-band hardware management
// across iLO (HPE), iDRAC (Dell), and generic IPMI implementations.
//
// Detection order:
//  1. Redfish (DMTF standard, supported by iLO 5+, iDRAC 9+, and most modern BMCs)
//  2. iLO 4 proprietary REST (legacy HPE, pre-Redfish)
//  3. IPMI via ipmitool (universal fallback)
//
// Credentials are read from the ha_fencing_config table (bmc_ip, bmc_user,
// bmc_password_file). The same credentials serve both STONITH fencing and
// health monitoring.
package bmc

import "time"

// Protocol identifies the communication method in use for a BMC.
type Protocol string

const (
	ProtocolRedfish Protocol = "redfish" // DMTF Redfish (iLO 5+, iDRAC 9+)
	ProtocolILO4    Protocol = "ilo4"    // HPE iLO 4 proprietary REST
	ProtocolIPMI    Protocol = "ipmi"    // Generic IPMI via ipmitool
	ProtocolUnknown Protocol = "unknown" // Not yet probed
	ProtocolNone    Protocol = "none"    // BMC not reachable / not configured
)

// Info describes the detected BMC.
type Info struct {
	Protocol        Protocol  `json:"protocol"`
	Vendor          string    `json:"vendor"`           // "HPE", "Dell", "Lenovo", "Generic", ""
	Model           string    `json:"model"`            // "iLO 5", "iDRAC 9", ""
	FirmwareVersion string    `json:"firmware_version"` // from BMC
	Hostname        string    `json:"hostname"`         // BMC IP or hostname
	ReachableAt     time.Time `json:"reachable_at"`
}

// Sensor represents one hardware sensor reading.
type Sensor struct {
	Name     string  `json:"name"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit"`     // "Celsius", "RPM", "Watts", "Volts", "%"
	Status   string  `json:"status"`   // "ok", "warning", "critical", "unknown"
	Category string  `json:"category"` // "temperature", "fan", "power", "voltage", "other"
}

// PowerState is the chassis power state.
type PowerState string

const (
	PowerOn      PowerState = "on"
	PowerOff     PowerState = "off"
	PowerUnknown PowerState = "unknown"
)

// Health aggregates all sensor readings for one system.
type Health struct {
	PowerState    PowerState `json:"power_state"`
	Sensors       []Sensor   `json:"sensors"`
	OverallStatus string     `json:"overall_status"` // "ok", "warning", "critical", "unknown"
	CollectedAt   time.Time  `json:"collected_at"`
}

// PowerAction is a chassis power management command.
type PowerAction string

const (
	PowerActionOff           PowerAction = "off"            // immediate power cut
	PowerActionGracefulOff   PowerAction = "graceful_off"   // ACPI shutdown signal
	PowerActionOn            PowerAction = "on"             // power on from off state
	PowerActionReset         PowerAction = "reset"          // hard reset
	PowerActionGracefulReset PowerAction = "graceful_reset" // OS-level reboot
)

// EventSeverity maps BMC event severity levels.
type EventSeverity string

const (
	SeverityOK       EventSeverity = "ok"
	SeverityWarning  EventSeverity = "warning"
	SeverityCritical EventSeverity = "critical"
	SeverityInfo     EventSeverity = "info"
)

// Event is one entry from the BMC system event log.
type Event struct {
	ID        string        `json:"id"`
	Timestamp time.Time     `json:"timestamp"`
	Severity  EventSeverity `json:"severity"`
	Message   string        `json:"message"`
	Source    string        `json:"source"` // sensor or component that generated the event
}

// Credentials holds BMC access information. The password is read from the
// file path and held in memory only for the duration of the request.
type Credentials struct {
	Host          string
	Username      string
	Password      string // transient - zero after use
	SkipTLSVerify bool   // BMCs commonly ship self-signed certs; always true in practice
}
