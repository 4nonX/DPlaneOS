package handlers

// replication_plan.go - what a backup replication run sends.
//
// The incremental base is found the way zettarepl (TrueNAS) finds it: the
// newest source snapshot that the destination holds, matched by GUID, not by
// name. Two datasets that send to the same remote name therefore never pass
// for each other, and a recorded "last replicated" name that was pruned on
// either side does not matter.
//
// Without a common snapshot a run never overwrites a remote dataset that
// holds snapshots: the remote may have the only copy of older history.
// AllowFromScratch opts in to destroying it and sending in full.

import (
	"errors"
	"fmt"
	"strings"
)

type replSnap struct {
	name string // full name, dataset@snap
	guid string
}

// shortName is the part after "@".
func (s replSnap) shortName() string {
	_, n, _ := strings.Cut(s.name, "@")
	return n
}

// parseSnapGUIDs parses `zfs list -H -p -o name,guid` output (oldest first).
func parseSnapGUIDs(out string) []replSnap {
	var snaps []replSnap
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.Contains(f[0], "@") {
			snaps = append(snaps, replSnap{name: f[0], guid: f[1]})
		}
	}
	return snaps
}

// listLocalSnapshots returns the snapshots of dataset itself, oldest first.
func listLocalSnapshots(dataset string) ([]replSnap, error) {
	out, err := executeCommandWithTimeout(TimeoutFast, "sched_zfs_list_snapshots",
		[]string{"list", "-t", "snapshot", "-H", "-p", "-o", "name,guid", "-s", "creation", "-d", "1", dataset})
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	return parseSnapGUIDs(out), nil
}

// listRemoteSnapshots returns the snapshots of the remote dataset, oldest
// first, and whether the dataset exists at all.
func listRemoteSnapshots(sshArgs []string, sshTarget, dataset string) ([]replSnap, bool, error) {
	args := append(append([]string{}, sshArgs...), sshTarget,
		"zfs", "list", "-t", "snapshot", "-H", "-p", "-o", "name,guid", "-s", "creation", "-d", "1", dataset)
	out, err := executeCommandWithTimeout(TimeoutMedium, "ssh", args)
	if err != nil {
		if strings.Contains(out, "does not exist") {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	return parseSnapGUIDs(out), true, nil
}

// replPlan is the outcome of planReplication.
type replPlan struct {
	base          string // full name of the incremental base; "" = full send
	upToDate      bool   // the remote already holds the newest snapshot
	destroyRemote bool   // destroy the remote dataset first (allow_from_scratch)
}

// errNoCommonSnapshot is returned when the remote holds snapshots, none of
// them shared with the source, and replication from scratch is not allowed.
var errNoCommonSnapshot = errors.New("no common snapshot")

// planReplication decides how to bring the remote up to the newest local
// snapshot. local and remote are oldest first; local is not empty.
func planReplication(local, remote []replSnap, remoteExists, allowFromScratch bool) (replPlan, error) {
	newest := local[len(local)-1]
	onRemote := map[string]bool{}
	for _, r := range remote {
		onRemote[r.guid] = true
	}
	if onRemote[newest.guid] {
		return replPlan{upToDate: true}, nil
	}
	for i := len(local) - 1; i >= 0; i-- {
		if onRemote[local[i].guid] {
			return replPlan{base: local[i].name}, nil
		}
	}
	if !remoteExists {
		return replPlan{}, nil
	}
	if allowFromScratch {
		return replPlan{destroyRemote: true}, nil
	}
	if len(remote) == 0 {
		return replPlan{}, fmt.Errorf("%w: the remote dataset exists but holds no snapshot; "+
			"remove it or enable \"replicate from scratch\" to overwrite it", errNoCommonSnapshot)
	}
	return replPlan{}, fmt.Errorf("%w: the remote dataset holds %d snapshot(s) (newest %s) but none of the source's, "+
		"so an incremental send is impossible. Another dataset may already replicate to this name, or the shared "+
		"snapshots were pruned. Pick another remote dataset, or enable \"replicate from scratch\" to destroy "+
		"the remote copy and send in full", errNoCommonSnapshot, len(remote), remote[len(remote)-1].shortName())
}

// datasetEncrypted reports whether dataset uses ZFS native encryption.
func datasetEncrypted(dataset string) (bool, error) {
	out, err := executeCommandWithTimeout(TimeoutFast, "repl_zfs_get_encryption",
		[]string{"get", "-H", "-o", "value", "encryption", dataset})
	if err != nil {
		return false, fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	v := strings.TrimSpace(out)
	return v != "" && v != "off" && v != "-", nil
}

// backupRecvArgs is how a backup copy is received (zettarepl defaults):
//   - -s: an interrupted transfer can be resumed
//   - -u, -x mountpoint: never mounted over a path of the backup host, now or
//     at its next boot (it inherits a mountpoint below the backup dataset)
//   - -o readonly=on: nothing writes to the copy, so the next incremental
//     applies without the rollback of -F
//   - no -F: with a -R stream it would destroy every snapshot the source has
//     pruned, and the backup could never keep more history than the source
func backupRecvArgs(dataset string) []string {
	return []string{"recv", "-s", "-u", "-x", "mountpoint", "-o", "readonly=on", dataset}
}

// restoreRecvArgs receives a backup copy back: -x readonly drops the readonly
// the backup was received with, -x mountpoint lets the restored dataset
// inherit its mountpoint where it lands.
func restoreRecvArgs(dataset string, force bool) []string {
	args := []string{"recv", "-s", "-x", "readonly", "-x", "mountpoint"}
	if force {
		args = append(args, "-F")
	}
	return append(args, dataset)
}
