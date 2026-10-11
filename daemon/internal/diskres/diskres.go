// Package diskres keeps the disks of a shared pool reserved for the node
// that serves it (SCSI-3 persistent reservations).
//
// The node that imports a pool registers its key on every disk of the pool,
// removes every other key and takes the reservation. From then on the disks
// refuse writes from any other node: a previous owner that is hung, cut off
// or simply wrong about who owns the pool cannot damage it, whatever it
// does. This holds without any cooperation from that node, which is what
// the other protections (watchdog, quorum) cannot give.
//
// Rules that keep this safe:
//   - a node takes disks over only at the moment it imports the pool, which
//     the cluster (or the operator) has decided;
//   - a node that finds its key gone while it has the pool imported was
//     shut out by another node. It reports that and never registers again
//     on its own;
//   - disks without reservations (SATA behind most bridges, NVMe, virtual
//     disks) are left alone and reported as such.
package diskres

import (
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"dplaned/internal/scsipr"
)

// Backend are the reservation commands; replaced in tests.
type Backend struct {
	Register   func(dev string, key scsipr.RegistrationKey) error
	Unregister func(dev string, key scsipr.RegistrationKey) error
	Reserve    func(dev string, key scsipr.RegistrationKey) error
	Preempt    func(dev string, ours, victim scsipr.RegistrationKey) error
	ReadKeys   func(dev string) (scsipr.PRStatus, error)
}

// SCSI issues the commands to the disks.
var SCSI = Backend{
	Register:   scsipr.Register,
	Unregister: scsipr.Unregister,
	Reserve:    scsipr.Reserve,
	Preempt:    scsipr.Preempt,
	ReadKeys:   scsipr.ReadKeys,
}

// States of one disk, seen from this node.
const (
	Held        = "held"        // reserved for this node alone
	Unclaimed   = "unclaimed"   // no node has registered
	Foreign     = "foreign"     // other nodes are registered, this one is not
	Shared      = "shared"      // this node and others are registered
	Unsupported = "unsupported" // the disk has no reservations
	Failed      = "error"
)

// Disk is the reservation state of one disk.
type Disk struct {
	Device string   `json:"device"`
	State  string   `json:"state"`
	Holder string   `json:"holder,omitempty"` // key that holds the reservation
	Keys   []string `json:"keys,omitempty"`   // registered keys
	Error  string   `json:"error,omitempty"`
}

// Report is the state of the disks of one pool.
type Report struct {
	Disks []Disk `json:"disks"`
}

// Count returns the number of disks in the given state.
func (r Report) Count(state string) int {
	n := 0
	for _, d := range r.Disks {
		if d.State == state {
			n++
		}
	}
	return n
}

// Active reports whether any disk of the pool is reserved for this node.
func (r Report) Active() bool { return r.Count(Held) > 0 }

// Summary describes the report in a few words.
func (r Report) Summary() string {
	total := len(r.Disks)
	switch held, unsup := r.Count(Held), r.Count(Unsupported); {
	case total == 0:
		return "no disks found"
	case held == total:
		return fmt.Sprintf("all %d disks reserved", total)
	case unsup == total:
		return "the disks have no reservations"
	default:
		var parts []string
		for _, s := range []string{Held, Unsupported, Foreign, Shared, Unclaimed, Failed} {
			if n := r.Count(s); n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, s))
			}
		}
		return strings.Join(parts, ", ")
	}
}

// Reserver reserves the disks of pools for this node.
type Reserver struct {
	Key     scsipr.RegistrationKey
	Backend Backend
	// Find returns the disks (device nodes) of the pool with this GUID.
	Find func(guid string) ([]string, error)
	// Rescan, when set, makes the kernel look at the disks again. It is
	// used when no disk of a pool is found: another node may have created
	// the pool on disks this node last looked at when they were empty.
	Rescan func()
	// Tolerance is the share of a pool's disks (percent) that may fail to
	// be reserved without the pool being refused.
	Tolerance func() int
}

