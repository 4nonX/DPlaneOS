// Package composegpu detects GPU-related intent in Docker Compose YAML and validates it against host capabilities.
package composegpu

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"

	"dplaned/internal/hardware"
)

// Requirements describes GPU-related patterns found in compose text (best-effort, no full YAML parse).
type Requirements struct {
	WantsNVIDIA bool // NVIDIA reservations, runtime, CDI IDs, or NVIDIA_* env
	WantsDRI    bool // /dev/dri or dri path in devices/volumes
}

var (
	reDriverNvidia    = regexp.MustCompile(`(?m)driver\s*:\s*nvidia\b`)
	reNvidiaComGPU    = regexp.MustCompile(`(?i)nvidia\.com/gpu`)
	reRuntimeNvidia   = regexp.MustCompile(`(?m)runtime\s*:\s*nvidia\b`)
	reNVIDIAEnv       = regexp.MustCompile(`(?m)NVIDIA_VISIBLE_DEVICES\s*:`)
	reNVIDIADriverCap = regexp.MustCompile(`(?m)NVIDIA_DRIVER_CAPABILITIES\s*:`)
	reGPUsAll         = regexp.MustCompile(`(?m)gpus\s*:\s*['"]?all['"]?`)
	reDevDRI          = regexp.MustCompile(`/dev/dri`)
	reDriPath         = regexp.MustCompile(`(?i)dri/card|dri/render`)
)

// Analyze returns GPU-related requirements inferred from compose YAML or similar text.
func Analyze(yaml string) Requirements {
	s := yaml
	var r Requirements
	if reDriverNvidia.MatchString(s) || reNvidiaComGPU.MatchString(s) || reRuntimeNvidia.MatchString(s) ||
		reNVIDIAEnv.MatchString(s) || reNVIDIADriverCap.MatchString(s) || reGPUsAll.MatchString(s) {
		r.WantsNVIDIA = true
	}
	// CDI device id style without nvidia.com prefix (rare)
	if strings.Contains(strings.ToLower(s), "cdi/") && strings.Contains(strings.ToLower(s), "nvidia") {
		r.WantsNVIDIA = true
	}
	if reDevDRI.MatchString(s) || reDriPath.MatchString(s) {
		r.WantsDRI = true
	}
	return r
}

// ValidateForDeploy builds a host GPU report and returns an error if compose requirements cannot be met.
// On non-Linux, validation is skipped (development hosts).
func ValidateForDeploy(composeYAML string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	rep, err := hardware.BuildGPUPassthroughReport()
	if err != nil {
		return fmt.Errorf("gpu host probe: %w", err)
	}
	return ValidateAgainstReport(composeYAML, rep)
}

// ValidateAgainstReport checks compose text against an existing host report.
func ValidateAgainstReport(composeYAML string, rep *hardware.GPUPassthroughReport) error {
	if rep == nil {
		return nil
	}
	req := Analyze(composeYAML)
	var msgs []string
	if req.WantsNVIDIA {
		if !rep.NVIDIADriverOK {
			msgs = append(msgs, "Compose references NVIDIA GPUs (driver/reservations/runtime or NVIDIA_* env) but nvidia-smi did not report any GPU on this host. Install and load the proprietary NVIDIA driver, then verify with nvidia-smi.")
		}
		if !rep.NVIDIARuntimeAvailable {
			opt := rep.NixOSDockerNvidiaOption
			if opt == "" {
				opt = "services.dplaneos.docker.enableNvidia"
			}
			msgs = append(msgs, fmt.Sprintf("Compose references NVIDIA GPUs but Docker does not expose the nvidia runtime. On D-PlaneOS NixOS set %s = true and run nixos-rebuild switch (NVIDIA Container Toolkit). You must still configure host drivers separately.", opt))
		}
	}
	if req.WantsDRI {
		if len(rep.DRINodes) == 0 {
			msgs = append(msgs, "Compose references /dev/dri (or DRI devices) but this host has no character devices under /dev/dri. Load a kernel GPU driver (e.g. i915, amdgpu) or adjust the compose file.")
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("GPU compose preflight failed: %s", strings.Join(msgs, " "))
}
