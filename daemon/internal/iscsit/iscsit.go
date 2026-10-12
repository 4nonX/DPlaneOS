// Package iscsit configures the kernel's iSCSI target (LIO) from a list of
// targets: each target offers one ZFS volume as LUN 0, on one address, to
// the initiators named in its access list.
//
// The list is what is saved; the kernel is brought in line with it by Apply.
// Changes are made with targetcli; what the kernel currently has is read
// from configfs. On an installed system nothing else configures the iSCSI
// target: Apply owns all of it.
package iscsit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dplaned/internal/cmdutil"
)

// TargetsFile is the saved list of targets. It holds CHAP secrets (the
// initiators need them in clear, so they cannot be stored as hashes).
var TargetsFile = "/var/lib/dplaneos/iscsi-targets.json"

// ACL is one initiator that may use a target.
type ACL struct {
	InitiatorIQN string `json:"initiator_iqn"`
	CHAPUser     string `json:"chap_user,omitempty"`
	CHAPPass     string `json:"chap_pass,omitempty"`
}

// Target is one iSCSI target with one LUN.
type Target struct {
	IQN         string `json:"iqn"`
	Zvol        string `json:"zvol"` // dataset name, e.g. tank/vm-disk
	PortalIP    string `json:"portal_ip,omitempty"`
	PortalPort  int    `json:"portal_port,omitempty"`
	RequireCHAP bool   `json:"require_chap,omitempty"`
	ACLs        []ACL  `json:"acls,omitempty"`
}

var (
	iqnRe      = regexp.MustCompile(`^iqn\.\d{4}-\d{2}\.[a-z0-9\-\.]+:[a-z0-9\-\.]+$`)
	zvolRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*(/[A-Za-z0-9][A-Za-z0-9_.:-]*)+$`)
	chapUserRe = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,64}$`)
	chapPassRe = regexp.MustCompile(`^[\x21-\x7e]{12,255}$`) // printable, no spaces (targetcli joins its arguments)
)

// ValidIQN reports whether s is an iSCSI qualified name as accepted here.
func ValidIQN(s string) error {
	if !iqnRe.MatchString(s) || len(s) > 223 {
		return errors.New("invalid IQN format (expected iqn.YYYY-MM.reverse.domain:id)")
	}
	return nil
}

// ValidCHAP checks CHAP credentials; both empty means none.
func ValidCHAP(user, pass string) error {
	if user == "" && pass == "" {
		return nil
	}
	if !chapUserRe.MatchString(user) {
		return errors.New("invalid CHAP user (letters, digits, . _ @ : -; up to 64)")
	}
	if !chapPassRe.MatchString(pass) {
		return errors.New("CHAP secret must be 12 to 255 printable characters without spaces")
	}
	return nil
}

// Validate checks a target and fills in its defaults. Only ZFS volumes can
// be offered: a whole disk (the system disk) must not leave over the network.
func Validate(t *Target) error {
	if err := ValidIQN(t.IQN); err != nil {
		return err
	}
	t.Zvol = strings.TrimPrefix(strings.TrimSpace(t.Zvol), "/dev/zvol/")
	if !zvolRe.MatchString(t.Zvol) || strings.Contains(t.Zvol, "..") {
		return errors.New("the volume must be a ZFS volume (<pool>/<volume>)")
	}
	if t.PortalIP == "" {
		t.PortalIP = "0.0.0.0"
	}
	if net.ParseIP(t.PortalIP) == nil {
		return errors.New("portal_ip must be an IP address")
	}
	if t.PortalPort == 0 {
		t.PortalPort = 3260
	}
	if t.PortalPort < 1 || t.PortalPort > 65535 {
		return errors.New("invalid portal_port")
	}
	seen := map[string]bool{}
	for _, a := range t.ACLs {
		if err := ValidIQN(a.InitiatorIQN); err != nil {
			return fmt.Errorf("initiator %q: %w", a.InitiatorIQN, err)
		}
		if seen[a.InitiatorIQN] {
			return fmt.Errorf("initiator %s is listed twice", a.InitiatorIQN)
		}
		seen[a.InitiatorIQN] = true
		if err := ValidCHAP(a.CHAPUser, a.CHAPPass); err != nil {
			return err
		}
		if t.RequireCHAP && a.CHAPUser == "" {
			return fmt.Errorf("the target requires CHAP: initiator %s needs a CHAP user and secret", a.InitiatorIQN)
		}
	}
	return nil
}

