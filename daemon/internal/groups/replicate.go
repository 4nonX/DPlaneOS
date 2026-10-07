package groups

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
)

// Replicated storage groups (Design 0001 phase 3c).
//
// The owner snapshots its pools recursively every interval and streams an
// incremental `zfs send -R` to every other candidate over the paired-node
// channel (no SSH setup). A candidate receives only from the group's owner at
// the current epoch, keeps its copy read-only, and refuses a stream that would
// roll back changes made to its own copy since the common snapshot (a split):
// the operator decides, never the software.

var replSnapRe = regexp.MustCompile(`^dplane-[0-9]+-[0-9]+$`)

// ReplOps are the ZFS side effects of replication; replaced in tests.
type ReplOps struct {
	Snapshot     func(pool, snap string) error
	Snapshots    func(pool string) ([]string, error) // replication snapshots of the root dataset, oldest first
	Destroy      func(pool, snap string) error
	SetReadonly  func(pool string, on bool) error
	Written      func(pool, snap string) (int64, error) // bytes written since snap, whole pool
	DatasetCount func(pool string) (int, error)
	// Send starts `zfs send -R [-i base] pool@snap`; wait reports its result.
	Send func(ctx context.Context, pool, base, snap string) (io.ReadCloser, func() error, error)
	Recv func(pool string, r io.Reader) error
}

func zfsErr(what string, out []byte, err error) error {
	return fmt.Errorf("%s: %v: %s", what, err, bytes.TrimSpace(out))
}

