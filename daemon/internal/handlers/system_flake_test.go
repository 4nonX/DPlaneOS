package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withSystemFlakePaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldSrc, oldFlake := systemSourcePath, systemFlakeDir
	systemSourcePath = filepath.Join(dir, "source.json")
	systemFlakeDir = filepath.Join(dir, "nixos")
	t.Cleanup(func() { systemSourcePath, systemFlakeDir = oldSrc, oldFlake })
	return dir
}

func TestSystemFlakeText(t *testing.T) {
	text, err := systemFlakeText("github:4nonX/DPlaneOS/v15.2.0", "dplaneos")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		systemFlakeMarker,
		`inputs.dplaneos.url = "github:4nonX/DPlaneOS/v15.2.0";`,
		`dplaneos.nixosConfigurations.dplaneos.extendModules`,
		`./hardware-configuration.nix`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("flake lacks %q:\n%s", want, text)
		}
	}
	for _, bad := range [][2]string{
		{`github:4nonX/DPlaneOS/v1"; evil`, "dplaneos"},
		{"path:/tmp/x", "dplaneos"},
		{"github:4nonX/DPlaneOS/v15.2.0", "dplaneos; x"},
	} {
		if _, err := systemFlakeText(bad[0], bad[1]); err == nil {
			t.Errorf("accepted %q %q", bad[0], bad[1])
		}
	}
}

func TestSystemFlakeRefAndRefusal(t *testing.T) {
	withSystemFlakePaths(t)
	// No flake: a rebuild is refused instead of using the stock configuration.
	if _, err := rebuildSystem("switch"); err != errNoSystemFlake {
		t.Fatalf("rebuild without flake: %v", err)
	}
	os.MkdirAll(systemFlakeDir, 0o755)
	text, _ := systemFlakeText("github:4nonX/DPlaneOS/abc123", "dplaneos")
	os.WriteFile(filepath.Join(systemFlakeDir, "flake.nix"), []byte(text), 0o644)
	ref, err := systemFlakeRef()
	if err != nil || ref != "github:4nonX/DPlaneOS/abc123" {
		t.Fatalf("ref %q %v", ref, err)
	}
	// A live system is never rebuilt.
	os.WriteFile(systemSourcePath, []byte(`{"flake":"github:4nonX/DPlaneOS","rev":null,"version":"15.1.0","configuration":"dplaneos-live","live":true}`), 0o644)
	if _, err := rebuildSystem("switch"); err == nil || !strings.Contains(err.Error(), "live") {
		t.Fatalf("live rebuild: %v", err)
	}
	if exists, ref, custom := systemFlakeState(); !exists || ref != "github:4nonX/DPlaneOS/abc123" || custom {
		t.Fatalf("flake state %v %q %v", exists, ref, custom)
	}
	os.WriteFile(filepath.Join(systemFlakeDir, "flake.nix"), []byte("{ outputs = _: {}; }"), 0o644)
	if exists, _, custom := systemFlakeState(); !exists || !custom {
		t.Fatalf("own flake: %v %v", exists, custom)
	}
}

func TestNormalizeReleaseTag(t *testing.T) {
	for in, want := range map[string]string{"15.2.0": "v15.2.0", "v15.2.0": "v15.2.0", " v1.0.0 ": "v1.0.0"} {
		if got, err := normalizeReleaseTag(in); err != nil || got != want {
			t.Errorf("%q -> %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "latest", "v15.2", "v15.2.0;rm", "main"} {
		if _, err := normalizeReleaseTag(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
