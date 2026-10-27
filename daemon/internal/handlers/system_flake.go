package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The system's configuration source.
//
// An installed system is built from the DPlaneOS flake, but its /etc/nixos
// held only what nixos-generate-config wrote: NixOS's stock
// configuration.nix. `nixos-rebuild switch` (Reconcile, the watchdog apply,
// the hardening flow) evaluated that file and replaced the appliance with
// plain NixOS (no daemon, no web UI, no Samba). The system now records where
// it comes from (/etc/dplaneos-source.json, written by the flake), the daemon
// writes /etc/nixos/flake.nix from it, and every rebuild goes through that
// flake or is refused.

var (
	systemSourcePath = "/etc/dplaneos-source.json"
	systemFlakeDir   = "/etc/nixos"
)

// systemFlakeMarker identifies a flake.nix written here (rewritten on update).
const systemFlakeMarker = "# Managed by DPlaneOS"

type systemSource struct {
	Flake         string  `json:"flake"`   // github:4nonX/DPlaneOS
	Rev           *string `json:"rev"`     // git revision of the build, null for an unpublished tree
	Version       string  `json:"version"` // 15.1.0
	Configuration string  `json:"configuration"`
	Live          bool    `json:"live"`
}

func readSystemSource() (*systemSource, error) {
	b, err := os.ReadFile(systemSourcePath)
	if err != nil {
		return nil, err
	}
	var s systemSource
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", systemSourcePath, err)
	}
	return &s, nil
}

var flakeRefRe = regexp.MustCompile(`^github:[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
var configNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// systemFlakeText renders /etc/nixos/flake.nix: the DPlaneOS release at ref
// plus this machine's hardware configuration and optional local.nix.
func systemFlakeText(ref, configuration string) (string, error) {
	if !flakeRefRe.MatchString(ref) || !configNameRe.MatchString(configuration) {
		return "", fmt.Errorf("invalid flake reference %q or configuration %q", ref, configuration)
	}
	return systemFlakeMarker + `: this machine is built from the DPlaneOS release
# below plus its hardware configuration (and local.nix, if present). Updates
# change the reference; settings made in the web UI come from
# /var/lib/dplaneos/dplane-state.json (rebuild with --impure).
{
  inputs.dplaneos.url = "` + ref + `";

  outputs = { dplaneos, ... }: {
    nixosConfigurations.dplaneos = dplaneos.nixosConfigurations.` + configuration + `.extendModules {
      modules = [ ./hardware-configuration.nix ]
        ++ (if builtins.pathExists ./local.nix then [ ./local.nix ] else [ ]);
    };
  };
}
`, nil
}

// systemFlakeRef returns the flake reference /etc/nixos/flake.nix uses.
func systemFlakeRef() (string, error) {
	b, err := os.ReadFile(filepath.Join(systemFlakeDir, "flake.nix"))
	if err != nil {
		return "", err
	}
	m := regexp.MustCompile(`inputs\.dplaneos\.url = "([^"]+)"`).FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("no DPlaneOS input in %s/flake.nix", systemFlakeDir)
	}
	return string(m[1]), nil
}

// EnsureSystemFlake writes /etc/nixos/flake.nix when it is missing (installed
// systems; a live system is never rebuilt). Runs at daemon start, so
// existing installs get it too.
func EnsureSystemFlake() {
	if !IsNixOS() {
		return
	}
	src, err := readSystemSource()
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("SYSTEM FLAKE: %v", err)
		}
		return
	}
	if src.Live {
		return
	}
	path := filepath.Join(systemFlakeDir, "flake.nix")
	if _, err := os.Stat(path); err == nil {
		return // ours or the administrator's: kept
	}
	if src.Rev == nil || *src.Rev == "" {
		log.Printf("SYSTEM FLAKE: this system was built from an unpublished source tree; write %s yourself to allow rebuilds", path)
		return
	}
	text, err := systemFlakeText(src.Flake+"/"+*src.Rev, src.Configuration)
	if err != nil {
		log.Printf("SYSTEM FLAKE: %v", err)
		return
	}
	if err := os.MkdirAll(systemFlakeDir, 0o755); err != nil {
		log.Printf("SYSTEM FLAKE: %v", err)
		return
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		log.Printf("SYSTEM FLAKE: %v", err)
		return
	}
	log.Printf("SYSTEM FLAKE: wrote %s (DPlaneOS %s, %s)", path, src.Version, *src.Rev)
}

var errNoSystemFlake = errors.New("this system has no " + "/etc/nixos/flake.nix" +
	": a rebuild would use NixOS's stock configuration and remove DPlaneOS. It is written when the daemon starts on an installed system built from a published release")

// rebuildSystem runs nixos-rebuild through the system flake. --impure: the
// web UI's settings are read from /var/lib/dplaneos/dplane-state.json, which
// a pure evaluation silently ignores.
func rebuildSystem(action string, extra ...string) ([]byte, error) {
	if src, err := readSystemSource(); err == nil && src.Live {
		return nil, errors.New("a live system is not rebuilt; install DPlaneOS to change its system configuration")
	}
	if _, err := os.Stat(filepath.Join(systemFlakeDir, "flake.nix")); err != nil {
		return nil, errNoSystemFlake
	}
	args := append([]string{action, "--flake", systemFlakeDir + "#dplaneos", "--impure"}, extra...)
	return runNixosRebuild(args...)
}

// systemFlakeState: whether /etc/nixos/flake.nix exists, its DPlaneOS
// reference, and whether it is someone else's (no DPlaneOS input).
func systemFlakeState() (exists bool, ref string, custom bool) {
	if r, err := systemFlakeRef(); err == nil {
		return true, r, false
	}
	if _, err := os.Stat(filepath.Join(systemFlakeDir, "flake.nix")); err == nil {
		return true, "", true
	}
	return false, "", false
}

// isSemverTag: v15.1.0
var releaseTagRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

func normalizeReleaseTag(v string) (string, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !releaseTagRe.MatchString(v) {
		return "", fmt.Errorf("invalid release %q (e.g. v16.0.2)", v)
	}
	return v, nil
}
