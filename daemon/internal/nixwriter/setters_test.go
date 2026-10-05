package nixwriter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNewSettersValidateAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dplane-state.json")
	w := New(path)
	w.nixOS = true // exercise the NixOS write path on any host

	if err := w.SetZFSArcMax(1 << 30); err != nil {
		t.Fatal(err)
	}
	if err := w.SetZFSArcMax(-1); err == nil {
		t.Error("negative ARC limit accepted")
	}
	if err := w.SetSysctl("vm.swappiness", "10"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][2]string{{"vm swappiness", "1"}, {"vm.swappiness", "1;reboot"}, {"swappiness", "1"}} {
		if err := w.SetSysctl(bad[0], bad[1]); err == nil {
			t.Errorf("SetSysctl(%q, %q) accepted", bad[0], bad[1])
		}
	}
	if err := w.SetTLS("/etc/dplaneos/ssl/a.crt", "/etc/dplaneos/ssl/a.key"); err != nil {
		t.Fatal(err)
	}
	if err := w.SetTLS("relative.crt", "/k"); err == nil {
		t.Error("relative TLS path accepted")
	}
	if err := w.SetTLS("/a.crt\"; x", "/k"); err == nil {
		t.Error("TLS path with quote accepted")
	}
	if err := w.SetUPSPolicy("shutdown", 25, 15); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		a    string
		l, d int
	}{{"reboot", 25, 15}, {"shutdown", 0, 15}, {"shutdown", 25, 601}} {
		if err := w.SetUPSPolicy(bad.a, bad.l, bad.d); err == nil {
			t.Errorf("SetUPSPolicy(%q, %d, %d) accepted", bad.a, bad.l, bad.d)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"zfs_arc_max": float64(1 << 30), "tls_cert": "/etc/dplaneos/ssl/a.crt", "tls_key": "/etc/dplaneos/ssl/a.key",
		"ups_low_battery": float64(25), "ups_final_delay": float64(15), "ups_action": "shutdown",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if sc, _ := got["sysctl"].(map[string]any); sc["vm.swappiness"] != "10" {
		t.Errorf("sysctl = %v", got["sysctl"])
	}

	if err := w.SetSysctl("vm.swappiness", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.state.Sysctl["vm.swappiness"]; ok {
		t.Error("empty value should remove the sysctl key")
	}
}
