package groups

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
)

// Split copies of replicated groups (Design 0001 section 5.11): when a copy
// that has changes the owner does not have is discarded, those changes are
// not destroyed. Before the copy is rolled back, every dataset changed since
// the last common replication snapshot is sent to the owner, which receives
// it as <pool>/dplane-split-<time>-from-<node>/... (a clone of the common
// snapshot, so only the changes travel). The owner's next replication carries
// it back to every copy: both sides keep it until the operator deletes it on
// the owner. The common snapshot stays as the clone's origin until then.

var (
	splitNameRe = regexp.MustCompile(`^dplane-split-[0-9]{8}-[0-9]{6}-from-[a-z0-9][a-z0-9-]{0,31}$`)
	splitSnapRe = regexp.MustCompile(`^split-[0-9]{8}-[0-9]{6}$`)
	splitRelRe  = regexp.MustCompile(`^(/[A-Za-z0-9_.:-]+)*$`)
)

// validRel accepts a dataset path below a pool ("" or "/a/b"); "." and ".."
// are valid characters in dataset names but never as a whole component.
func validRel(rel string) bool {
	if !splitRelRe.MatchString(rel) {
		return false
	}
	for _, c := range strings.Split(rel, "/")[1:] {
		if c == "." || c == ".." {
			return false
		}
	}
	return true
}

// SplitDataset is one dataset of a copy with what was written to it since
// the common snapshot (-1: it did not exist then).
type SplitDataset struct {
	Name    string
	Written int64
}

// SplitCopy is a kept copy, as listed on a node.
type SplitCopy struct {
	Pool    string    `json:"pool"`
	Name    string    `json:"name"`
	Used    int64     `json:"used"`
	Created time.Time `json:"created"`
}

// SplitOps are the ZFS side effects of keeping split copies; replaced in tests.
type SplitOps struct {
	Snapshot func(pool, snap string) error                   // zfs snapshot -r pool@snap
	Changed  func(pool, base string) ([]SplitDataset, error) // datasets of pool and written@base
	Send     func(ctx context.Context, ds, base, snap string) (io.ReadCloser, func() error, error)
	Recv     func(target, origin string, r io.Reader) error // into target, as a clone of origin if set
	Create   func(ds string) error                          // zfs create -p ds
	List     func(pool string) ([]SplitCopy, error)
	Destroy  func(pool, name string) error
}

