//go:build !linux

package hardware

// BuildGPUPassthroughReport returns a minimal report on non-Linux builds (e.g. dev on Windows).
func BuildGPUPassthroughReport() (*GPUPassthroughReport, error) {
	return &GPUPassthroughReport{
		PCIDevices:              nil,
		DRINodes:                nil,
		NVIDIAGPUs:              nil,
		DockerRuntimes:          nil,
		NVIDIARuntimeAvailable:  false,
		NVIDIADriverOK:          false,
		ComposeHints:            ComposeHints{CanUseNVIDIADeviceReservation: false, CanPassDRIDevices: false},
		NixOSDockerNvidiaOption: "services.dplaneos.docker.enableNvidia",
		ComposeExamples:         DefaultComposeExamples(),
	}, nil
}
