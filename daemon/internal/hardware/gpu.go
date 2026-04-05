// Package hardware probes the host for GPU passthrough support used by Docker Compose.
package hardware

// GPUPassthroughReport is returned by BuildGPUPassthroughReport for API consumers.
type GPUPassthroughReport struct {
	PCIDevices []PCIGPUDevice `json:"pci_devices"`

	// DRINodes lists character devices under /dev/dri (e.g. card0, renderD128).
	DRINodes []DRINode `json:"dri_nodes"`

	// NVIDIAGPUs is populated when nvidia-smi runs successfully.
	NVIDIAGPUs []NVIDIAGPU `json:"nvidia_gpus,omitempty"`

	// DockerRuntimes lists runtime names reported by Docker (e.g. runc, nvidia).
	DockerRuntimes []string `json:"docker_runtimes"`

	// NVIDIARuntimeAvailable is true when Docker exposes a "nvidia" runtime.
	NVIDIARuntimeAvailable bool `json:"nvidia_runtime_available"`

	// NVIDIADriverOK is true when nvidia-smi succeeded (driver + device usable on host).
	NVIDIADriverOK bool `json:"nvidia_driver_ok"`

	// ComposeHints are machine-readable checks for preflight (mirrors composegpu).
	ComposeHints ComposeHints `json:"compose_hints"`

	// NixOSOption is the module toggle for NVIDIA Container Toolkit when using module.nix.
	NixOSDockerNvidiaOption string `json:"nixos_docker_nvidia_option"`

	// ComposeExamples are copy-paste YAML fragments; users merge into their own compose files.
	ComposeExamples ComposeExamples `json:"compose_examples"`
}

// ComposeHints summarises host readiness for common compose patterns.
type ComposeHints struct {
	CanUseNVIDIADeviceReservation bool `json:"can_use_nvidia_device_reservation"`
	CanPassDRIDevices             bool `json:"can_pass_dri_devices"`
}

// ComposeExamples holds reference YAML for NVIDIA CDI-style and DRI bind-mounts.
type ComposeExamples struct {
	NVIDIADeviceReservation string `json:"nvidia_device_reservation"`
	NVIDIADeploySnippet     string `json:"nvidia_deploy_snippet"`
	DRIRenderGroup          string `json:"dri_render_group"`
}

// PCIGPUDevice is a display-class PCI function from lspci.
type PCIGPUDevice struct {
	BusID       string `json:"bus_id"`
	Class       string `json:"class"`        // e.g. VGA, 3D, Display
	ClassCode   string `json:"class_code"`   // e.g. 0300
	VendorName  string `json:"vendor_name"`
	DeviceName  string `json:"device_name"`
	VendorID    string `json:"vendor_id"`    // hex, e.g. 10de
	DeviceID    string `json:"device_id"`    // hex
	RawLine     string `json:"raw_line"`
}

// DRINode is one node under /dev/dri.
type DRINode struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	MajorMinor  string `json:"major_minor,omitempty"`
	IsRender    bool   `json:"is_render"`
}

// NVIDIAGPU is one GPU reported by nvidia-smi.
type NVIDIAGPU struct {
	Name          string `json:"name"`
	UUID          string `json:"uuid"`
	DriverVersion string `json:"driver_version"`
}

// DefaultComposeExamples returns copy-paste YAML fragments for NVIDIA CDI-style reservations and DRI bind-mounts.
func DefaultComposeExamples() ComposeExamples {
	return ComposeExamples{
		NVIDIADeviceReservation: `services:
  app:
    image: your/image:tag
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: 1
              capabilities: [gpu]
`,
		NVIDIADeploySnippet: `    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
`,
		DRIRenderGroup: `services:
  app:
    image: your/image:tag
    devices:
      - /dev/dri:/dev/dri
    group_add:
      - "993"   # render — replace with: getent group render | cut -d: -f3
      - "992"   # video  — replace with: getent group video | cut -d: -f3
`,
	}
}
