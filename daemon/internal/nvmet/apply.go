package nvmet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The kernel's NVMe target is configured through configfs:
//
//	nvmet/subsystems/<NQN>/                  the directory's name is the NQN
//	    attr_allow_any_host, attr_serial
//	    namespaces/<nsid>/device_path, device_uuid, device_nguid, enable
//	    allowed_hosts/<host NQN> -> ../../../hosts/<host NQN>
//	nvmet/hosts/<host NQN>/                  the directory's name is the NQN
//	nvmet/ports/<number>/                    the directory's name is a number
//	    addr_trtype, addr_adrfam, addr_traddr, addr_trsvcid
//	    subsystems/<NQN> -> ../../../subsystems/<NQN>
//
// A directory is made with mkdir and removed with rmdir; the kernel fills it
// with its attribute files. A port starts listening when the first subsystem
// is linked into it. On an installed system nothing else configures the
// NVMe target: Apply owns all of it.

// Replaced in tests, where a temporary directory stands in for configfs.
var (
	mkdir = func(p string) error { return os.Mkdir(p, 0o755) }
	rmdir = os.Remove
	// link makes the symbolic link by which configfs joins two objects.
	link = os.Symlink
	// deviceOK reports whether an export's volume is there.
	deviceOK = func(dev string) error { return deviceThere(dev) }
)

func dirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func readAttr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeAttr(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(value)
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("writing %q to %s: %w", value, path, werr)
	}
	return cerr
}

func reconcile(root string, exports []Export) error {
	subsDir := filepath.Join(root, "subsystems")
	want := map[string]Export{}
	for _, e := range exports {
		want[e.SubsystemNQN] = e
	}
	var errs []error

	// 1. What is served but not wanted (any more, or not like this) goes.
	for _, nqn := range dirNames(subsDir) {
		e, wanted := want[nqn]
		if wanted && (namespaceAsWanted(root, e) || deviceOK(ZvolDevicePath(e.Zvol)) != nil) {
			// As wanted; or its volume cannot be seen right now, and then a
			// subsystem that is being served is not taken down on a guess.
			continue
		}
		if err := removeSubsystem(root, nqn); err != nil {
			errs = append(errs, fmt.Errorf("removing %s: %w", nqn, err))
		}
	}

	// 2. What is wanted is there, with its hosts and its port.
	served := map[string]bool{}
	for _, e := range exports {
		existed := isDir(filepath.Join(subsDir, e.SubsystemNQN))
		if !existed {
			if err := deviceOK(ZvolDevicePath(e.Zvol)); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", e.SubsystemNQN, err))
				continue
			}
		}
		if err := ensureSubsystem(root, e); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.SubsystemNQN, err))
			if !existed {
				// A half-made subsystem is not left behind.
				_ = removeSubsystem(root, e.SubsystemNQN)
				continue
			}
		}
		served[e.SubsystemNQN] = true
	}

	// 3. Ports nothing is served on, and hosts nothing refers to.
	for _, p := range dirNames(filepath.Join(root, "ports")) {
		pdir := filepath.Join(root, "ports", p)
		if len(dirNames(filepath.Join(pdir, "subsystems"))) == 0 {
			if err := rmdir(pdir); err != nil {
				errs = append(errs, fmt.Errorf("removing port %s: %w", p, err))
			}
		}
	}
	used := map[string]bool{}
	for _, nqn := range dirNames(subsDir) {
		for _, h := range dirNames(filepath.Join(subsDir, nqn, "allowed_hosts")) {
			used[h] = true
		}
	}
	for _, h := range dirNames(filepath.Join(root, "hosts")) {
		if !used[h] {
			if err := rmdir(filepath.Join(root, "hosts", h)); err != nil {
				errs = append(errs, fmt.Errorf("removing host %s: %w", h, err))
			}
		}
	}
	return errors.Join(errs...)
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// namespaceAsWanted reports whether the subsystem serves exactly the
// export's volume, as its one namespace, with the export's identity.
func namespaceAsWanted(root string, e Export) bool {
	ss := filepath.Join(root, "subsystems", e.SubsystemNQN)
	nsid := strconv.Itoa(e.NamespaceID)
	if ns := dirNames(filepath.Join(ss, "namespaces")); len(ns) != 1 || ns[0] != nsid {
		return false
	}
	nsDir := filepath.Join(ss, "namespaces", nsid)
	id := IdentityOf(e.SubsystemNQN, e.NamespaceID)
	return readAttr(filepath.Join(nsDir, "device_path")) == ZvolDevicePath(e.Zvol) &&
		readAttr(filepath.Join(nsDir, "enable")) == "1" &&
		strings.EqualFold(readAttr(filepath.Join(nsDir, "device_uuid")), id.UUID)
}

// removeSubsystem takes a subsystem out of its ports (which disconnects its
// initiators), then removes its host links, its namespaces and itself.
func removeSubsystem(root, nqn string) error {
	ss := filepath.Join(root, "subsystems", nqn)
	for _, p := range dirNames(filepath.Join(root, "ports")) {
		l := filepath.Join(root, "ports", p, "subsystems", nqn)
		if _, err := os.Lstat(l); err == nil {
			if err := os.Remove(l); err != nil {
				return err
			}
		}
	}
	for _, h := range dirNames(filepath.Join(ss, "allowed_hosts")) {
		if err := os.Remove(filepath.Join(ss, "allowed_hosts", h)); err != nil {
			return err
		}
	}
	for _, ns := range dirNames(filepath.Join(ss, "namespaces")) {
		if err := rmdir(filepath.Join(ss, "namespaces", ns)); err != nil {
			return err
		}
	}
	return rmdir(ss)
}

