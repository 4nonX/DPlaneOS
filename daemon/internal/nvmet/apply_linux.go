//go:build linux

package nvmet

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The kernel's nvmet configfs (Documentation/nvme/nvmet... and nvmetcli):
//
//	subsystems/<subsystem NQN>/attr_allow_any_host
//	subsystems/<NQN>/namespaces/<nsid>/{device_path,ana_grpid,enable}
//	subsystems/<NQN>/allowed_hosts/<host NQN> -> ../../../hosts/<host NQN>
//	hosts/<host NQN>
//	ports/<numeric port id>/{addr_trtype,addr_adrfam,addr_traddr,addr_trsvcid}
//	ports/<id>/subsystems/<NQN> -> ../../../subsystems/<NQN>
//	ports/<id>/ana_groups/<group id>/ana_state
//
// A subsystem's and a host's NQN is the directory name and a port is a
// number. The former code made directories named dplane-ss-<hash> and wrote
// a "subsys_nqn" file (read-only: every export failed), named ports
// dplane-p-<hash> (not a number), wrote ANA groups under the subsystem and
// always set the address family to IPv4. Which objects DPlaneOS made is now
// recorded in AppliedFile (/run: cleared with configfs at boot).

// AppliedFile records the subsystems, hosts and ports DPlaneOS created.
var AppliedFile = "/run/dplaneos/nvmet-applied.json"

type applied struct {
	Subsystems []string `json:"subsystems"`
	Hosts      []string `json:"hosts"`
	Ports      []int    `json:"ports"`
}

func readApplied() applied {
	var a applied
	if b, err := os.ReadFile(AppliedFile); err == nil {
		json.Unmarshal(b, &a) //nolint:errcheck
	}
	return a
}

