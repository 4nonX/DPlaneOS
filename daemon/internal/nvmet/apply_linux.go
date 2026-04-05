//go:build linux

package nvmet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Apply wipes D-PlaneOS-managed nvmet objects and applies exports (empty slice clears target config).
func Apply(exports []Export) error {
	if err := modprobe(); err != nil {
		return err
	}
	root := nvmetRoot()
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return fmt.Errorf("nvmet configfs not mounted at %s (is configfs mounted?)", root)
	}
	if err := teardownDPlane(); err != nil {
		return fmt.Errorf("nvmet teardown: %w", err)
	}
	for i := range exports {
		if err := ValidateExport(&exports[i]); err != nil {
			return fmt.Errorf("export %q: %w", exports[i].SubsystemNQN, err)
		}
	}
	for _, e := range exports {
		if err := createExport(&e); err != nil {
			return err
		}
	}
	return nil
}

func modprobe() error {
	for _, m := range []string{"nvmet", "nvmet-tcp"} {
		out, err := exec.Command("modprobe", m).CombinedOutput()
		if err != nil {
			return fmt.Errorf("modprobe %s: %w\n%s", m, err, out)
		}
	}
	return nil
}

func teardownDPlane() error {
	root := nvmetRoot()
	portsDir := filepath.Join(root, "ports")
	entries, err := os.ReadDir(portsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "dplane-p-") {
			continue
		}
		p := filepath.Join(portsDir, e.Name())
		subs, _ := os.ReadDir(filepath.Join(p, "subsystems"))
		for _, s := range subs {
			_ = os.Remove(filepath.Join(p, "subsystems", s.Name()))
		}
		_ = os.RemoveAll(p)
	}

	subDir := filepath.Join(root, "subsystems")
	subs, err := os.ReadDir(subDir)
	if err != nil {
		return err
	}
	for _, e := range subs {
		if !strings.HasPrefix(e.Name(), "dplane-ss-") {
			continue
		}
		if err := removeSubsystem(filepath.Join(subDir, e.Name())); err != nil {
			return err
		}
	}

	hostDir := filepath.Join(root, "hosts")
	hents, _ := os.ReadDir(hostDir)
	for _, e := range hents {
		if strings.HasPrefix(e.Name(), "dplane-h-") {
			_ = os.RemoveAll(filepath.Join(hostDir, e.Name()))
		}
	}
	return nil
}

func removeSubsystem(path string) error {
	ah := filepath.Join(path, "allowed_hosts")
	if ents, err := os.ReadDir(ah); err == nil {
		for _, e := range ents {
			_ = os.Remove(filepath.Join(ah, e.Name()))
		}
	}
	ns := filepath.Join(path, "namespaces")
	if ents, err := os.ReadDir(ns); err == nil {
		for _, e := range ents {
			nspath := filepath.Join(ns, e.Name())
			_ = os.WriteFile(filepath.Join(nspath, "enable"), []byte("0"), 0644)
			_ = os.RemoveAll(nspath)
		}
	}
	return os.RemoveAll(path)
}

func createExport(e *Export) error {
	root := nvmetRoot()
	ssName := subsysDirName(e.SubsystemNQN)
	ssPath := filepath.Join(root, "subsystems", ssName)
	if err := os.Mkdir(ssPath, 0755); err != nil {
		return fmt.Errorf("mkdir subsystem %s: %w", ssName, err)
	}
	if err := os.WriteFile(filepath.Join(ssPath, "subsys_nqn"), []byte(e.SubsystemNQN), 0644); err != nil {
		return fmt.Errorf("subsys_nqn: %w", err)
	}
	if e.AllowAnyHost {
		if err := os.WriteFile(filepath.Join(ssPath, "attr_allow_any_host"), []byte("1"), 0644); err != nil {
			return fmt.Errorf("attr_allow_any_host: %w", err)
		}
	} else {
		if err := os.WriteFile(filepath.Join(ssPath, "attr_allow_any_host"), []byte("0"), 0644); err != nil {
			return fmt.Errorf("attr_allow_any_host: %w", err)
		}
		for _, hn := range e.HostNQNs {
			hn = strings.TrimSpace(hn)
			hname := hostDirName(hn)
			hpath := filepath.Join(root, "hosts", hname)
			if err := os.Mkdir(hpath, 0755); err != nil && !os.IsExist(err) {
				return fmt.Errorf("mkdir host %s: %w", hname, err)
			}
			if err := writeHostNQN(hpath, hn); err != nil {
				return err
			}
			link := filepath.Join(ssPath, "allowed_hosts", hname)
			_ = os.Remove(link)
			target := filepath.Join("..", "..", "..", "hosts", hname)
			if err := os.Symlink(target, link); err != nil {
				return fmt.Errorf("allowed_hosts link %s: %w", hname, err)
			}
		}
	}

	nsID := fmt.Sprintf("%d", e.NamespaceID)
	nsPath := filepath.Join(ssPath, "namespaces", nsID)
	if err := os.Mkdir(nsPath, 0755); err != nil {
		return fmt.Errorf("mkdir namespace: %w", err)
	}
	dev := ZvolDevicePath(e.Zvol)
	if err := os.WriteFile(filepath.Join(nsPath, "device_path"), []byte(dev), 0644); err != nil {
		return fmt.Errorf("device_path: %w", err)
	}
	if err := os.WriteFile(filepath.Join(nsPath, "enable"), []byte("1"), 0644); err != nil {
		return fmt.Errorf("namespace enable: %w", err)
	}

	pName := portDirName(e.Transport, e.ListenAddr, e.ListenPort)
	pPath := filepath.Join(root, "ports", pName)
	if _, err := os.Stat(pPath); os.IsNotExist(err) {
		if err := os.Mkdir(pPath, 0755); err != nil {
			return fmt.Errorf("mkdir port: %w", err)
		}
		if err := os.WriteFile(filepath.Join(pPath, "addr_trtype"), []byte(e.Transport), 0644); err != nil {
			return fmt.Errorf("addr_trtype: %w", err)
		}
		if err := os.WriteFile(filepath.Join(pPath, "addr_adrfam"), []byte("ipv4"), 0644); err != nil {
			return fmt.Errorf("addr_adrfam: %w", err)
		}
		if err := os.WriteFile(filepath.Join(pPath, "addr_traddr"), []byte(e.ListenAddr), 0644); err != nil {
			return fmt.Errorf("addr_traddr: %w", err)
		}
		svc := fmt.Sprintf("%d", e.ListenPort)
		if err := os.WriteFile(filepath.Join(pPath, "addr_trsvcid"), []byte(svc), 0644); err != nil {
			return fmt.Errorf("addr_trsvcid: %w", err)
		}
	}
	link := filepath.Join(pPath, "subsystems", ssName)
	_ = os.Remove(link)
	target := filepath.Join("..", "..", "..", "subsystems", ssName)
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("port subsystem link: %w", err)
	}

	return nil
}

func writeHostNQN(hpath, nqn string) error {
	for _, fname := range []string{"hostnqn", "host_nqn"} {
		p := filepath.Join(hpath, fname)
		if err := os.WriteFile(p, []byte(nqn), 0644); err == nil {
			return nil
		}
	}
	return fmt.Errorf("could not write host NQN in %s", hpath)
}