// New returns a Reserver for the node with this ID, on the real disks.
func New(nodeID string, tolerance func() int) *Reserver {
	return &Reserver{Key: scsipr.KeyFor(nodeID), Backend: SCSI, Find: FindDisks, Tolerance: tolerance}
}

func (r *Reserver) inspect(dev string) Disk {
	d := Disk{Device: dev}
	st, err := r.Backend.ReadKeys(dev)
	switch {
	case err != nil && scsipr.IsUnsupported(err):
		d.State = Unsupported
		return d
	case err != nil:
		d.State, d.Error = Failed, err.Error()
		return d
	}
	// A node that reaches the disk over several paths is listed once per
	// path, with the same key.
	distinct := map[scsipr.RegistrationKey]bool{}
	for _, k := range st.Keys {
		if !distinct[k] {
			distinct[k] = true
			d.Keys = append(d.Keys, k.String())
		}
	}
	if st.Reserved {
		d.Holder = st.HolderKey.String()
	}
	switch mine := distinct[r.Key]; {
	case len(distinct) == 0:
		d.State = Unclaimed
	case !mine:
		d.State = Foreign
	case len(distinct) == 1 && st.Reserved && st.HolderKey == r.Key:
		d.State = Held
	case len(distinct) == 1:
		d.State = Unclaimed // registered, the reservation is still to be taken
	default:
		d.State = Shared
	}
	return d
}

// take makes this node the only registered node of the disk and gives it
// the reservation.
func (r *Reserver) take(dev string) Disk {
	if err := r.Backend.Register(dev, r.Key); err != nil {
		if scsipr.IsUnsupported(err) {
			return Disk{Device: dev, State: Unsupported}
		}
		return Disk{Device: dev, State: Failed, Error: err.Error()}
	}
	st, err := r.Backend.ReadKeys(dev)
	if err != nil {
		if scsipr.IsUnsupported(err) {
			return Disk{Device: dev, State: Unsupported}
		}
		return Disk{Device: dev, State: Failed, Error: err.Error()}
	}
	var errs []error
	for _, k := range st.Keys {
		if k == r.Key {
			continue
		}
		if err := r.Backend.Preempt(dev, r.Key, k); err != nil {
			errs = append(errs, err)
		}
	}
	if err := r.Backend.Reserve(dev, r.Key); err != nil {
		errs = append(errs, err)
	}
	d := r.inspect(dev)
	if d.State != Held && d.State != Unsupported {
		if d.Error == "" && len(errs) > 0 {
			d.Error = errors.Join(errs...).Error()
		}
		if d.Error == "" {
			d.Error = "the disk is not reserved for this node after taking it (" + d.State + ")"
		}
		d.State = Failed
	}
	return d
}

func (r *Reserver) disks(guid string) ([]string, error) {
	if guid == "" {
		return nil, errors.New("the pool has no GUID")
	}
	devs, err := r.Find(guid)
	if err == nil && len(devs) == 0 && r.Rescan != nil {
		r.Rescan()
		devs, err = r.Find(guid)
	}
	if err != nil {
		return nil, fmt.Errorf("finding the disks of pool %s: %w", guid, err)
	}
	sort.Strings(devs)
	return devs, nil
}

// Acquire reserves the disks of the pool for this node, removing every
// other node's registration. Call it before the pool is imported.
//
// It fails when more disks than the tolerance allows have reservations but
// could not be taken: importing then would mean writing to a pool whose
// disks another node still holds.
func (r *Reserver) Acquire(guid string) (Report, error) {
	devs, err := r.disks(guid)
	if err != nil {
		return Report{}, err
	}
	var rep Report
	for _, dev := range devs {
		rep.Disks = append(rep.Disks, r.take(dev))
	}
	held, failed := rep.Count(Held), rep.Count(Failed)
	if failed > 0 {
		tol := 0
		if r.Tolerance != nil {
			tol = r.Tolerance()
		}
		var which []string
		for _, d := range rep.Disks {
			if d.State == Failed {
				which = append(which, d.Device+": "+d.Error)
			}
		}
		msg := fmt.Sprintf("%d of %d disks with reservations could not be reserved for this node (%s)", failed, held+failed, strings.Join(which, "; "))
		if failed*100 > tol*(held+failed) {
			return rep, errors.New(msg)
		}
		log.Printf("DISK RESERVATIONS: pool %s: %s; within the tolerance of %d%%", guid, msg, tol)
	}
	log.Printf("DISK RESERVATIONS: pool %s: %s (key %s)", guid, rep.Summary(), r.Key)
	return rep, nil
}