func ensureSubsystem(root string, e Export) error {
	ss := filepath.Join(root, "subsystems", e.SubsystemNQN)
	id := IdentityOf(e.SubsystemNQN, e.NamespaceID)
	if !isDir(ss) {
		if err := mkdir(ss); err != nil {
			return fmt.Errorf("creating the subsystem: %w", err)
		}
		// Only possible before the first initiator has seen the subsystem.
		if err := writeAttr(filepath.Join(ss, "attr_serial"), id.Serial); err != nil {
			return fmt.Errorf("serial number: %w", err)
		}
	}

	// Who may connect. The kernel refuses "any host" while hosts are listed,
	// and listing hosts while "any host" is set.
	hostsDir := filepath.Join(ss, "allowed_hosts")
	wantHosts := map[string]bool{}
	if !e.AllowAnyHost {
		for _, h := range e.HostNQNs {
			wantHosts[strings.TrimSpace(h)] = true
		}
	}
	for _, h := range dirNames(hostsDir) {
		if !wantHosts[h] {
			if err := os.Remove(filepath.Join(hostsDir, h)); err != nil {
				return fmt.Errorf("removing host %s: %w", h, err)
			}
		}
	}
	any := "0"
	if e.AllowAnyHost {
		any = "1"
	}
	if readAttr(filepath.Join(ss, "attr_allow_any_host")) != any {
		if err := writeAttr(filepath.Join(ss, "attr_allow_any_host"), any); err != nil {
			return fmt.Errorf("allow_any_host: %w", err)
		}
	}
	hosts := make([]string, 0, len(wantHosts))
	for h := range wantHosts {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		hdir := filepath.Join(root, "hosts", h)
		if !isDir(hdir) {
			if err := mkdir(hdir); err != nil {
				return fmt.Errorf("host %s: %w", h, err)
			}
		}
		l := filepath.Join(hostsDir, h)
		if _, err := os.Lstat(l); err != nil {
			if err := link(hdir, l); err != nil {
				return fmt.Errorf("allowing host %s: %w", h, err)
			}
		}
	}

	// The namespace: the volume, under an identity that does not change.
	nsDir := filepath.Join(ss, "namespaces", strconv.Itoa(e.NamespaceID))
	if !isDir(nsDir) {
		if err := mkdir(nsDir); err != nil {
			return fmt.Errorf("creating the namespace: %w", err)
		}
		for _, a := range [][2]string{
			{"device_path", ZvolDevicePath(e.Zvol)},
			{"device_uuid", id.UUID},
			{"device_nguid", id.NGUID},
			{"enable", "1"},
		} {
			if err := writeAttr(filepath.Join(nsDir, a[0]), a[1]); err != nil {
				return fmt.Errorf("namespace: %w", err)
			}
		}
	}

	// The port it is served on, and no other.
	port, err := ensurePort(root, e)
	if err != nil {
		return err
	}
	for _, p := range dirNames(filepath.Join(root, "ports")) {
		l := filepath.Join(root, "ports", p, "subsystems", e.SubsystemNQN)
		_, lerr := os.Lstat(l)
		switch {
		case p == port && lerr != nil:
			if err := link(ss, l); err != nil {
				return fmt.Errorf("serving on %s port %d: %w", e.ListenAddr, e.ListenPort, err)
			}
		case p != port && lerr == nil:
			if err := os.Remove(l); err != nil {
				return fmt.Errorf("leaving port %s: %w", p, err)
			}
		}
	}
	return nil
}

// ensurePort returns the port (its directory name) for the export's
// address, creating it when there is none.
func ensurePort(root string, e Export) (string, error) {
	portsDir := filepath.Join(root, "ports")
	svc := strconv.Itoa(e.ListenPort)
	usedIDs := map[int]bool{}
	for _, p := range dirNames(portsDir) {
		if n, err := strconv.Atoi(p); err == nil {
			usedIDs[n] = true
		}
		pdir := filepath.Join(portsDir, p)
		if readAttr(filepath.Join(pdir, "addr_trtype")) == e.Transport &&
			readAttr(filepath.Join(pdir, "addr_traddr")) == e.ListenAddr &&
			readAttr(filepath.Join(pdir, "addr_trsvcid")) == svc {
			return p, nil
		}
	}
	id := 1
	for usedIDs[id] {
		id++
	}
	name := strconv.Itoa(id)
	pdir := filepath.Join(portsDir, name)
	if err := mkdir(pdir); err != nil {
		return "", fmt.Errorf("creating a port: %w", err)
	}
	family := "ipv4"
	if strings.Contains(e.ListenAddr, ":") {
		family = "ipv6"
	}
	for _, a := range [][2]string{
		{"addr_trtype", e.Transport},
		{"addr_adrfam", family},
		{"addr_traddr", e.ListenAddr},
		{"addr_trsvcid", svc},
	} {
		if err := writeAttr(filepath.Join(pdir, a[0]), a[1]); err != nil {
			_ = rmdir(pdir)
			return "", fmt.Errorf("port: %w", err)
		}
	}
	return name, nil
}
