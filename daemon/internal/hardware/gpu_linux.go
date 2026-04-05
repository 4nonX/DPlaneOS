//go:build linux

package hardware

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	lspciBusRe   = regexp.MustCompile(`^([0-9a-f]{2}:[0-9a-f]{2}\.[0-9])`)
	lspciClassRe = regexp.MustCompile(`\[([0-9a-f]{4})\]:\s*`)
	lspciVidDid  = regexp.MustCompile(`\[([0-9a-f]{4}):([0-9a-f]{4})\]`)
)

// displayClass reports whether a PCI class code is a GPU / display controller.
func displayClass(code string) bool {
	switch code {
	case "0300", "0301", "0302", "0380", "0680":
		return true
	default:
		return false
	}
}

// BuildGPUPassthroughReport probes lspci, /dev/dri, nvidia-smi, and docker.
func BuildGPUPassthroughReport() (*GPUPassthroughReport, error) {
	rep := &GPUPassthroughReport{
		NixOSDockerNvidiaOption: "services.dplaneos.docker.enableNvidia",
		ComposeExamples:         DefaultComposeExamples(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	rep.PCIDevices = probeLspci(ctx)
	rep.DRINodes = probeDRI()
	rep.NVIDIAGPUs, rep.NVIDIADriverOK = probeNvidiaSMI(ctx)
	rep.DockerRuntimes, rep.NVIDIARuntimeAvailable = probeDockerRuntimes(ctx)

	rep.ComposeHints.CanPassDRIDevices = len(rep.DRINodes) > 0
	rep.ComposeHints.CanUseNVIDIADeviceReservation = rep.NVIDIARuntimeAvailable && rep.NVIDIADriverOK

	return rep, nil
}

func probeLspci(ctx context.Context) []PCIGPUDevice {
	out, err := exec.CommandContext(ctx, "lspci", "-nn").Output()
	if err != nil {
		return nil
	}
	var list []PCIGPUDevice
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		d := parseLspciLine(line)
		if d == nil || !displayClass(d.ClassCode) {
			continue
		}
		list = append(list, *d)
	}
	return list
}

func parseLspciLine(line string) *PCIGPUDevice {
	d := &PCIGPUDevice{RawLine: line}
	if bm := lspciBusRe.FindStringSubmatch(line); len(bm) >= 2 {
		d.BusID = bm[1]
	}
	if cm := lspciClassRe.FindStringSubmatch(line); len(cm) >= 2 {
		d.ClassCode = cm[1]
	}
	if idx := strings.Index(line, "]:"); idx >= 0 {
		rest := line[idx+2:]
		if vm := lspciVidDid.FindStringIndex(rest); vm != nil {
			d.VendorName = strings.TrimSpace(rest[:vm[0]])
		} else {
			d.VendorName = strings.TrimSpace(rest)
		}
	}
	var vid, did string
	for _, m := range lspciVidDid.FindAllStringSubmatch(line, -1) {
		if len(m) >= 3 {
			vid, did = m[1], m[2]
		}
	}
	d.VendorID, d.DeviceID = vid, did
	if d.Class == "" && d.BusID != "" && d.ClassCode != "" {
		if i := strings.Index(line, "["+d.ClassCode+"]"); i > len(d.BusID) {
			d.Class = strings.TrimSpace(line[len(d.BusID)+1 : i])
		}
	}
	return d
}

func probeDRI() []DRINode {
	dir := "/dev/dri"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var nodes []DRINode
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		full := filepath.Join(dir, name)
		st, err := os.Stat(full)
		if err != nil {
			continue
		}
		mode := st.Mode()
		if mode&os.ModeCharDevice == 0 {
			continue
		}
		nodes = append(nodes, DRINode{
			Name:     name,
			Path:     full,
			Mode:     mode.String(),
			IsRender: strings.HasPrefix(name, "renderD"),
		})
	}
	return nodes
}

func probeNvidiaSMI(ctx context.Context) ([]NVIDIAGPU, bool) {
	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=gpu_name,gpu_uuid,driver_version",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var gpus []NVIDIAGPU
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		parts := splitCSVLine(ln)
		if len(parts) < 3 {
			continue
		}
		gpus = append(gpus, NVIDIAGPU{
			Name:          strings.TrimSpace(parts[0]),
			UUID:          strings.TrimSpace(parts[1]),
			DriverVersion: strings.TrimSpace(parts[2]),
		})
	}
	return gpus, len(gpus) > 0
}

func splitCSVLine(s string) []string {
	var out []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == ',' && !inQuote:
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	out = append(out, b.String())
	return out
}

func probeDockerRuntimes(ctx context.Context) ([]string, bool) {
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{json .}}").Output()
	if err != nil {
		return nil, false
	}
	var info struct {
		Runtimes map[string]interface{} `json:"Runtimes"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, false
	}
	if len(info.Runtimes) == 0 {
		return nil, false
	}
	var names []string
	hasNvidia := false
	for k := range info.Runtimes {
		names = append(names, k)
		if k == "nvidia" {
			hasNvidia = true
		}
	}
	sort.Strings(names)
	return names, hasNvidia
}