// Release removes this node's registration from the disks of the pool (and
// with it the reservation). Call it after the pool was exported here on
// purpose. A node that was shut out has nothing to release.
func (r *Reserver) Release(guid string) error {
	devs, err := r.disks(guid)
	if err != nil {
		return err
	}
	var errs []error
	for _, dev := range devs {
		st, err := r.Backend.ReadKeys(dev)
		if err != nil {
			if !scsipr.IsUnsupported(err) {
				errs = append(errs, err)
			}
			continue
		}
		if !st.Has(r.Key) {
			continue
		}
		if err := r.Backend.Unregister(dev, r.Key); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		log.Printf("DISK RESERVATIONS: pool %s: released", guid)
	}
	return errors.Join(errs...)
}

// Status reports the state of the pool's disks without changing anything.
func (r *Reserver) Status(guid string) (Report, error) {
	devs, err := r.disks(guid)
	if err != nil {
		return Report{}, err
	}
	var rep Report
	for _, dev := range devs {
		rep.Disks = append(rep.Disks, r.inspect(dev))
	}
	return rep, nil
}

// Verify checks the disks of a pool this node has imported. Disks nobody
// has registered on (a disk added to the pool, a replaced one) are taken.
// lost is true when another node has taken a disk from this one: this node
// has been shut out and must not write to the pool. Nothing is taken back.
func (r *Reserver) Verify(guid string) (rep Report, lost bool, err error) {
	devs, err := r.disks(guid)
	if err != nil {
		return Report{}, false, err
	}
	for _, dev := range devs {
		d := r.inspect(dev)
		switch d.State {
		case Unclaimed:
			d = r.take(dev)
			if d.State == Held {
				log.Printf("DISK RESERVATIONS: pool %s: %s reserved (it was not yet)", guid, dev)
			}
		case Foreign:
			lost = true
		}
		rep.Disks = append(rep.Disks, d)
	}
	return rep, lost, nil
}

// ── Finding the disks of a pool ──────────────────────────────────────────────

var lsblkPair = regexp.MustCompile(`([A-Z_]+)="([^"]*)"`)

// parseLsblk returns the whole disks that carry a ZFS label of the pool
// with this GUID, from the output of
// lsblk -P -p -o NAME,PKNAME,TYPE,FSTYPE,UUID.
func parseLsblk(out, guid string) []string {
	seen := map[string]bool{}
	var disks []string
	for _, line := range strings.Split(out, "\n") {
		f := map[string]string{}
		for _, m := range lsblkPair.FindAllStringSubmatch(line, -1) {
			f[m[1]] = m[2]
		}
		if f["FSTYPE"] != "zfs_member" || f["UUID"] != guid {
			continue
		}
		disk := f["NAME"]
		if f["TYPE"] == "part" && f["PKNAME"] != "" {
			disk = f["PKNAME"]
		}
		// Multipath and other mapped devices do not take the commands
		// themselves; their paths appear as disks of their own.
		if disk == "" || strings.HasPrefix(filepath.Base(disk), "dm-") || seen[disk] {
			continue
		}
		seen[disk] = true
		disks = append(disks, disk)
	}
	return disks
}

// FindDisks returns the disks of the pool with this GUID, imported or not:
// every member disk carries the pool's GUID in its label.
func FindDisks(guid string) ([]string, error) {
	out, err := exec.Command("lsblk", "-P", "-p", "-o", "NAME,PKNAME,TYPE,FSTYPE,UUID").Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	return parseLsblk(string(out), guid), nil
}
