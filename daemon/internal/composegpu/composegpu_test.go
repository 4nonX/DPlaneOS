package composegpu

import (
	"testing"

	"dplaned/internal/hardware"
)

func TestAnalyze_NVIDIA(t *testing.T) {
	y := `
services:
  x:
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: 1
`
	r := Analyze(y)
	if !r.WantsNVIDIA {
		t.Fatal("expected WantsNVIDIA")
	}
	if r.WantsDRI {
		t.Fatal("unexpected WantsDRI")
	}
}

func TestAnalyze_DRI(t *testing.T) {
	y := `services:
  j:
    devices:
      - /dev/dri:/dev/dri
`
	r := Analyze(y)
	if !r.WantsDRI {
		t.Fatal("expected WantsDRI")
	}
}

func TestValidateAgainstReport_NVIDIAFail(t *testing.T) {
	y := `services:
  a:
    runtime: nvidia
`
	rep := &hardware.GPUPassthroughReport{
		NVIDIADriverOK:         false,
		NVIDIARuntimeAvailable: false,
		NixOSDockerNvidiaOption: "services.dplaneos.docker.enableNvidia",
	}
	if err := ValidateAgainstReport(y, rep); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateAgainstReport_OK(t *testing.T) {
	y := `services:
  a:
    image: nginx
`
	rep := &hardware.GPUPassthroughReport{}
	if err := ValidateAgainstReport(y, rep); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAgainstReport_DRI(t *testing.T) {
	y := `devices: ["/dev/dri:/dev/dri"]`
	rep := &hardware.GPUPassthroughReport{}
	if err := ValidateAgainstReport(y, rep); err == nil {
		t.Fatal("expected error when no DRI nodes")
	}
	rep2 := &hardware.GPUPassthroughReport{
		DRINodes: []hardware.DRINode{{Name: "card0", Path: "/dev/dri/card0"}},
	}
	if err := ValidateAgainstReport(y, rep2); err != nil {
		t.Fatal(err)
	}
}
