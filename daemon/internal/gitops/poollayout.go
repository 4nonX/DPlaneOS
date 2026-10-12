package gitops

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// The layout of the pools as `zpool status -P` shows it.
//
// Two things depend on reading it exactly:
//
//   - the state.yaml DPlaneOS writes to the repository. It used to record
//     every pool as one stripe of all its disks, whatever the pool was: a
//     mirror stood in Git as a stripe, and a machine rebuilt from that file
//     would have been given a pool without redundancy;
//   - the comparison of a declared pool with the pool that exists. ZFS
//     partitions a disk it is given whole and reports the partition
//     (…-part1), so a pool declared with its disks never matched itself.

// poolLayout is what one pool's "config:" section says.
type poolLayout struct {
	Topology PoolTopology
	// Settled is false while the layout is not the pool's lasting one: a
	// disk is being replaced or a spare has stepped in (the section then
	// has "replacing-N" or "spare-N" groups), or a line was not understood.
	// A layout that is not settled is compared as a set of disks only and
	// is not written anywhere.
	Settled bool
	Reason  string
	Disks   []string
}

var (
	vdevSuffixRe = regexp.MustCompile(`-\d+$`)
	wholeDiskRe  = regexp.MustCompile(`^(/dev/disk/by-id/.+)-part1$`)
)

