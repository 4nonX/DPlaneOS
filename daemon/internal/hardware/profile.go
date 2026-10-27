// Package hardware provides hardware detection and profiling.
// Phase 2.1: Hardware-agnostic operation with graceful fallback.
package hardware

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Profile describes the hardware capabilities of this appliance.
// Used to enable/disable features based on what's actually available.
type Profile struct {
	Timestamp time.Time
	Hostname  string
	OS        string
	Kernel    string
	CPUCount  int
	MemoryGB  int

	// BMC/Out-of-band management
	BMCType    string // "redfish", "ipmi", "ilo4", "idrac", "none"
	BMCAddress string

	// Storage controllers
	SASControllers  int // Number of SAS RAID controllers
	SASSlotsTotal   int
	NVMeControllers int

	// Enclosures
	HasSESSupport bool // SCSI Enclosure Services available
	SESEnclosures int

	// Networking
	BondingSupport bool
	VLANSupport    bool
	NICs           int

	// Features implied by hardware
	Capabilities map[string]bool // Feature → available (e.g., "ha_clustering" → true)
	Warnings     []string        // Non-fatal detection issues
	Errors       []string        // Fatal detection issues
}

// Detector detects hardware capabilities.
type Detector struct {
	log func(string, ...interface{})
}

// NewDetector creates a hardware detector.
func NewDetector() *Detector {
	return &Detector{
		log: func(msg string, args ...interface{}) {
			log.Printf("[HARDWARE] "+msg, args...)
		},
	}
}

// Detect runs hardware detection and returns a profile.
func (d *Detector) Detect(ctx context.Context) *Profile {
	p := &Profile{
		Timestamp:    time.Now(),
		Capabilities: make(map[string]bool),
		Warnings:     []string{},
		Errors:       []string{},
	}

	d.log("Starting hardware detection...")

	// Basic system info
	d.detectBasicInfo(p)

	// BMC detection
	d.detectBMC(p)

	// Storage controller detection
	d.detectStorageControllers(p)

	// Enclosure detection
	d.detectEnclosures(p)

	// Network capability detection
	d.detectNetworking(p)

	// Infer feature capabilities from hardware
	d.inferCapabilities(p)

	d.log("Hardware detection complete: %d warnings, %d errors", len(p.Warnings), len(p.Errors))
	for _, w := range p.Warnings {
		d.log("WARNING: %s", w)
	}
	for _, e := range p.Errors {
		d.log("ERROR: %s", e)
	}

	return p
}

func (d *Detector) detectBasicInfo(p *Profile) {
	var err error
	p.Hostname, _ = os.Hostname()

	// OS
	out, err := exec.Command("uname", "-s").Output()
	if err == nil {
		p.OS = strings.TrimSpace(string(out))
	}

	// Kernel
	out, err = exec.Command("uname", "-r").Output()
	if err == nil {
		p.Kernel = strings.TrimSpace(string(out))
	}

	// CPU count
	out, err = exec.Command("nproc").Output()
	if err == nil {
		fmt.Sscanf(string(out), "%d", &p.CPUCount)
	}

	// Memory
	out, err = exec.Command("free", "-g").Output()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		if len(lines) > 1 {
			fields := strings.Fields(lines[1])
			if len(fields) > 1 {
				fmt.Sscanf(fields[1], "%d", &p.MemoryGB)
			}
		}
	}

	d.log("Basic info: %s (OS=%s, kernel=%s, CPU=%d, RAM=%dGB)",
		p.Hostname, p.OS, p.Kernel, p.CPUCount, p.MemoryGB)
}

func (d *Detector) detectBMC(p *Profile) {
	// Try Redfish first (modern IPMI)
	if d.testRedfish() {
		p.BMCType = "redfish"
		d.log("Detected Redfish BMC")
		return
	}

	// Try iLO 5+ (HP)
	if d.testILO5() {
		p.BMCType = "ilo5"
		d.log("Detected iLO 5+ BMC")
		return
	}

	// Try iLO 4 (legacy HP)
	if d.testILO4() {
		p.BMCType = "ilo4"
		d.log("Detected iLO 4 BMC (legacy)")
		p.Warnings = append(p.Warnings, "iLO 4 is legacy; upgrade to iLO 5+ for best compatibility")
		return
	}

	// Try generic IPMI
	if d.testIPMI() {
		p.BMCType = "ipmi"
		d.log("Detected generic IPMI BMC")
		return
	}

	// No BMC found
	p.BMCType = "none"
	p.Warnings = append(p.Warnings, "No BMC detected; automatic fencing not available (HA will use SBD/network witness)")
	d.log("No BMC detected")
}