// DefaultReplOps use zfs through the command whitelist; send and receive
// stream through exec with daemon-generated, validated names.
var DefaultReplOps = ReplOps{
	Snapshot: func(pool, snap string) error {
		if out, err := cmdutil.RunMedium("zfs_repl_snapshot", "snapshot", "-r", pool+"@"+snap); err != nil {
			return zfsErr("snapshot", out, err)
		}
		return nil
	},
	Snapshots: func(pool string) ([]string, error) {
		out, err := cmdutil.RunFast("zfs_repl_list_snapshots", "list", "-H", "-t", "snapshot", "-o", "name", "-s", "createtxg", "-d", "1", pool)
		if err != nil {
			return nil, zfsErr("list snapshots", out, err)
		}
		var snaps []string
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if _, name, ok := strings.Cut(strings.TrimSpace(l), "@"); ok && replSnapRe.MatchString(name) {
				snaps = append(snaps, name)
			}
		}
		return snaps, nil
	},
	Destroy: func(pool, snap string) error {
		if out, err := cmdutil.RunMedium("zfs_repl_destroy_snapshot", "destroy", "-r", pool+"@"+snap); err != nil {
			return zfsErr("destroy snapshot", out, err)
		}
		return nil
	},
	SetReadonly: func(pool string, on bool) error {
		v := "readonly=off"
		if on {
			v = "readonly=on"
		}
		if out, err := cmdutil.RunFast("zfs_repl_readonly", "set", v, pool); err != nil {
			return zfsErr("set "+v, out, err)
		}
		// Children inherit the property but keep their mount options: a
		// dataset mounted read-only stays "readonly on (temporary)" until it
		// is remounted (VM test). Remount every dataset of the pool.
		out, err := cmdutil.RunFast("zfs_repl_list_datasets", "list", "-H", "-o", "name", "-r", pool)
		if err != nil {
			return zfsErr("list datasets", out, err)
		}
		mode := "remount,rw"
		if on {
			mode = "remount,ro"
		}
		var errs []error
		for _, ds := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			ds = strings.TrimSpace(ds)
			if ds == "" {
				continue
			}
			if rout, rerr := cmdutil.RunFast("zfs_repl_remount", "mount", "-o", mode, ds); rerr != nil {
				// Not mounted, or remount unsupported: unmount and mount again.
				_, _ = cmdutil.RunFast("zfs_repl_unmount", "unmount", ds)
				if mout, merr := cmdutil.RunFast("zfs_repl_mount", "mount", ds); merr != nil && !bytes.Contains(mout, []byte("already mounted")) {
					errs = append(errs, fmt.Errorf("remounting %s: %v: %s / %s", ds, rerr, bytes.TrimSpace(rout), bytes.TrimSpace(mout)))
				}
			}
		}
		return errors.Join(errs...)
	},
	Written: func(pool, snap string) (int64, error) {
		out, err := cmdutil.RunFast("zfs_repl_written", "get", "-r", "-H", "-p", "-o", "value", "written@"+snap, pool)
		if err != nil {
			return 0, zfsErr("written since "+snap, out, err)
		}
		var total int64
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if n, err := strconv.ParseInt(strings.TrimSpace(l), 10, 64); err == nil {
				total += n
			}
		}
		return total, nil
	},
	DatasetCount: func(pool string) (int, error) {
		out, err := cmdutil.RunFast("zfs_repl_list_datasets", "list", "-H", "-o", "name", "-r", pool)
		if err != nil {
			return 0, zfsErr("list datasets", out, err)
		}
		return len(strings.Split(strings.TrimSpace(string(out)), "\n")), nil
	},
	Send: func(ctx context.Context, pool, base, snap string) (io.ReadCloser, func() error, error) {
		if !poolRe.MatchString(pool) || !replSnapRe.MatchString(snap) || (base != "" && !replSnapRe.MatchString(base)) {
			return nil, nil, errors.New("invalid pool or snapshot name")
		}
		args := []string{"send", "-R"}
		if base != "" {
			args = append(args, "-i", "@"+base)
		}
		args = append(args, pool+"@"+snap)
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
	Recv: func(pool string, r io.Reader) error {
		if !poolRe.MatchString(pool) {
			return errors.New("invalid pool name")
		}
		// -F: roll the copy back to the stream's base (divergence was checked
		// before); -s: resumable; readonly=on: the copy stays read-only.
		cmd := exec.Command("zfs", "recv", "-s", "-F", "-o", "readonly=on", pool)
		cmd.Stdin = r
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("zfs recv: %v: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	},
}

// ── Pure helpers ──────────────────────────────────────────────────────────────

// latestCommon is the newest snapshot of local (oldest first) that remote has.
func latestCommon(local, remote []string) string {
	have := map[string]bool{}
	for _, s := range remote {
		have[s] = true
	}
	for i := len(local) - 1; i >= 0; i-- {
		if have[local[i]] {
			return local[i]
		}
	}
	return ""
}

// pruneList returns the snapshots to destroy: all but the newest keep, except
// those still needed as the base for a target that lags behind.
func pruneList(local []string, keep int, needed map[string]bool) []string {
	var out []string
	for i, s := range local {
		if i >= len(local)-keep || needed[s] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Receive refusals.
var (
	ErrNotOwnerSender = errors.New("the sender is not the owner of this group at the current epoch")
	ErrDiverged       = errors.New("the copy here has changes the owner does not have")
)

// checkReceive decides whether a replication stream may be applied here.
func checkReceive(g Group, self, sender string, epoch int64, base string, written int64, datasets int, discardOK bool) error {
	switch {
	case g.Topology != Replicated:
		return errors.New("not a replicated group")
	case g.Owner == self:
		return errors.New("this node owns the group; it does not receive")
	case epoch < g.Epoch:
		return fmt.Errorf("%w (stream epoch %d, group epoch %d)", ErrNotOwnerSender, epoch, g.Epoch)
	case epoch > g.Epoch:
		return fmt.Errorf("this node has not learned epoch %d yet; retried after the next group sync", epoch)
	case g.Owner != sender:
		return ErrNotOwnerSender
	}
	if discardOK {
		return nil
	}
	if base != "" && written > 0 {
		return fmt.Errorf("%w: %d bytes written here since %s. Replication to this node is paused; to resume, discard the changes here (System › High Availability › Storage groups), after copying off anything you need", ErrDiverged, written, base)
	}
	if base == "" && datasets > 1 {
		return fmt.Errorf("%w: a first full replication would replace the %d datasets of this copy. Confirm by discarding the copy here (System › High Availability › Storage groups)", ErrDiverged, datasets-1)
	}
	return nil
}

// ── Records ───────────────────────────────────────────────────────────────────

// ReplState is one replication direction of a group on this node.
type ReplState struct {
	Direction    string     `json:"direction"` // out (owner → peer) or in (peer → here)
	Peer         string     `json:"peer"`
	Pool         string     `json:"pool"`
	LastSnapshot string     `json:"last_snapshot"`
	LastOKAt     *time.Time `json:"last_ok_at"`
	LastError    string     `json:"last_error"`
	DiscardOK    bool       `json:"discard_ok"`
}

func replStates(db *sql.DB, group string) ([]ReplState, error) {
	rows, err := db.Query(`SELECT direction, peer, pool, last_snapshot, last_ok_at, last_error, discard_ok
		FROM group_replication WHERE group_name = $1 ORDER BY direction, peer, pool`, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReplState{}
	for rows.Next() {
		var s ReplState
		var at sql.NullTime
		if err := rows.Scan(&s.Direction, &s.Peer, &s.Pool, &s.LastSnapshot, &at, &s.LastError, &s.DiscardOK); err != nil {
			return nil, err
		}
		if at.Valid {
			s.LastOKAt = &at.Time
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func recordRepl(db *sql.DB, group, dir, peer, pool, snap string, err error) {
	if err != nil {
		_, _ = db.Exec(`INSERT INTO group_replication (group_name, direction, peer, pool, last_error)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (group_name, direction, peer, pool) DO UPDATE SET last_error = EXCLUDED.last_error, updated_at = NOW()`,
			group, dir, peer, pool, err.Error())
		return
	}
	_, _ = db.Exec(`INSERT INTO group_replication (group_name, direction, peer, pool, last_snapshot, last_ok_at, last_error, discard_ok)
		VALUES ($1, $2, $3, $4, $5, NOW(), '', FALSE)
		ON CONFLICT (group_name, direction, peer, pool) DO UPDATE SET last_snapshot = EXCLUDED.last_snapshot,
			last_ok_at = NOW(), last_error = '', discard_ok = FALSE, updated_at = NOW()`,
		group, dir, peer, pool, snap)
}

// ── Owner side ────────────────────────────────────────────────────────────────

// ReplicateNow replicates every pool of a group to every other candidate now.
func (m *Manager) ReplicateNow(name string) error {
	g, err := Get(m.db, name)
	if err != nil {
		return err
	}
	return m.replicate(*g, nil)
}

// replicate sends every pool of g to the targets (all other candidates when
// targets is nil). One new snapshot per pool serves all targets.
func (m *Manager) replicate(g Group, targets []string) error {
	self := m.self()
	if g.Topology != Replicated || g.Owner != self {
		return errors.New("only the owner of a replicated group replicates it")
	}
	if targets == nil {
		targets = without(g.Candidates, self)
	}
	var errs []error
	for _, p := range g.Pools {
		snap := fmt.Sprintf("dplane-%d-%d", g.Epoch, time.Now().UnixNano()/int64(time.Millisecond))
		if err := m.repl.Snapshot(p.Name, snap); err != nil {
			errs = append(errs, err)
			continue
		}
		local, err := m.repl.Snapshots(p.Name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		needed := map[string]bool{snap: true}
		for _, t := range targets {
			base, err := m.sendOne(g, t, p.Name, local, snap)
			recordRepl(m.db, g.Name, "out", t, p.Name, snap, err)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s to %s: %w", p.Name, t, err))
				if base != "" {
					needed[base] = true // keep the base for the next attempt
				}
				continue
			}
		}
		for _, s := range pruneList(local, 3, needed) {
			if err := m.repl.Destroy(p.Name, s); err != nil {
				log.Printf("GROUPS: pruning %s@%s: %v", p.Name, s, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) sendOne(g Group, target, pool string, local []string, snap string) (string, error) {
	remote, err := m.tr.RemoteSnapshots(target, g.Name, pool)
	if err != nil {
		return "", fmt.Errorf("listing the copy's snapshots: %w", err)
	}
	base := latestCommon(local, remote)
	if base == snap {
		return base, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, wait, err := m.repl.Send(ctx, pool, base, snap)
	if err != nil {
		return base, err
	}
	sendErr := m.tr.SendReplica(target, g.Name, pool, g.Epoch, base, snap, stream)
	if sendErr != nil {
		cancel()
	}
	waitErr := wait()
	if sendErr != nil {
		return base, sendErr
	}
	return base, waitErr
}

// ReplicateTick replicates groups owned here whose interval has passed.
func (m *Manager) ReplicateTick() {
	gs, err := List(m.db)
	if err != nil {
		return
	}
	self := m.self()
	for _, g := range gs {
		if g.Topology != Replicated || g.Owner != self {
			continue
		}
		if ok, _ := m.CanWritePool(g.Pools[0].Name); !ok {
			continue
		}
		var due []string
		states, _ := replStates(m.db, g.Name)
		for _, t := range without(g.Candidates, self) {
			last := time.Time{}
			for _, s := range states {
				if s.Direction == "out" && s.Peer == t && s.LastOKAt != nil && (last.IsZero() || s.LastOKAt.Before(last)) {
					last = *s.LastOKAt
				}
			}
			if time.Since(last) >= time.Duration(g.IntervalSecs)*time.Second {
				due = append(due, t)
			}
		}
		if len(due) > 0 {
			if err := m.replicate(g, due); err != nil {
				log.Printf("GROUPS: replication of %s: %v", g.Name, err)
			}
		}
	}
}

// StartReplication runs ReplicateTick every interval.
func (m *Manager) StartReplication(interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			m.ReplicateTick()
		}
	}()
}

// ── Receiving side ────────────────────────────────────────────────────────────

// LocalSnapshots lists the replication snapshots of a pool of a group here.
func (m *Manager) LocalSnapshots(group, pool string) ([]string, error) {
	g, err := Get(m.db, group)
	if err != nil {
		return nil, err
	}
	if !g.HasPool(pool) {
		return nil, fmt.Errorf("pool %s is not in group %s", pool, group)
	}
	return m.repl.Snapshots(pool)
}

// ReceiveReplica applies a replication stream from sender after checking
// that sender owns the group at the current epoch and that the copy here has
// not diverged.
func (m *Manager) ReceiveReplica(sender, group, pool string, epoch int64, base, snap string, body io.Reader) error {
	g, err := Get(m.db, group)
	if err != nil {
		return err
	}
	if !g.HasPool(pool) || (base != "" && !replSnapRe.MatchString(base)) || !replSnapRe.MatchString(snap) {
		return errors.New("invalid pool or snapshot")
	}
	var written int64
	datasets := 1
	if base != "" {
		if written, err = m.repl.Written(pool, base); err != nil {
			return err
		}
	} else if datasets, err = m.repl.DatasetCount(pool); err != nil {
		return err
	}
	var discardOK bool
	_ = m.db.QueryRow(`SELECT COALESCE(bool_or(discard_ok), FALSE) FROM group_replication
		WHERE group_name = $1 AND direction = 'in' AND pool = $2`, group, pool).Scan(&discardOK)
	if err := checkReceive(*g, m.self(), sender, epoch, base, written, datasets, discardOK); err != nil {
		recordRepl(m.db, group, "in", sender, pool, "", err)
		return err
	}
	if err := m.repl.Recv(pool, body); err != nil {
		recordRepl(m.db, group, "in", sender, pool, "", err)
		return err
	}
	recordRepl(m.db, group, "in", sender, pool, snap, nil)
	return nil
}

// DiscardDivergent lets the next replication stream roll back the changes
// made to this node's copy (operator decision, once).
func (m *Manager) DiscardDivergent(group string) error {
	g, err := Get(m.db, group)
	if err != nil {
		return err
	}
	if g.Owner == m.self() {
		return errors.New("this node owns the group; its copy is the one replicated")
	}
	for _, p := range g.Pools {
		if _, err := m.db.Exec(`INSERT INTO group_replication (group_name, direction, peer, pool, discard_ok)
			VALUES ($1, 'in', $2, $3, TRUE)
			ON CONFLICT (group_name, direction, peer, pool) DO UPDATE SET discard_ok = TRUE, updated_at = NOW()`,
			group, g.Owner, p.Name); err != nil {
			return err
		}
	}
	return nil
}

// moveReplicated hands a replicated group to target: a final replication,
// this copy read-only, epoch + 1; the target's copy becomes writable when it
// adopts the update, and replication then runs from the target. If the
// target does not take over, this node stays the owner and writable.
// Caller holds m.mu.
func (m *Manager) moveReplicated(g Group, target string) (*MoveResult, error) {
	if err := m.deactivate(g); err != nil {
		go m.ActivateTick()
		return nil, fmt.Errorf("stopping the group's stacks or exports: %w (the group stays here)", err)
	}
	for _, p := range g.Pools {
		if err := m.repl.SetReadonly(p.Name, true); err != nil {
			m.writable(g)
			return nil, fmt.Errorf("making %s read-only: %w", p.Name, err)
		}
	}
	if err := m.replicate(g, []string{target}); err != nil {
		m.writable(g)
		return nil, fmt.Errorf("final replication to the target: %w (the group stays here)", err)
	}
	ng := g
	ng.Owner, ng.Epoch, ng.UpdatedAt, ng.UpdatedBy = target, g.Epoch+1, time.Now(), m.self()
	if err := m.tr.Push(target, Update{Group: ng}); err != nil {
		m.writable(g)
		return nil, fmt.Errorf("the target did not take over: %w (the group stays here)", err)
	}
	if err := put(m.db, ng); err != nil {
		return nil, err
	}
	res := &MoveResult{Group: &ng}
	for _, e := range m.pushAll(ng, target) {
		res.Warning = append(res.Warning, e.Error()+" (retried automatically)")
	}
	log.Printf("GROUPS: %s moved to %s (epoch %d, replicated)", g.Name, target, ng.Epoch)
	return res, nil
}

func (m *Manager) writable(g Group) {
	for _, p := range g.Pools {
		if err := m.repl.SetReadonly(p.Name, false); err != nil {
			log.Printf("GROUPS: making %s writable again: %v", p.Name, err)
		}
	}
}