// DefaultSplitOps use zfs through the command whitelist; streams through exec
// with names generated and validated by the daemon.
var DefaultSplitOps = SplitOps{
	Snapshot: func(pool, snap string) error {
		if out, err := cmdutil.RunMedium("zfs_split_snapshot", "snapshot", "-r", pool+"@"+snap); err != nil {
			return zfsErr("snapshot", out, err)
		}
		return nil
	},
	Changed: func(pool, base string) ([]SplitDataset, error) {
		out, err := cmdutil.RunFast("zfs_split_changed", "list", "-H", "-p", "-o", "name,written@"+base, "-r", pool)
		if err != nil {
			return nil, zfsErr("written since "+base, out, err)
		}
		var ds []SplitDataset
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			f := strings.Split(l, "\t")
			if len(f) != 2 {
				continue
			}
			n, err := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64)
			if err != nil {
				n = -1 // "-": the dataset has no such snapshot
			}
			ds = append(ds, SplitDataset{Name: strings.TrimSpace(f[0]), Written: n})
		}
		return ds, nil
	},
	Send: func(ctx context.Context, ds, base, snap string) (io.ReadCloser, func() error, error) {
		if !splitSnapRe.MatchString(snap) || (base != "" && !replSnapRe.MatchString(base)) {
			return nil, nil, errors.New("invalid snapshot name")
		}
		args := []string{"send"}
		if base != "" {
			args = append(args, "-i", "@"+base)
		}
		args = append(args, ds+"@"+snap)
		cmd := exec.CommandContext(ctx, "zfs", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		return out, func() error {
			if err := cmd.Wait(); err != nil {
				return fmt.Errorf("zfs send: %v: %s", err, strings.TrimSpace(stderr.String()))
			}
			return nil
		}, nil
	},
	Recv: func(target, origin string, r io.Reader) error {
		args := []string{"recv", "-u"}
		if origin != "" {
			args = append(args, "-o", "origin="+origin)
		}
		args = append(args, target)
		cmd := exec.Command("zfs", args...)
		cmd.Stdin = r
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("zfs recv: %v: %s", err, strings.TrimSpace(stderr.String()))
		}
		// Received unmounted (-u): mount it, so it can be browsed.
		_, _ = cmdutil.RunFast("zfs_repl_mount", "mount", target)
		return nil
	},
	Create: func(ds string) error {
		if out, err := cmdutil.RunFast("zfs_split_create", "create", "-p", ds); err != nil {
			return zfsErr("create "+ds, out, err)
		}
		return nil
	},
	List: func(pool string) ([]SplitCopy, error) {
		out, err := cmdutil.RunFast("zfs_split_list", "list", "-H", "-p", "-o", "name,used,creation", "-d", "1", pool)
		if err != nil {
			return nil, zfsErr("list", out, err)
		}
		cs := []SplitCopy{}
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			f := strings.Split(l, "\t")
			if len(f) != 3 {
				continue
			}
			_, name, _ := strings.Cut(f[0], "/")
			if !splitNameRe.MatchString(name) {
				continue
			}
			used, _ := strconv.ParseInt(f[1], 10, 64)
			created, _ := strconv.ParseInt(f[2], 10, 64)
			cs = append(cs, SplitCopy{Pool: pool, Name: name, Used: used, Created: time.Unix(created, 0)})
		}
		return cs, nil
	},
	Destroy: func(pool, name string) error {
		if out, err := cmdutil.RunMedium("zfs_split_destroy", "destroy", "-r", pool+"/"+name); err != nil {
			return zfsErr("destroy", out, err)
		}
		return nil
	},
}

// splitName names a kept copy: when, and from which node.
func splitName(now time.Time, node string) string {
	n := strings.ToLower(node)
	n = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(n, "-")
	n = strings.Trim(n, "-")
	if len(n) > 32 {
		n = n[:32]
	}
	if n == "" {
		n = "node"
	}
	return fmt.Sprintf("dplane-split-%s-from-%s", now.UTC().Format("20060102-150405"), n)
}

