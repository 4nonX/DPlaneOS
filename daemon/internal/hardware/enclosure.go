package hardware

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const enclosureSysBase = "/sys/class/enclosure"

// Enclosure represents one SES enclosure exposed via sysfs.
type Enclosure struct {
	ID      string `json:"id"`
	SysPath string `json:"sys_path"`
	Slots   []Slot `json:"slots"`
}

// Slot is one bay inside an enclosure.
type Slot struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Locate bool   `json:"locate"`
	Fault  bool   `json:"fault"`
	Type   string `json:"type,omitempty"`
}

// SESElement is one parsed element from sg_ses --page=es output.
type SESElement struct {
	Type       string `json:"type"`
	Descriptor string `json:"descriptor"`
	Status     string `json:"status"`
	Details    string `json:"details,omitempty"`
}

var enclosureIDRe = regexp.MustCompile(`^[a-zA-Z0-9:_\-]+$`)
var sgDevRe = regexp.MustCompile(`^/dev/sg[0-9]+$`)

// ListEnclosures enumerates all enclosures via /sys/class/enclosure.
// Returns nil (no error) when the enclosure subsystem is absent.
func ListEnclosures() ([]Enclosure, error) {
	entries, err := os.ReadDir(enclosureSysBase)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read enclosure sysfs: %w", err)
	}
	var out []Enclosure
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		enc, err := readEnclosure(e.Name())
		if err != nil {
			continue
		}
		out = append(out, enc)
	}
	return out, nil
}

func readEnclosure(id string) (Enclosure, error) {
	sysPath := filepath.Join(enclosureSysBase, id)
	entries, err := os.ReadDir(sysPath)
	if err != nil {
		return Enclosure{}, err
	}
	enc := Enclosure{ID: id, SysPath: sysPath}
	idx := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slotPath := filepath.Join(sysPath, e.Name())
		if _, statErr := os.Stat(filepath.Join(slotPath, "status")); statErr != nil {
			continue
		}
		enc.Slots = append(enc.Slots, readSlot(slotPath, e.Name(), idx))
		idx++
	}
	return enc, nil
}

func readSlot(slotPath, name string, idx int) Slot {
	return Slot{
		Index:  idx,
		Name:   name,
		Status: readSysFile(filepath.Join(slotPath, "status")),
		Locate: readSysFile(filepath.Join(slotPath, "locate")) == "1",
		Fault:  readSysFile(filepath.Join(slotPath, "fault")) == "1",
		Type:   readSysFile(filepath.Join(slotPath, "type")),
	}
}

func readSysFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SetLocateLED writes 1 or 0 to the locate sysfs attribute for a slot.
// slotIndex must be the Index value returned by ListEnclosures.
func SetLocateLED(enclosureID string, slotIndex int, on bool) error {
	if !enclosureIDRe.MatchString(enclosureID) {
		return fmt.Errorf("invalid enclosure ID")
	}
	if slotIndex < 0 {
		return fmt.Errorf("invalid slot index")
	}
	enc, err := readEnclosure(enclosureID)
	if err != nil {
		return fmt.Errorf("read enclosure: %w", err)
	}
	if slotIndex >= len(enc.Slots) {
		return fmt.Errorf("slot index %d out of range (enclosure has %d slots)", slotIndex, len(enc.Slots))
	}
	slotName := enc.Slots[slotIndex].Name
	p := filepath.Join(enclosureSysBase, enclosureID, slotName, "locate")
	clean := filepath.Clean(p)
	// Guard against path traversal in case slotName contained dots.
	if !strings.HasPrefix(clean, enclosureSysBase+string(filepath.Separator)) {
		return fmt.Errorf("invalid locate path")
	}
	val := []byte("0")
	if on {
		val = []byte("1")
	}
	return os.WriteFile(clean, val, 0644)
}

// FindSGDevice resolves the /dev/sgN device for an enclosure by following the
// sysfs symlink and inspecting the scsi_generic child directory.
func FindSGDevice(enclosureID string) (string, error) {
	if !enclosureIDRe.MatchString(enclosureID) {
		return "", fmt.Errorf("invalid enclosure ID")
	}
	encPath := filepath.Join(enclosureSysBase, enclosureID)
	realPath, err := filepath.EvalSymlinks(encPath)
	if err != nil {
		return "", fmt.Errorf("resolve enclosure path: %w", err)
	}
	sgDir := filepath.Join(realPath, "scsi_generic")
	entries, err := os.ReadDir(sgDir)
	if err != nil {
		return "", fmt.Errorf("no scsi_generic for enclosure %s: %w", enclosureID, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "sg") {
			dev := "/dev/" + e.Name()
			if sgDevRe.MatchString(dev) {
				return dev, nil
			}
		}
	}
	return "", fmt.Errorf("no sg device found for enclosure %s", enclosureID)
}