func writeApplied(a applied) error {
	if err := os.MkdirAll(filepath.Dir(AppliedFile), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(a)
	return os.WriteFile(AppliedFile, b, 0o644)
}

func nvmetRoot() string {
	return filepath.Join(ConfigfsRoot, "nvmet")
}

// Apply removes the DPlaneOS-made nvmet objects and creates the exports
// (an empty slice clears them). Objects made by hand are left alone.
func Apply(exports []Export) error {
	for i := range exports {
		if err := ValidateExport(&exports[i]); err != nil {
			return fmt.Errorf("export %q: %w", exports[i].SubsystemNQN, err)
		}
	}
	if err := modprobe(); err != nil {
		return err
	}
	root := nvmetRoot()
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return fmt.Errorf("nvmet configfs not mounted at %s (is configfs mounted?)", root)
	}
	if err := teardown(readApplied()); err != nil {
		return fmt.Errorf("nvmet teardown: %w", err)
	}
	var a applied
	defer func() { writeApplied(a) }() //nolint:errcheck
	ports := map[string]int{}          // transport|addr|port -> id
	for i := range exports {
		if err := createExport(&exports[i], &a, ports); err != nil {
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

func rmdirTree(path string) {
	// configfs: entries are removed with rmdir (os.RemoveAll would try to
	// unlink the attribute files).
	os.Remove(path) //nolint:errcheck
}

func teardown(a applied) error {
	root := nvmetRoot()
	for _, id := range a.Ports {
		p := filepath.Join(root, "ports", strconv.Itoa(id))
		links, _ := os.ReadDir(filepath.Join(p, "subsystems"))
		for _, l := range links {
			os.Remove(filepath.Join(p, "subsystems", l.Name())) //nolint:errcheck
		}
		groups, _ := os.ReadDir(filepath.Join(p, "ana_groups"))
		for _, g := range groups {
			if g.Name() != "1" { // group 1 belongs to the port
				rmdirTree(filepath.Join(p, "ana_groups", g.Name()))
			}
		}
		rmdirTree(p)
	}
	for _, nqn := range a.Subsystems {
		ss := filepath.Join(root, "subsystems", nqn)
		if hosts, err := os.ReadDir(filepath.Join(ss, "allowed_hosts")); err == nil {
			for _, h := range hosts {
				os.Remove(filepath.Join(ss, "allowed_hosts", h.Name())) //nolint:errcheck
			}
		}
		if nss, err := os.ReadDir(filepath.Join(ss, "namespaces")); err == nil {
			for _, n := range nss {
				np := filepath.Join(ss, "namespaces", n.Name())
				os.WriteFile(filepath.Join(np, "enable"), []byte("0"), 0o644) //nolint:errcheck
				rmdirTree(np)
			}
		}
		rmdirTree(ss)
		if _, err := os.Stat(ss); err == nil {
			return fmt.Errorf("subsystem %s could not be removed (still exported by a port made by hand?)", nqn)
		}
	}
	for _, h := range a.Hosts {
		rmdirTree(filepath.Join(root, "hosts", h)) // fails while a hand-made subsystem still allows it: kept
	}
	return nil
}

// portID returns the port for transport/addr/port, creating it when needed.
func portID(e *Export, a *applied, ports map[string]int) (int, error) {
	key := fmt.Sprintf("%s|%s|%d", e.Transport, e.ListenAddr, e.ListenPort)
	if id, ok := ports[key]; ok {
		return id, nil
	}
	root := nvmetRoot()
	used := map[int]bool{}
	if ents, err := os.ReadDir(filepath.Join(root, "ports")); err == nil {
		for _, en := range ents {
			if id, err := strconv.Atoi(en.Name()); err == nil {
				used[id] = true
			}
		}
	}
	id := 1
	for used[id] {
		id++
	}
	p := filepath.Join(root, "ports", strconv.Itoa(id))
	if err := os.Mkdir(p, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir port %d: %w", id, err)
	}
	a.Ports = append(a.Ports, id)
	family := "ipv4"
	if ip := net.ParseIP(e.ListenAddr); ip != nil && ip.To4() == nil {
		family = "ipv6"
	}
	for _, kv := range [][2]string{
		{"addr_trtype", e.Transport},
		{"addr_adrfam", family},
		{"addr_traddr", e.ListenAddr},
		{"addr_trsvcid", strconv.Itoa(e.ListenPort)},
	} {
		if err := os.WriteFile(filepath.Join(p, kv[0]), []byte(kv[1]), 0o644); err != nil {
			return 0, fmt.Errorf("port %d %s: %w", id, kv[0], err)
		}
	}
	ports[key] = id
	return id, nil
}

func createExport(e *Export, a *applied, ports map[string]int) error {
	root := nvmetRoot()
	ssPath := filepath.Join(root, "subsystems", e.SubsystemNQN)
	if err := os.Mkdir(ssPath, 0o755); err != nil {
		return fmt.Errorf("mkdir subsystem %s: %w", e.SubsystemNQN, err)
	}
	a.Subsystems = append(a.Subsystems, e.SubsystemNQN)
	allowAny := "0"
	if e.AllowAnyHost {
		allowAny = "1"
	}
	if err := os.WriteFile(filepath.Join(ssPath, "attr_allow_any_host"), []byte(allowAny), 0o644); err != nil {
		return fmt.Errorf("attr_allow_any_host: %w", err)
	}
	if !e.AllowAnyHost {
		for _, hn := range e.HostNQNs {
			hn = strings.TrimSpace(hn)
			hpath := filepath.Join(root, "hosts", hn)
			if err := os.Mkdir(hpath, 0o755); err == nil {
				a.Hosts = append(a.Hosts, hn)
			} else if !os.IsExist(err) {
				return fmt.Errorf("mkdir host %s: %w", hn, err)
			}
			link := filepath.Join(ssPath, "allowed_hosts", hn)
			if err := os.Symlink(hpath, link); err != nil && !os.IsExist(err) {
				return fmt.Errorf("allowed_hosts %s: %w", hn, err)
			}
		}
	}

	nsPath := filepath.Join(ssPath, "namespaces", strconv.Itoa(e.NamespaceID))
	if err := os.Mkdir(nsPath, 0o755); err != nil {
		return fmt.Errorf("mkdir namespace: %w", err)
	}
	if err := os.WriteFile(filepath.Join(nsPath, "device_path"), []byte(ZvolDevicePath(e.Zvol)), 0o644); err != nil {
		return fmt.Errorf("device_path: %w", err)
	}

	id, err := portID(e, a, ports)
	if err != nil {
		return err
	}
	pPath := filepath.Join(root, "ports", strconv.Itoa(id))

	// ANA: the namespace joins a group of the port; the state is the port's.
	if e.ANAEnabled {
		for _, ag := range e.ANAGroups {
			if ag.NamespaceID != e.NamespaceID {
				continue
			}
			if err := applyANAGroupState(pPath, ag.GroupID, ag.State); err != nil {
				return fmt.Errorf("ana group state: %w", err)
			}
			if err := os.WriteFile(filepath.Join(nsPath, "ana_grpid"), []byte(strconv.Itoa(ag.GroupID)), 0o644); err != nil {
				return fmt.Errorf("ana_grpid: %w", err)
			}
			break
		}
	}

	if err := os.WriteFile(filepath.Join(nsPath, "enable"), []byte("1"), 0o644); err != nil {
		return fmt.Errorf("namespace enable: %w", err)
	}
	if err := os.Symlink(ssPath, filepath.Join(pPath, "subsystems", e.SubsystemNQN)); err != nil {
		return fmt.Errorf("port subsystem link: %w", err)
	}
	return nil
}

// ANAPropagationDelay is the time to wait after writing an ANA state change
// before proceeding with VIP handoff or pool export. The Linux NVMe target
// driver commits the state synchronously, but it then sends an AEN
// (Asynchronous Event Notification) to each connected host; hosts update
// their multipath tables asynchronously. Set to 0 in tests.
var ANAPropagationDelay = 200 * time.Millisecond

// kernelANAState maps the spec's states to the kernel's names. NVMe ANA has
// no "standby": a standby HA node that cannot serve I/O is "inaccessible".
func kernelANAState(s ANAState) string {
	switch s {
	case "", ANAOptimized:
		return "optimized"
	case ANAStandby:
		return "inaccessible"
	}
	return string(s)
}

// applyANAGroupState creates ANA group groupID on the port (group 1 always
// exists) and sets its state, then waits ANAPropagationDelay.
func applyANAGroupState(portPath string, groupID int, state ANAState) error {
	anaDir := filepath.Join(portPath, "ana_groups", strconv.Itoa(groupID))
	if err := os.Mkdir(anaDir, 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("mkdir ana_groups/%d: %w (no ANA support in the kernel?)", groupID, err)
	}
	want := kernelANAState(state)
	stateFile := filepath.Join(anaDir, "ana_state")
	if err := os.WriteFile(stateFile, []byte(want), 0o644); err != nil {
		return fmt.Errorf("write ana_state %q: %w", want, err)
	}
	if got, err := os.ReadFile(stateFile); err != nil || strings.TrimSpace(string(got)) != want {
		return fmt.Errorf("ana_state: wrote %q, the kernel reports %q (%v)", want, strings.TrimSpace(string(got)), err)
	}
	if ANAPropagationDelay > 0 {
		time.Sleep(ANAPropagationDelay)
	}
	return nil
}

// configuredNQNs lists the subsystems DPlaneOS made (restore checks it).
func configuredNQNs() []string {
	a := readApplied()
	var out []string
	for _, n := range a.Subsystems {
		if _, err := os.Stat(filepath.Join(nvmetRoot(), "subsystems", n)); err == nil {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