// Device is the volume's device path.
func (t Target) Device() string { return "/dev/zvol/" + t.Zvol }

// Portal is the address the target listens on, as configfs names it.
func (t Target) Portal() string {
	return net.JoinHostPort(t.PortalIP, strconv.Itoa(t.PortalPort))
}

var nameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// StoreName is the name of a target's storage object.
func StoreName(iqn string) string {
	name := nameRe.ReplaceAllString(iqn, "_")
	if len(name) > 64 {
		// Long names keep their end and gain a digest, so that two IQNs with
		// the same last 64 characters do not share a storage object.
		sum := sha256.Sum256([]byte(iqn))
		name = name[len(name)-55:] + "_" + hex.EncodeToString(sum[:4])
	}
	return name
}

// Serial is the unit serial number of a target's LUN: what an initiator
// recognises the disk by, and what the kernel files the initiators'
// persistent reservations under. targetcli would make one up at random; it
// is derived from the target's name instead, so that the disk is the same
// disk after a restart and on whichever node serves it.
func Serial(iqn string) string {
	sum := sha256.Sum256([]byte("dplaneos iscsi lun\x00" + iqn))
	u := make([]byte, 16)
	copy(u, sum[:16])
	u[6] = (u[6] & 0x0f) | 0x80
	u[8] = (u[8] & 0x3f) | 0x80
	h := hex.EncodeToString(u)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ── The saved list ───────────────────────────────────────────────────────────

// Load reads the saved targets; a missing file is an empty list.
func Load(path string) ([]Target, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var list []Target
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return list, nil
}

// Save writes the list (readable by root only: it holds CHAP secrets).
func Save(path string, list []Target) error {
	if list == nil {
		list = []Target{}
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ── The kernel's state ───────────────────────────────────────────────────────

// Replaced in tests.
var (
	// Configfs is where the kernel's target configuration is mounted.
	Configfs = "/sys/kernel/config/target"
	// Run runs targetcli. Its arguments are checked by the callers (it is
	// not a shell: each argument is passed as it is).
	Run = func(args ...string) (string, error) {
		out, err := cmdutil.Run(2*time.Minute, "targetcli", args...)
		return string(out), err
	}
	deviceOK = func(dev string) error {
		st, err := os.Stat(dev)
		if err != nil {
			return fmt.Errorf("volume device %s: %w", dev, err)
		}
		if st.Mode()&os.ModeDevice == 0 {
			return fmt.Errorf("%s is not a device", dev)
		}
		return nil
	}
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

func tpgDir(iqn string) string { return filepath.Join(Configfs, "iscsi", iqn, "tpgt_1") }

// InKernel lists the targets the kernel serves.
func InKernel() []string {
	var out []string
	for _, n := range dirNames(filepath.Join(Configfs, "iscsi")) {
		if strings.HasPrefix(n, "iqn.") || strings.HasPrefix(n, "eui.") || strings.HasPrefix(n, "naa.") {
			out = append(out, n)
		}
	}
	return out
}

// Active reports whether the kernel serves the target.
func Active(iqn string) bool {
	st, err := os.Stat(filepath.Join(Configfs, "iscsi", iqn))
	return err == nil && st.IsDir()
}

// Portals lists the addresses ("ip:port") a target listens on.
func Portals(iqn string) []string {
	return dirNames(filepath.Join(tpgDir(iqn), "np"))
}

// PortalPorts lists the TCP ports all targets in the kernel listen on.
func PortalPorts() []int {
	seen := map[int]bool{}
	for _, iqn := range InKernel() {
		for _, np := range Portals(iqn) {
			if _, p, err := net.SplitHostPort(np); err == nil {
				if n, err := strconv.Atoi(p); err == nil {
					seen[n] = true
				}
			}
		}
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// Sessions counts the initiators of the access list that are connected.
func Sessions(iqn string) int {
	n := 0
	acls := filepath.Join(tpgDir(iqn), "acls")
	for _, a := range dirNames(acls) {
		info := readAttr(filepath.Join(acls, a, "info"))
		if info != "" && !strings.HasPrefix(info, "No active") {
			n++
		}
	}
	return n
}

// storeNames lists the block storage objects in the kernel.
func storeNames() []string {
	var out []string
	for _, hba := range dirNames(filepath.Join(Configfs, "core")) {
		if !strings.HasPrefix(hba, "iblock_") {
			continue
		}
		out = append(out, dirNames(filepath.Join(Configfs, "core", hba))...)
	}
	sort.Strings(out)
	return out
}

// storeDevice returns the device a storage object serves ("" if none).
func storeDevice(name string) string {
	matches, _ := filepath.Glob(filepath.Join(Configfs, "core", "iblock_*", name, "udev_path"))
	for _, m := range matches {
		return readAttr(m)
	}
	return ""
}

// asWanted reports whether the kernel serves the target's volume, on its
// address, with its authentication mode. (Its access list is compared, and
// changed in place, separately.)
func asWanted(t Target) bool {
	tpg := tpgDir(t.IQN)
	auth := "0"
	if t.RequireCHAP {
		auth = "1"
	}
	portals := Portals(t.IQN)
	return storeDevice(StoreName(t.IQN)) == t.Device() &&
		len(dirNames(filepath.Join(tpg, "lun"))) == 1 &&
		len(portals) == 1 && portals[0] == t.Portal() &&
		readAttr(filepath.Join(tpg, "attrib", "authentication")) == auth &&
		readAttr(filepath.Join(tpg, "enable")) == "1"
}

// ── Apply ────────────────────────────────────────────────────────────────────

var mu sync.Mutex

// says is what targetcli answered, short enough for an error message.
func says(out string, err error) error {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			if len(t) > 240 {
				t = t[:240]
			}
			return errors.New(t)
		}
	}
	if err != nil {
		return err
	}
	return errors.New("no message")
}

func run(what string, args ...string) error {
	if out, err := Run(args...); err != nil {
		return fmt.Errorf("%s: %w", what, says(out, err))
	}
	return nil
}

// Apply brings the kernel's iSCSI target in line with want.
//
// A target that is served as described is not touched: its initiators stay
// connected while other targets are added, changed or removed, and a change
// of its access list is made in place. A target whose volume is not there
// (its pool is not imported) is left as it is and reported in the error;
// the others are applied all the same.
func Apply(want []Target) error {
	mu.Lock()
	defer mu.Unlock()
	list := make([]Target, len(want))
	copy(list, want)
	wanted := map[string]bool{}
	stores := map[string]bool{}
	for i := range list {
		if err := Validate(&list[i]); err != nil {
			return fmt.Errorf("target %q: %w", list[i].IQN, err)
		}
		if wanted[list[i].IQN] {
			return fmt.Errorf("target %q is listed twice", list[i].IQN)
		}
		wanted[list[i].IQN] = true
		stores[StoreName(list[i].IQN)] = true
	}
	var errs []error

	// 1. Targets and storage objects that are not wanted (any more) go. The
	//    storage object holds the volume open: left behind, the volume could
	//    not be destroyed and its pool not be exported.
	for _, iqn := range InKernel() {
		if !wanted[iqn] {
			if err := remove(iqn); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for _, s := range storeNames() {
		if !stores[s] {
			if err := run("removing the storage object "+s, "/backstores/block", "delete", s); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// 2. What is wanted is there.
	for _, t := range list {
		exists := Active(t.IQN)
		if exists && !asWanted(t) {
			if err := deviceOK(t.Device()); err != nil {
				// Its volume cannot be seen right now: a target that is
				// being served is not taken down on a guess.
				errs = append(errs, fmt.Errorf("%s: %w", t.IQN, err))
				continue
			}
			if err := remove(t.IQN); err != nil {
				errs = append(errs, err)
				continue
			}
			exists = false
		}
		if !exists {
			if err := deviceOK(t.Device()); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", t.IQN, err))
				continue
			}
			if err := create(t); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", t.IQN, err))
				_ = remove(t.IQN) // no half-made target is left serving
				continue
			}
		}
		if err := syncACLs(t); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.IQN, err))
		}
	}
	return errors.Join(errs...)
}

// remove deletes a target (which disconnects its initiators) and its
// storage object.
func remove(iqn string) error {
	var errs []error
	if Active(iqn) {
		if err := run("removing the target "+iqn, "/iscsi", "delete", iqn); err != nil {
			errs = append(errs, err)
		}
	}
	name := StoreName(iqn)
	if storeDevice(name) != "" {
		if err := run("removing the storage object of "+iqn, "/backstores/block", "delete", name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func create(t Target) error {
	name := StoreName(t.IQN)
	tpg := "/iscsi/" + t.IQN + "/tpg1"
	if err := run("creating the storage object", "/backstores/block", "create", "name="+name, "dev="+t.Device(), "wwn="+Serial(t.IQN)); err != nil {
		return err
	}
	if err := run("creating the target", "/iscsi", "create", t.IQN); err != nil {
		return err
	}
	if err := run("creating the LUN", tpg+"/luns", "create", "/backstores/block/"+name); err != nil {
		return err
	}
	// targetcli may have given the new target the portal 0.0.0.0:3260 (its
	// default): the target ends up with exactly the one asked for.
	have := false
	for _, p := range Portals(t.IQN) {
		if p == t.Portal() {
			have = true
			continue
		}
		if host, port, err := net.SplitHostPort(p); err == nil {
			_, _ = Run(tpg+"/portals", "delete", host, port) // the create below says if something is in the way
		}
	}
	if !have {
		if err := run("setting the portal "+t.Portal(), tpg+"/portals", "create", t.PortalIP, strconv.Itoa(t.PortalPort)); err != nil {
			return err
		}
	}
	// Authentication is security-relevant: a target whose mode could not be
	// set is not left running. Without CHAP the initiator names of the
	// access list are all that is checked; with it, their secrets too.
	auth := "authentication=0"
	if t.RequireCHAP {
		auth = "authentication=1"
	}
	if err := run("setting the authentication mode", tpg, "set", "attribute", auth, "generate_node_acls=0"); err != nil {
		return err
	}
	return run("enabling the target", tpg, "enable")
}

// syncACLs makes the target's access list in the kernel the wanted one,
// without touching the initiators that stay as they are.
func syncACLs(t Target) error {
	acls := filepath.Join(tpgDir(t.IQN), "acls")
	path := "/iscsi/" + t.IQN + "/tpg1/acls"
	want := map[string]ACL{}
	for _, a := range t.ACLs {
		want[a.InitiatorIQN] = a
	}
	var errs []error
	have := map[string]bool{}
	for _, init := range dirNames(acls) {
		a, ok := want[init]
		same := ok &&
			readAttr(filepath.Join(acls, init, "auth", "userid")) == a.CHAPUser &&
			readAttr(filepath.Join(acls, init, "auth", "password")) == a.CHAPPass
		if same {
			have[init] = true
			continue
		}
		// Not allowed any more, or with other credentials: out (and, with
		// the new credentials, in again below).
		if err := run("removing the initiator "+init, path, "delete", init); err != nil {
			errs = append(errs, err)
			have[init] = true // still there: do not create it over itself
		}
	}
	for _, a := range t.ACLs {
		if have[a.InitiatorIQN] {
			continue
		}
		if err := run("allowing the initiator "+a.InitiatorIQN, path, "create", a.InitiatorIQN); err != nil {
			errs = append(errs, err)
			continue
		}
		if a.CHAPUser == "" {
			continue
		}
		// CHAP that could not be set would leave the initiator allowed
		// without the authentication that was asked for: it goes instead.
		err := run("setting the CHAP user of "+a.InitiatorIQN, path+"/"+a.InitiatorIQN, "set", "auth", "userid="+a.CHAPUser)
		if err == nil {
			err = run("setting the CHAP secret of "+a.InitiatorIQN, path+"/"+a.InitiatorIQN, "set", "auth", "password="+a.CHAPPass)
		}
		if err != nil {
			_, _ = Run(path, "delete", a.InitiatorIQN)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ApplySaved applies the saved list. A cluster node replaces it by a
// function that applies only what this node serves (targets on the volumes
// of a storage group follow the group).
var ApplySaved = func() error {
	list, err := Load(TargetsFile)
	if err != nil {
		return err
	}
	return Apply(list)
}
