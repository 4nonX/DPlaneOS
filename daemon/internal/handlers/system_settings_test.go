package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemTuning(t *testing.T) {
	dir := t.TempDir()
	prevCfg, prevArc, prevSwap, prevMod, prevSysctl := ConfigDir, arcMaxParamPath, swappinessPath, modprobeZFSPath, sysctlDropInPath
	ConfigDir = dir
	arcMaxParamPath = filepath.Join(dir, "zfs_arc_max")
	swappinessPath = filepath.Join(dir, "swappiness")
	modprobeZFSPath = filepath.Join(dir, "zfs.conf")
	sysctlDropInPath = filepath.Join(dir, "sysctl.conf")
	t.Cleanup(func() {
		ConfigDir, arcMaxParamPath, swappinessPath, modprobeZFSPath, sysctlDropInPath = prevCfg, prevArc, prevSwap, prevMod, prevSysctl
	})
	_ = os.WriteFile(arcMaxParamPath, []byte("0\n"), 0644)
	_ = os.WriteFile(swappinessPath, []byte("60\n"), 0644)

	// Nothing saved: what the kernel uses, not invented defaults.
	r := call(t, HandleSystemSettings, req{})
	if !r.ok() || r.body["arc_limit_gb"].(float64) != 0 || r.body["swappiness"].(float64) != 60 {
		t.Fatalf("defaults: %s", r)
	}
	for _, bad := range []map[string]any{{"arc_limit_gb": -1, "swappiness": 10}, {"arc_limit_gb": 4, "swappiness": 101}} {
		if r := call(t, HandleSystemSettings, req{method: "POST", body: bad}); r.code != 400 {
			t.Errorf("%v: %s", bad, r)
		}
	}
	if onNixOS() {
		t.Skip("persistence goes through the NixOS writer here")
	}
	if r := call(t, HandleSystemSettings, req{method: "POST", body: map[string]any{"arc_limit_gb": 4, "swappiness": 10}}); !r.ok() {
		t.Fatalf("save: %s", r)
	}
	if b, _ := os.ReadFile(arcMaxParamPath); strings.TrimSpace(string(b)) != "4294967296" {
		t.Errorf("zfs_arc_max = %q", b)
	}
	if b, _ := os.ReadFile(modprobeZFSPath); !strings.Contains(string(b), "zfs_arc_max=4294967296") {
		t.Errorf("modprobe file: %q", b)
	}
	if b, _ := os.ReadFile(sysctlDropInPath); !strings.Contains(string(b), "vm.swappiness=10") {
		t.Errorf("sysctl file: %q", b)
	}
	r = call(t, HandleSystemSettings, req{})
	if r.body["arc_limit_gb"].(float64) != 4 || r.body["live"].(map[string]any)["swappiness"].(float64) != 10 {
		t.Errorf("after save: %s", r)
	}
}
