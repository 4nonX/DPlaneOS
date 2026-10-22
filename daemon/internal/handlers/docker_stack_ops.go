package handlers

// docker_stack_ops.go - the operations a compose stack goes through, shared
// by the stack handlers, auto-update and the update job:
//
//   - one operation per stack at a time (Komodo's per-resource action state);
//   - compose always runs with an explicit project name (-p);
//   - new files are checked with `docker compose config` before they replace
//     the running ones, and replaced atomically;
//   - an update records the images the services ran, snapshots the ZFS
//     datasets the stack mounts, pulls, recreates, waits until every service
//     is up and, if one is not, goes back to the previous images.

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/dockerclient"
	"dplaned/internal/jobs"
)

// ── One operation per stack ─────────────────────────────────────────────────

var stackLocks sync.Map // name -> *sync.Mutex

// lockStack takes the stack's lock if it is free. Two compose runs on one
// project at once (an edit while an update pulls) leave it half-applied.
func lockStack(name string) (unlock func(), ok bool) {
	m, _ := stackLocks.LoadOrStore(name, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	if !mu.TryLock() {
		return nil, false
	}
	return mu.Unlock, true
}

// ── Compose invocations ─────────────────────────────────────────────────────

// stackComposeArgs is "compose -p NAME --project-directory DIR -f FILE SUB...".
func stackComposeArgs(name, dir string, sub ...string) []string {
	args := []string{"compose", "-p", name, "--project-directory", dir, "-f", filepath.Join(dir, "docker-compose.yml")}
	return append(args, sub...)
}

// validateStackFiles checks a compose file and its .env with `docker compose
// config` before they replace the running ones. env nil: the current .env.
func validateStackFiles(name, dir, yaml string, env *string) error {
	cand := filepath.Join(dir, ".dplaneos-candidate.yml")
	candEnv := filepath.Join(dir, ".dplaneos-candidate.env")
	defer os.Remove(cand)
	defer os.Remove(candEnv)
	if err := os.WriteFile(cand, []byte(yaml), 0o640); err != nil {
		return err
	}
	envText := readEnvFile(dir)
	if env != nil {
		envText = *env
	}
	if err := os.WriteFile(candEnv, []byte(envText), 0o640); err != nil {
		return err
	}
	out, err := cmdutil.RunMedium("docker_compose", "compose", "-p", name, "--project-directory", dir,
		"-f", cand, "--env-file", candEnv, "config", "-q")
	if err != nil {
		msg := strings.TrimSpace(strings.ReplaceAll(string(out), cand, "docker-compose.yml"))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("invalid compose file: %s", msg)
	}
	return nil
}

// stackFiles is the compose file and .env of a stack, as they were.
type stackFiles struct {
	yaml   []byte
	env    []byte
	hasEnv bool
}

func readStackFiles(dir string) (stackFiles, error) {
	var f stackFiles
	var err error
	if f.yaml, err = os.ReadFile(filepath.Join(dir, "docker-compose.yml")); err != nil {
		return f, err
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".env")); err == nil {
		f.env, f.hasEnv = b, true
	}
	return f, nil
}