// toKeep selects the datasets of a copy that hold changes the owner does not
// have, parents first. Kept copies already in the pool are left out.
func toKeep(pool string, ds []SplitDataset) []SplitDataset {
	var out []SplitDataset
	for _, d := range ds {
		rel := strings.TrimPrefix(d.Name, pool)
		if d.Name != pool && !strings.HasPrefix(rel, "/") {
			continue
		}
		if first, _, _ := strings.Cut(strings.TrimPrefix(rel, "/"), "/"); splitNameRe.MatchString(first) {
			continue
		}
		if d.Written != 0 {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ErrCannotKeep: the divergent changes cannot be kept on the owner.
var ErrCannotKeep = errors.New("the changes here cannot be kept on the owner")

// keepDivergent sends the changes of this node's copy of pool to the owner
// as a kept copy. It returns the copy's name ("" when nothing had changed).
func (m *Manager) keepDivergent(g Group, pool string) (string, error) {
	local, err := m.repl.Snapshots(pool)
	if err != nil {
		return "", err
	}
	if len(local) == 0 {
		return "", fmt.Errorf("%w: this copy shares no replication snapshot with the owner (it is not a split but a different pool)", ErrCannotKeep)
	}
	base := local[len(local)-1]
	remote, err := m.tr.RemoteSnapshots(g.Owner, g.Name, pool)
	if err != nil {
		return "", fmt.Errorf("%w: the owner does not answer: %v", ErrCannotKeep, err)
	}
	if !contains(remote, base) {
		return "", fmt.Errorf("%w: the owner no longer has the common snapshot %s", ErrCannotKeep, base)
	}
	changed, err := m.split.Changed(pool, base)
	if err != nil {
		return "", err
	}
	keep := toKeep(pool, changed)
	if len(keep) == 0 {
		return "", nil
	}
	host, _ := os.Hostname()
	now := time.Now()
	name := splitName(now, host)
	snap := "split-" + now.UTC().Format("20060102-150405")
	if err := m.split.Snapshot(pool, snap); err != nil {
		return "", err
	}
	for _, d := range keep {
		rel := strings.TrimPrefix(d.Name, pool)
		from := base
		if d.Written < 0 {
			from = "" // new since the common snapshot: sent whole
		}
		if err := m.sendSplit(g, pool, name, rel, d.Name, from, snap); err != nil {
			return "", fmt.Errorf("%w: %s: %v", ErrCannotKeep, d.Name, err)
		}
	}
	log.Printf("GROUPS: %s: the changes of %s here are kept on the owner as %s/%s", g.Name, pool, pool, name)
	return name, nil
}

func (m *Manager) sendSplit(g Group, pool, name, rel, ds, base, snap string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, wait, err := m.split.Send(ctx, ds, base, snap)
	if err != nil {
		return err
	}
	sendErr := m.tr.SendSplit(g.Owner, g.Name, pool, name, rel, base, stream)
	if sendErr != nil {
		cancel()
	}
	waitErr := wait()
	if sendErr != nil {
		return sendErr
	}
	return waitErr
}

// ReceiveSplit stores a dataset of a copy that is being discarded on sender,
// on this node (the owner) as part of the kept copy name.
func (m *Manager) ReceiveSplit(sender, group, pool, name, rel, base string, body io.Reader) error {
	g, err := Get(m.db, group)
	if err != nil {
		return err
	}
	switch {
	case g.Topology != Replicated:
		return errors.New("not a replicated group")
	case g.Owner != m.self():
		return errors.New("this node does not own the group: kept copies go to the owner")
	case !g.IsCandidate(sender) || sender == m.self():
		return errors.New("the sender is not a node of this group")
	case !g.HasPool(pool) || !splitNameRe.MatchString(name) || !validRel(rel) || (base != "" && !replSnapRe.MatchString(base)):
		return errors.New("invalid pool, name or snapshot")
	}
	target := pool + "/" + name + rel
	if parent := target[:strings.LastIndex(target, "/")]; parent != pool {
		if err := m.split.Create(parent); err != nil {
			return err
		}
	}
	origin := ""
	if base != "" {
		origin = pool + rel + "@" + base
	}
	if err := m.split.Recv(target, origin, body); err != nil {
		return err
	}
	log.Printf("GROUPS: %s: kept %s from a discarded copy", group, target)
	return nil
}

// Splits lists the kept copies of a group's pools on this node.
func (m *Manager) Splits(g Group) []SplitCopy {
	out := []SplitCopy{}
	if g.Topology != Replicated {
		return out
	}
	for _, p := range g.Pools {
		cs, err := m.split.List(p.Name)
		if err != nil {
			continue // pool not imported here
		}
		out = append(out, cs...)
	}
	return out
}

// DeleteSplit deletes a kept copy. Only on the owner: replication then
// removes it from every copy.
func (m *Manager) DeleteSplit(group, pool, name string) error {
	g, err := Get(m.db, group)
	if err != nil {
		return err
	}
	if g.Owner != m.self() {
		return errors.New("delete kept copies on the owner of the group; replication removes them from the other copies")
	}
	if !g.HasPool(pool) || !splitNameRe.MatchString(name) {
		return errors.New("invalid pool or copy")
	}
	if err := m.split.Destroy(pool, name); err != nil {
		return err
	}
	log.Printf("GROUPS: %s: kept copy %s/%s deleted", group, pool, name)
	return nil
}

// isDependentClones: a replication snapshot that is the origin of a kept
// copy cannot be pruned until the copy is deleted.
func isDependentClones(err error) bool {
	return err != nil && strings.Contains(err.Error(), "dependent clones")
}