func (d *Detector) detectStorageControllers(p *Profile) {
	// Detect SAS RAID controllers
	out, _ := exec.Command("lsscsi", "-H").Output()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "scsi") {
			p.SASControllers++
		}
	}

	// Detect NVMe controllers
	out, _ = exec.Command("nvme", "list").Output()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Device") {
			p.NVMeControllers++
		}
	}

	d.log("Storage: %d SAS controllers, %d NVMe controllers", p.SASControllers, p.NVMeControllers)
}

func (d *Detector) detectEnclosures(p *Profile) {
	// Check for SES (SCSI Enclosure Services) support
	out, err := exec.Command("lsscsi", "-g").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "enclosure") || strings.Contains(line, "SES") {
				p.HasSESSupport = true
				p.SESEnclosures++
			}
		}
	}

	if p.HasSESSupport {
		d.log("SES enclosure support available: %d enclosures", p.SESEnclosures)
	} else {
		p.Warnings = append(p.Warnings, "No SES enclosure support; will use S.M.A.R.T monitoring only")
		d.log("No SES support; degrading to S.M.A.R.T")
	}
}

func (d *Detector) detectNetworking(p *Profile) {
	// Check for bonding support
	if _, err := os.Stat("/sys/class/net/bonding_masters"); err == nil {
		p.BondingSupport = true
	}

	// Check for VLAN support
	if _, err := os.Stat("/proc/net/vlan"); err == nil {
		p.VLANSupport = true
	}

	// Count NICs
	files, _ := os.ReadDir("/sys/class/net")
	p.NICs = len(files)

	d.log("Networking: %d NICs, bonding=%v, VLAN=%v", p.NICs, p.BondingSupport, p.VLANSupport)
}

func (d *Detector) inferCapabilities(p *Profile) {
	// ha_clustering: requires BMC OR HA modules
	p.Capabilities["ha_clustering"] = p.BMCType != "none" || p.SASControllers > 0
	if !p.Capabilities["ha_clustering"] {
		p.Warnings = append(p.Warnings, "HA clustering not recommended: no BMC and no shared storage detected")
	}

	// nvmeof_support: requires NVMe controllers
	p.Capabilities["nvmeof_support"] = p.NVMeControllers > 0
	if !p.Capabilities["nvmeof_support"] {
		d.log("NVMe-oF not available: no NVMe controllers detected")
	}

	// ses_enclosure: requires SES support
	p.Capabilities["ses_enclosure"] = p.HasSESSupport

	// bonding: requires kernel support
	p.Capabilities["bonding"] = p.BondingSupport
	if !p.BondingSupport {
		p.Warnings = append(p.Warnings, "Bonding not available; LACP not supported on this system")
	}

	// vlan: requires kernel support
	p.Capabilities["vlan"] = p.VLANSupport
	if !p.VLANSupport {
		p.Warnings = append(p.Warnings, "VLAN not available; 802.1Q tagging not supported on this system")
	}

	// Assume stable features always available
	p.Capabilities["zfs_storage"] = true
	p.Capabilities["docker_containers"] = true
	p.Capabilities["smb_sharing"] = true
	p.Capabilities["nfs_sharing"] = true
}

// BMC detection helpers
func (d *Detector) testRedfish() bool {
	cmd := exec.Command("curl", "-s", "-m", "2", "https://localhost/redfish/v1/Systems")
	return cmd.Run() == nil
}

func (d *Detector) testILO5() bool {
	cmd := exec.Command("curl", "-s", "-m", "2", "https://localhost/ilo/api/v1/Sessions")
	return cmd.Run() == nil
}

func (d *Detector) testILO4() bool {
	cmd := exec.Command("curl", "-s", "-m", "2", "https://localhost/ilojlm/login.aspx")
	return cmd.Run() == nil
}

func (d *Detector) testIPMI() bool {
	cmd := exec.Command("ipmitool", "chassis", "status")
	return cmd.Run() == nil
}