// writeStackFiles replaces the compose file and, unless env is nil, the
// .env (an empty env removes it), each atomically.
func writeStackFiles(dir string, yaml []byte, env *string) error {
	if err := writeFileAtomic(filepath.Join(dir, "docker-compose.yml"), yaml, 0o640); err != nil {
		return err
	}
	if env == nil {
		return nil
	}
	envPath := filepath.Join(dir, ".env")
	if *env == "" {
		if err := os.Remove(envPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return writeFileAtomic(envPath, []byte(*env), 0o640)
}

// restore puts the files back as they were.
func (f stackFiles) restore(dir string) error {
	env := ""
	if f.hasEnv {
		env = string(f.env)
	}
	return writeStackFiles(dir, f.yaml, &env)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ── Services and their state ────────────────────────────────────────────────

// stackService is one container of a stack as an update sees it.
type stackService struct {
	Service  string `json:"service"`
	ID       string `json:"container_id"`
	ImageRef string `json:"image"`    // as in the compose file: "lscr.io/linuxserver/plex:latest"
	ImageID  string `json:"image_id"` // what it runs
}

// stackServices returns the stack's containers with the image each runs.
func stackServices(ctx context.Context, dc *dockerclient.Client, name string) ([]stackService, error) {
	cs, err := dc.ProjectContainers(ctx, name)
	if err != nil {
		return nil, err
	}
	var out []stackService
	for _, c := range cs {
		d, err := dc.Inspect(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, stackService{Service: c.Labels["com.docker.compose.service"], ID: c.ID,
			ImageRef: d.Config.Image, ImageID: d.Image})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out, nil
}

// waitStackUp waits until every container of the stack is up: healthy when
// it has a health check, otherwise running without restarting for
// dockerclient.StableFor. A one-shot service (restart "no") that exited 0
// counts as done.
func waitStackUp(ctx context.Context, dc *dockerclient.Client, name string, timeout time.Duration) error {
	svcs, err := stackServices(ctx, dc, name)
	if err != nil {
		return err
	}
	if len(svcs) == 0 {
		return fmt.Errorf("the stack has no containers")
	}
	var failed []string
	for _, s := range svcs {
		d, err := dc.Inspect(ctx, s.ID)
		if err == nil && !d.State.Running && d.State.ExitCode == 0 &&
			(d.HostConfig.RestartPolicy.Name == "" || d.HostConfig.RestartPolicy.Name == "no") {
			continue
		}
		if ok, err := dc.WaitForHealthy(ctx, s.ID, timeout); !ok {
			failed = append(failed, fmt.Sprintf("%s (%v)", s.Service, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("not up: %s", strings.Join(failed, ", "))
	}
	return nil
}

// ── ZFS snapshots of what a stack mounts ────────────────────────────────────

// stackDatasets returns the ZFS filesystems holding the stack's bind mounts.
func stackDatasets(ctx context.Context, dc *dockerclient.Client, name string) ([]string, error) {
	cs, err := dc.ProjectContainers(ctx, name)
	if err != nil {
		return nil, err
	}
	out, err := cmdutil.RunZFS("zfs", "list", "-H", "-t", "filesystem", "-o", "name,mountpoint,mounted")
	if err != nil {
		return nil, nil // no ZFS: nothing to snapshot
	}
	type fs struct{ name, mp string }
	var fss []fs
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 3 && f[2] == "yes" && strings.HasPrefix(f[1], "/") {
			fss = append(fss, fs{f[0], f[1]})
		}
	}
	set := map[string]bool{}
	for _, c := range cs {
		for _, m := range c.Mounts {
			if m.Type != "bind" {
				continue
			}
			best := ""
			bestLen := -1
			for _, f := range fss {
				if (m.Source == f.mp || strings.HasPrefix(m.Source, strings.TrimSuffix(f.mp, "/")+"/")) && len(f.mp) > bestLen {
					best, bestLen = f.name, len(f.mp)
				}
			}
			// A root filesystem on ZFS (mounted at /) is not stack data.
			if best != "" && bestLen > 1 {
				set[best] = true
			}
		}
	}
	var ds []string
	for d := range set {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	return ds, nil
}

const preUpdateKeep = 3 // pre-update snapshots kept per stack and dataset

// snapshotForUpdate snapshots each dataset as <ds>@pre-update-<stack>-<time>
// and keeps the newest preUpdateKeep of them.
func snapshotForUpdate(datasets []string, stack string, now time.Time) ([]string, error) {
	prefix := "pre-update-" + stack + "-"
	var snaps []string
	for _, ds := range datasets {
		snap := ds + "@" + prefix + now.Format("20060102-150405")
		if out, err := cmdutil.RunZFS("zfs", "snapshot", snap); err != nil {
			return snaps, fmt.Errorf("zfs snapshot %s: %v: %s", snap, err, strings.TrimSpace(string(out)))
		}
		snaps = append(snaps, snap)
		pruneSnapshotsWithPrefix(ds, prefix, preUpdateKeep)
	}
	return snaps, nil
}

func pruneSnapshotsWithPrefix(ds, prefix string, keep int) {
	out, err := cmdutil.RunZFS("zfs", "list", "-H", "-t", "snapshot", "-o", "name", "-s", "creation", "-d", "1", ds)
	if err != nil {
		return
	}
	var matching []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if _, n, ok := strings.Cut(line, "@"); ok && strings.HasPrefix(n, prefix) {
			matching = append(matching, line)
		}
	}
	for i := 0; i < len(matching)-keep; i++ {
		if _, err := cmdutil.RunZFS("zfs", "destroy", matching[i]); err != nil {
			log.Printf("WARN: pre-update snapshot %s not pruned: %v", matching[i], err)
		}
	}
}

// ── Update with rollback ────────────────────────────────────────────────────

// StackUpdateOptions control an update.
type StackUpdateOptions struct {
	Snapshot      bool          // snapshot the stack's datasets first (default on)
	RollbackData  bool          // on failure also roll the datasets back to that snapshot
	HealthTimeout time.Duration // per service; 0 = 90s
}

// StackUpdateResult is what an update did.
type StackUpdateResult struct {
	Updated    []string `json:"updated"`     // services that got a new image
	UpToDate   bool     `json:"up_to_date"`  // nothing new was pulled
	RolledBack bool     `json:"rolled_back"` // the previous images run again
	Snapshots  []string `json:"snapshots"`
	Error      string   `json:"error,omitempty"`
	// FailedImages: image ref -> registry digest of the image that failed,
	// so auto-update does not try the same image again.
	FailedImages map[string]string `json:"failed_images,omitempty"`
}

// jobLog adapts a job to updateStack's progress callback.
func jobLog(j *jobs.Job) func(string) {
	return func(s string) { j.Log(s) }
}

// updateStack pulls a stack's images and recreates what changed; if the
// stack does not come up it goes back to the images it ran before (and,
// with RollbackData, to the snapshot of its data). The caller holds the
// lock. progress (may be nil) receives one line per step.
func updateStack(ctx context.Context, progress func(string), name string, opt StackUpdateOptions) StackUpdateResult {
	res := StackUpdateResult{}
	logf := func(f string, a ...any) {
		if progress != nil {
			progress(fmt.Sprintf(f, a...))
		}
		log.Printf("STACK UPDATE %s: "+f, append([]any{name}, a...)...)
	}
	if opt.HealthTimeout <= 0 {
		opt.HealthTimeout = 90 * time.Second
	}
	dir, err := stackDir(name)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	dc := dockerclient.New()

	before, err := stackServices(ctx, dc, name)
	if err != nil {
		res.Error = "reading the running services: " + err.Error()
		return res
	}
	prev := map[string]stackService{} // image ref -> what ran
	for _, s := range before {
		prev[s.ImageRef] = s
	}

	if opt.Snapshot {
		if ds, err := stackDatasets(ctx, dc, name); err != nil {
			logf("WARN: cannot find the stack's datasets: %v", err)
		} else if len(ds) > 0 {
			snaps, err := snapshotForUpdate(ds, name, time.Now())
			res.Snapshots = snaps
			if err != nil {
				res.Error = err.Error()
				return res
			}
			logf("snapshots: %s", strings.Join(snaps, ", "))
		}
	}

	logf("pulling images")
	if out, err := cmdutil.RunSlow("docker_compose", stackComposeArgs(name, dir, "pull")...); err != nil {
		res.Error = fmt.Sprintf("pull failed, nothing changed: %v: %s", err, lastLines(out, 5))
		return res
	}
	changed := map[string]bool{}
	for ref, s := range prev {
		img, err := dc.InspectImage(ctx, ref)
		if err == nil && img.ID != s.ImageID {
			changed[ref] = true
		}
	}
	if len(changed) == 0 && len(before) > 0 {
		res.UpToDate = true
		logf("all images are current")
		return res
	}

	logf("recreating services")
	upOut, upErr := cmdutil.RunSlow("docker_compose", stackComposeArgs(name, dir, "up", "-d", "--remove-orphans")...)
	var failure error
	if upErr != nil {
		failure = fmt.Errorf("compose up: %v: %s", upErr, lastLines(upOut, 5))
	} else if err := waitStackUp(ctx, dc, name, opt.HealthTimeout); err != nil {
		failure = err
	}
	for ref := range changed {
		res.Updated = append(res.Updated, prev[ref].Service)
	}
	sort.Strings(res.Updated)
	if failure == nil {
		logf("updated: %s", strings.Join(res.Updated, ", "))
		return res
	}

	// Back to what ran: point each changed tag at the previous image again
	// and recreate. The tag stays there until the next update pulls.
	logf("update failed (%v): going back to the previous images", failure)
	res.Error = failure.Error()
	res.FailedImages = map[string]string{}
	for ref := range changed {
		if img, err := dc.InspectImage(ctx, ref); err == nil {
			for _, d := range img.RepoDigests {
				if _, dg, ok := strings.Cut(d, "@"); ok {
					res.FailedImages[ref] = dg
					break
				}
			}
		}
		repo, tag := dockerclient.SplitImageRef(ref)
		if tag == "" {
			continue // pinned by digest: compose recreates from the pin
		}
		if err := dc.TagImage(ctx, prev[ref].ImageID, repo, tag); err != nil {
			res.Error += "; re-tagging " + ref + ": " + err.Error()
		}
	}
	if opt.RollbackData && len(res.Snapshots) > 0 {
		cmdutil.RunMedium("docker_compose", stackComposeArgs(name, dir, "stop")...) //nolint
		for _, s := range res.Snapshots {
			if out, err := cmdutil.RunZFS("zfs", "rollback", s); err != nil {
				res.Error += fmt.Sprintf("; zfs rollback %s: %v: %s", s, err, strings.TrimSpace(string(out)))
			} else {
				logf("data rolled back to %s", s)
			}
		}
	}
	if out, err := cmdutil.RunSlow("docker_compose", stackComposeArgs(name, dir, "up", "-d", "--remove-orphans")...); err != nil {
		res.Error += fmt.Sprintf("; going back failed: %v: %s", err, lastLines(out, 5))
		return res
	}
	if err := waitStackUp(ctx, dc, name, opt.HealthTimeout); err != nil {
		res.Error += "; after going back: " + err.Error()
		return res
	}
	res.RolledBack = true
	logf("back on the previous images")
	return res
}

func lastLines(out []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