// pathExists is replaced in tests.
var pathExists = func(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// diskOfVdev names the disk a vdev path belongs to. ZFS gives a whole disk
// two partitions, 1 for the data and 9 as reserve, and reports partition 1;
// the disk is what a state.yaml names. A partition somebody gave ZFS as such
// (there is no partition 9 beside it) stays what it is.
func diskOfVdev(path string) string {
	if m := wholeDiskRe.FindStringSubmatch(path); m != nil && pathExists(m[1]+"-part9") {
		return m[1]
	}
	return path
}

// parsePoolLayouts reads the output of `zpool status -P` (any number of pools).
func parsePoolLayouts(output string) map[string]*poolLayout {
	out := map[string]*poolLayout{}
	var (
		cur      *poolLayout
		pool     string
		inConfig bool
		tier     string
		// the top-level vdev lines of the current tier are indented this far
		group *VdevGroup
	)
	flush := func() {
		if cur == nil || group == nil {
			return
		}
		appendGroup(cur, tier, *group)
		group = nil
	}
	unsettle := func(reason string) {
		if cur != nil && cur.Settled {
			cur.Settled, cur.Reason = false, reason
		}
	}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(trimmed, "pool:"); ok {
			flush()
			pool = strings.TrimSpace(v)
			cur = &poolLayout{Settled: true}
			out[pool] = cur
			inConfig, tier = false, "data"
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(trimmed, "config:") {
			inConfig = true
			continue
		}
		if strings.HasPrefix(trimmed, "errors:") {
			flush()
			inConfig = false
			continue
		}
		if !inConfig || trimmed == "" {
			continue
		}
		// Indentation after the leading tab: 0 the pool and the class
		// headers, 2 the top-level vdevs, 4 their disks.
		body := strings.TrimPrefix(line, "\t")
		indent := len(body) - len(strings.TrimLeft(body, " "))
		name := strings.Fields(trimmed)[0]
		if name == "NAME" {
			continue
		}
		isPath := strings.HasPrefix(name, "/")
		switch {
		case indent == 0:
			flush()
			switch name {
			case pool:
				tier = "data"
			case "special", "logs", "cache", "spares":
				tier = map[string]string{"special": "special", "logs": "log", "cache": "cache", "spares": "spare"}[name]
			case "dedup":
				tier = "dedup"
				unsettle("the pool has a dedup class, which state.yaml cannot describe")
			default:
				tier = "unknown"
				unsettle(fmt.Sprintf("unexpected line %q", trimmed))
			}
		case indent == 2:
			if isPath {
				// A disk that is a top-level vdev by itself. Consecutive
				// ones are written as one stripe group.
				d := diskOfVdev(name)
				cur.Disks = append(cur.Disks, d)
				if group != nil && group.Type == "stripe" {
					group.Disks = append(group.Disks, d)
				} else {
					flush()
					group = &VdevGroup{Type: "stripe", Disks: []string{d}}
				}
				continue
			}
			flush()
			if tier == "spare" {
				continue // a dRAID's distributed spare: part of its draid_spec
			}
			kind := vdevSuffixRe.ReplaceAllString(name, "")
			switch {
			case kind == "mirror", kind == "raidz1", kind == "raidz2", kind == "raidz3":
				group = &VdevGroup{Type: kind}
			case strings.HasPrefix(kind, "draid"):
				group = &VdevGroup{Type: "draid", DraidSpec: kind}
			default:
				// replacing-N, spare-N directly under the pool, indirect-N, ...
				group = &VdevGroup{Type: kind}
				unsettle(fmt.Sprintf("%s is in progress or not describable (%s)", kind, name))
			}
		case indent >= 4:
			if !isPath {
				// replacing-N or spare-N inside a mirror or raidz, or a
				// dRAID's distributed spare.
				if strings.Trim(name, "0123456789") == "" {
					unsettle(fmt.Sprintf("a disk is missing (%s)", name))
				} else {
					unsettle(fmt.Sprintf("a disk is being replaced or a spare is in use (%s)", name))
				}
				continue
			}
			d := diskOfVdev(name)
			cur.Disks = append(cur.Disks, d)
			if indent > 4 {
				continue // a disk below replacing-N/spare-N: counted, not laid out
			}
			if group == nil {
				unsettle(fmt.Sprintf("disk %s outside a group", name))
				continue
			}
			group.Disks = append(group.Disks, d)
		default:
			unsettle(fmt.Sprintf("unexpected indentation in %q", trimmed))
		}
	}
	flush()
	for _, l := range out {
		if l.Settled && len(l.Topology.Data) == 0 {
			l.Settled, l.Reason = false, "no data disks found in the pool's status"
		}
	}
	return out
}

func appendGroup(l *poolLayout, tier string, g VdevGroup) {
	switch tier {
	case "data":
		l.Topology.Data = append(l.Topology.Data, g)
	case "special":
		l.Topology.Special = append(l.Topology.Special, g)
	case "log":
		l.Topology.Log = append(l.Topology.Log, g)
	case "cache":
		l.Topology.Cache = append(l.Topology.Cache, g)
	case "spare":
		l.Topology.Spare = append(l.Topology.Spare, g)
	}
}

// Pool options in state.yaml are of two kinds, and ZFS keeps them in two
// places: properties of the pool (zpool set) and properties of the pool's
// root dataset, which its datasets inherit (zfs set).
var poolProperties = map[string]bool{
	"autoexpand": true, "autoreplace": true, "autotrim": true,
	"failmode": true, "listsnapshots": true, "comment": true,
}

// rootDatasetProperties can be declared for a pool; the ones marked false
// can only be given when the pool is created and are not compared later.
var rootDatasetProperties = map[string]bool{
	"compression": true, "atime": true, "recordsize": true, "sync": true, "xattr": true,
	"acltype": true, "secondarycache": true, "copies": true, "dedup": true,
	"relatime": false, "dnodesize": false, "normalization": false, "casesensitivity": false, "utf8only": false,
}

// IsPoolProperty: a property of the pool itself, not of its root dataset.
func IsPoolProperty(key string) bool { return poolProperties[key] }

func validPoolOption(key string) bool {
	_, ds := rootDatasetProperties[key]
	return poolProperties[key] || ds
}

// comparedPoolOption: an option whose value on the system is compared with
// the declared one after the pool exists.
func comparedPoolOption(key string) bool {
	return poolProperties[key] || rootDatasetProperties[key]
}
