package handlers

import (
	"database/sql"
	"dplaned/internal/gitops"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"dplaned/internal/jobs"
	"github.com/gorilla/mux"
)

// replActiveMu guards replActiveSet, which tracks dataset+peer pairs currently
// being replicated. This prevents two concurrent schedules (or a schedule and a
// manual trigger) from sending to the same destination simultaneously, which would
// corrupt the remote ZFS stream.
var (
	replActiveMu  sync.Mutex
	replActiveSet = map[string]bool{}
)

var errScheduleNotFound = errors.New("schedule not found")

type ReplicationScheduleHandler struct {
	db *sql.DB
}

func NewReplicationScheduleHandler(db *sql.DB) *ReplicationScheduleHandler {
	return &ReplicationScheduleHandler{db: db}
}

// ============================================================
// REPLICATION SCHEDULES
// ============================================================

// ReplicationSchedule defines when and how to trigger replication.
// Connection details are resolved at runtime via RemoteID - see Remote struct.
type ReplicationSchedule struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	SourceDataset     string `json:"source_dataset"`
	RemoteID          string `json:"remote_id"`           // references a Remote peer
	RemotePool        string `json:"remote_pool"`         // destination pool/dataset on the remote
	Interval          string `json:"interval"`            // "hourly","daily","weekly","manual"
	TriggerOnSnapshot bool   `json:"trigger_on_snapshot"` // replicate after each auto-snapshot
	Incremental       bool   `json:"incremental"`         // use -i with last replicated snapshot as base
	Resume            bool   `json:"resume"`              // check for resume token before sending
	Compress          bool   `json:"compress"`
	NonRecursive      bool   `json:"non_recursive,omitempty"` // when true, omit -R from zfs send
	// AllowFromScratch lets a run destroy the remote dataset and send it in
	// full when the remote has snapshots but none in common with the source.
	// Off by default: the remote may hold the only copy of older snapshots.
	AllowFromScratch       bool       `json:"allow_from_scratch,omitempty"`
	RateLimitMB            int        `json:"rate_limit_mb"`
	Enabled                bool       `json:"enabled"`
	LastRun                *time.Time `json:"last_run,omitempty"`
	LastStatus             string     `json:"last_status,omitempty"`
	LastJobID              string     `json:"last_job_id,omitempty"`
	LastReplicatedSnapshot string     `json:"last_replicated_snapshot,omitempty"` // set on success, used as incremental base
}

// replScheduleFile is the on-disk backing store for replication schedules.
// Access is serialized by replStateMu (defined in replication_remotes.go),
// which also guards the remotes file so that cross-store operations (e.g.
// HandleDeleteRemote checking for schedule references) are race-free.
const replScheduleFile = "replication-schedules.json"

func loadReplicationSchedules() ([]ReplicationSchedule, error) {
	replStateMu.RLock()
	defer replStateMu.RUnlock()

	data, err := os.ReadFile(configPath(replScheduleFile))
	if err != nil {
		if os.IsNotExist(err) {
			return []ReplicationSchedule{}, nil
		}
		return nil, err
	}
	var schedules []ReplicationSchedule
	if err := json.Unmarshal(data, &schedules); err != nil {
		return nil, err
	}
	return schedules, nil
}

// atomicModifySchedules holds replStateMu exclusively across the full
// load-modify-save cycle, eliminating TOCTOU races between concurrent requests.
func atomicModifySchedules(fn func([]ReplicationSchedule) ([]ReplicationSchedule, error)) error {
	replStateMu.Lock()
	defer replStateMu.Unlock()

	data, err := os.ReadFile(configPath(replScheduleFile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var schedules []ReplicationSchedule
	if len(data) > 0 {
		if err := json.Unmarshal(data, &schedules); err != nil {
			return err
		}
	}
	modified, err := fn(schedules)
	if err != nil {
		return err
	}
	os.MkdirAll(ConfigDir, 0755)
	out, err := json.MarshalIndent(modified, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(replScheduleFile), out, 0644)
}

// HandleListReplicationSchedules serves GET /api/replication/schedules
func (h *ReplicationScheduleHandler) HandleListReplicationSchedules(w http.ResponseWriter, r *http.Request) {
	schedules, err := loadReplicationSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "schedules": schedules})
}

// HandleCreateReplicationSchedule serves POST /api/replication/schedules
func (h *ReplicationScheduleHandler) HandleCreateReplicationSchedule(w http.ResponseWriter, r *http.Request) {
	var s ReplicationSchedule
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate before acquiring the lock
	if s.Name == "" {
		respondErrorSimple(w, "name is required", http.StatusBadRequest)
		return
	}
	if !isValidDataset(s.SourceDataset) {
		respondErrorSimple(w, "Invalid source_dataset", http.StatusBadRequest)
		return
	}
	validIntervals := map[string]bool{"hourly": true, "daily": true, "weekly": true, "manual": true}
	if !validIntervals[s.Interval] {
		respondErrorSimple(w, "interval must be hourly, daily, weekly, or manual", http.StatusBadRequest)
		return
	}
	if s.RemoteID == "" {
		respondErrorSimple(w, "remote_id is required - create a peer first", http.StatusBadRequest)
		return
	}
	if _, err := ResolveRemoteByID(s.RemoteID); err != nil {
		respondErrorSimple(w, "Peer not found: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.ID = fmt.Sprintf("rs-%d", time.Now().UnixNano())

	if err := atomicModifySchedules(func(schedules []ReplicationSchedule) ([]ReplicationSchedule, error) {
		return append(schedules, s), nil
	}); err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]any{"success": true, "schedule": s})
	gitops.CommitAllAsync(h.db)
}

// HandleUpdateReplicationSchedule serves PUT /api/replication/schedules/{id}
func (h *ReplicationScheduleHandler) HandleUpdateReplicationSchedule(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	if id == "" {
		respondErrorSimple(w, "id is required", http.StatusBadRequest)
		return
	}

	var req ReplicationSchedule
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate updateable fields before acquiring the lock
	if req.SourceDataset != "" && !isValidDataset(req.SourceDataset) {
		respondErrorSimple(w, "Invalid source_dataset", http.StatusBadRequest)
		return
	}
	if req.Interval != "" {
		validIntervals := map[string]bool{"hourly": true, "daily": true, "weekly": true, "manual": true}
		if !validIntervals[req.Interval] {
			respondErrorSimple(w, "interval must be hourly, daily, weekly, or manual", http.StatusBadRequest)
			return
		}
	}
	if req.RemoteID != "" {
		if _, err := ResolveRemoteByID(req.RemoteID); err != nil {
			respondErrorSimple(w, "Peer not found: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	err := atomicModifySchedules(func(schedules []ReplicationSchedule) ([]ReplicationSchedule, error) {
		for i := range schedules {
			if schedules[i].ID != id {
				continue
			}
			if req.Name != "" {
				schedules[i].Name = req.Name
			}
			if req.SourceDataset != "" {
				schedules[i].SourceDataset = req.SourceDataset
			}
			if req.Interval != "" {
				schedules[i].Interval = req.Interval
			}
			if req.RemoteID != "" {
				schedules[i].RemoteID = req.RemoteID
			}
			schedules[i].RemotePool = req.RemotePool
			schedules[i].TriggerOnSnapshot = req.TriggerOnSnapshot
			schedules[i].Incremental = req.Incremental
			schedules[i].Resume = req.Resume
			schedules[i].Compress = req.Compress
			schedules[i].RateLimitMB = req.RateLimitMB
			schedules[i].Enabled = req.Enabled
			schedules[i].NonRecursive = req.NonRecursive
			schedules[i].AllowFromScratch = req.AllowFromScratch
			return schedules, nil
		}
		return nil, errScheduleNotFound
	})

	if errors.Is(err, errScheduleNotFound) {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]any{"success": true})
	gitops.CommitAllAsync(h.db)
}

// HandleDeleteReplicationSchedule serves DELETE /api/replication/schedules/{id}
func (h *ReplicationScheduleHandler) HandleDeleteReplicationSchedule(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	if id == "" {
		respondErrorSimple(w, "id is required", http.StatusBadRequest)
		return
	}

	err := atomicModifySchedules(func(schedules []ReplicationSchedule) ([]ReplicationSchedule, error) {
		out := schedules[:0]
		found := false
		for _, s := range schedules {
			if s.ID == id {
				found = true
				continue
			}
			out = append(out, s)
		}
		if !found {
			return nil, errScheduleNotFound
		}
		return out, nil
	})

	if errors.Is(err, errScheduleNotFound) {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]any{"success": true})
	gitops.CommitAllAsync(h.db)
}

// HandleRunReplicationScheduleNow serves POST /api/replication/schedules/{id}/run
func (h *ReplicationScheduleHandler) HandleRunReplicationScheduleNow(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	if id == "" {
		respondErrorSimple(w, "id is required", http.StatusBadRequest)
		return
	}

	jobID, err := startReplicationSchedule(id)
	switch {
	case errors.Is(err, errScheduleNotFound):
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
	case errors.Is(err, errScheduleRunning):
		respondErrorSimple(w, "This schedule is already running", http.StatusConflict)
	case err != nil:
		respondErrorSimple(w, "Failed to load schedules", http.StatusInternalServerError)
	default:
		respondOK(w, map[string]any{"success": true, "job_id": jobID})
	}
}

var errScheduleRunning = errors.New("schedule already running")

// startReplicationSchedule claims schedule id (marks it running) and then
// launches its job. Claiming first matters: a job that fails at once writes
// "failed", and a "running" written after the launch would overwrite it and
// leave the schedule skipped for good.
func startReplicationSchedule(id string) (string, error) {
	now := time.Now()
	var sched ReplicationSchedule
	if err := atomicModifySchedules(func(all []ReplicationSchedule) ([]ReplicationSchedule, error) {
		for i := range all {
			if all[i].ID != id {
				continue
			}
			if all[i].LastStatus == "running" {
				return nil, errScheduleRunning
			}
			all[i].LastRun = &now
			all[i].LastStatus = "running"
			all[i].LastJobID = ""
			sched = all[i]
			return all, nil
		}
		return nil, errScheduleNotFound
	}); err != nil {
		return "", err
	}
	jobID := launchReplicationJob(sched)
	if err := atomicModifySchedules(func(all []ReplicationSchedule) ([]ReplicationSchedule, error) {
		for i := range all {
			if all[i].ID == id && all[i].LastJobID == "" {
				all[i].LastJobID = jobID
			}
		}
		return all, nil
	}); err != nil {
		log.Printf("WARN: replication schedule %s: failed to persist job ID: %v", id, err)
	}
	return jobID, nil
}

// TriggerReplicationSchedule runs one replication schedule after a snapshot
// schedule that names it (trigger_replication). A schedule that is already
// running is left alone.
func TriggerReplicationSchedule(id string) {
	jobID, err := startReplicationSchedule(id)
	if err != nil {
		log.Printf("INFO: replication schedule %s not triggered: %v", id, err)
		return
	}
	log.Printf("INFO: replication schedule %s triggered by a snapshot schedule (job %s)", id, jobID)
}

// ResetInterruptedReplicationSchedules clears the "running" status that a job
// stopped by a daemon restart left behind; until cleared the monitor would skip
// the schedule forever. Call once at startup, before the monitor starts.
func ResetInterruptedReplicationSchedules() {
	if err := atomicModifySchedules(func(all []ReplicationSchedule) ([]ReplicationSchedule, error) {
		for i := range all {
			if all[i].LastStatus == "running" {
				log.Printf("WARN: replication schedule %s was running when the daemon stopped: marked interrupted", all[i].ID)
				all[i].LastStatus = "interrupted"
			}
		}
		return all, nil
	}); err != nil {
		log.Printf("WARN: replication schedules: cannot reset interrupted runs: %v", err)
	}
}

// launchReplicationJob starts an async replication job and returns the job ID.
// It resolves the peer at runtime, verifies authorization and TCP reachability,
// then executes the ZFS send pipeline.
func launchReplicationJob(s ReplicationSchedule) string {
	return jobs.Start("replication_schedule", func(j *jobs.Job) {
		// Conflict guard: prevent two jobs from sending to the same source+remote
		// simultaneously (concurrent ZFS recv on same dataset corrupts the stream).
		lockKey := s.SourceDataset + "|" + s.RemoteID
		replActiveMu.Lock()
		if replActiveSet[lockKey] {
			replActiveMu.Unlock()
			j.Fail(fmt.Sprintf("replication conflict: another job is already running for %s → remote %s; skipping to avoid stream corruption", s.SourceDataset, s.RemoteID))
			updateScheduleStatus(s.ID, "skipped", j.ID, "")
			return
		}
		replActiveSet[lockKey] = true
		replActiveMu.Unlock()
		defer func() {
			replActiveMu.Lock()
			delete(replActiveSet, lockKey)
			replActiveMu.Unlock()
		}()

		// Step 1: Resolve peer
		if s.RemoteID == "" {
			j.Fail("No peer configured for this schedule - edit the schedule and select a peer")
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}
		remote, err := ResolveRemoteByID(s.RemoteID)
		if err != nil {
			j.Fail(fmt.Sprintf("Peer not found (%s): %v", s.RemoteID, err))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}

		// Step 2: Verify the replication key has been installed on this peer
		if !remote.KeyInstalled {
			j.Fail(fmt.Sprintf("Peer %q is not authorized - open the Peers tab and click Authorize", remote.Name))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}

		// Warn early if rate limiting is requested but pv is not installed
		if s.RateLimitMB > 0 {
			if _, pvErr := exec.LookPath("pv"); pvErr != nil {
				j.Log("WARN: pv is not installed - rate_limit_mb will be ignored. Install pv to enable bandwidth throttling.")
			}
		}

		// Step 3: Quick TCP reachability check before the expensive ZFS send
		dialAddr := net.JoinHostPort(remote.Host, fmt.Sprintf("%d", remote.Port))
		conn, dialErr := net.DialTimeout("tcp", dialAddr, 5*time.Second)
		if dialErr != nil {
			j.Fail(fmt.Sprintf("Peer %q unreachable at %s: %v", remote.Name, dialAddr, dialErr))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}
		conn.Close()

		// Step 4: Find the newest snapshot of the source dataset itself (-d 1:
		// with -r the newest snapshot of a child could be picked instead).
		localSnaps, snapErr := listLocalSnapshots(s.SourceDataset)
		if snapErr != nil {
			j.Fail(fmt.Sprintf("Failed to list snapshots for %s: %v", s.SourceDataset, snapErr))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}
		if len(localSnaps) == 0 {
			j.Fail(fmt.Sprintf("No snapshots found for dataset %s - create a snapshot first", s.SourceDataset))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}
		snapshot := localSnaps[len(localSnaps)-1].name

		remotePool := s.RemotePool
		if remotePool == "" {
			remotePool = s.SourceDataset
		}

		if !isValidSnapshotName(snapshot) {
			j.Fail("Invalid snapshot name: " + snapshot)
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}
		if !isValidDataset(remotePool) {
			j.Fail("Invalid remote pool: " + remotePool)
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}

		// Step 5: Build SSH args using the fixed replication key path and pinned host key
		knownHostsArgs, cleanupKH, khErr := buildKnownHostsArgs(remote)
		defer cleanupKH()
		if khErr != nil {
			j.Fail(fmt.Sprintf("Failed to prepare known_hosts for peer %q: %v", remote.Name, khErr))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}
		if remote.HostKey == "" {
			j.Log("WARN: peer has no pinned host key - running in TOFU mode (accept-new). Use the Peers tab to Test this peer and pin the fingerprint.")
		}

		sshArgs := append([]string{"-i", replKeyPath}, knownHostsArgs...)
		sshArgs = append(sshArgs,
			"-o", "ConnectTimeout=10",
			"-o", "ServerAliveInterval=30",
			"-o", "ServerAliveCountMax=3",
			"-p", fmt.Sprintf("%d", remote.Port),
		)
		sshTarget := fmt.Sprintf("%s@%s", remote.User, remote.Host)

		snapParts := strings.SplitN(snapshot, "@", 2)
		datasetName := snapParts[0]
		parts := strings.Split(datasetName, "/")
		remoteDataset := remotePool + "/" + parts[len(parts)-1]

		// Step 6: Finish a transfer that was interrupted. The resumed stream may
		// be an older snapshot; the regular send below brings the rest.
		if s.Resume {
			token := getResumeToken(sshArgs, sshTarget, remoteDataset)
			if token != "" && isValidResumeToken(token) {
				j.Log("Found resume token - resuming interrupted transfer...")
				if _, resumeErr := execPipedZFSSend(j, []string{"send", "-V", "-t", token},
					sshArgs, sshTarget, backupRecvArgs(remoteDataset), nil); resumeErr != nil {
					j.Log(fmt.Sprintf("Resume failed (%v) - continuing with a regular send", resumeErr))
				} else {
					j.Log("Interrupted transfer completed")
				}
			}
		}

		// Step 7: Plan the send from what both sides hold. The incremental base
		// is the newest local snapshot the remote has (same GUID, so a dataset
		// of the same name but other origin never passes for it).
		encrypted, encErr := datasetEncrypted(s.SourceDataset)
		if encErr != nil {
			j.Fail(fmt.Sprintf("Cannot read the encryption of %s: %v", s.SourceDataset, encErr))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}
		remoteSnaps, remoteExists, rErr := listRemoteSnapshots(sshArgs, sshTarget, remoteDataset)
		if rErr != nil {
			j.Fail(fmt.Sprintf("Cannot list the snapshots of %s on %s: %v", remoteDataset, remote.Name, rErr))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}
		plan, planErr := planReplication(localSnaps, remoteSnaps, remoteExists, s.AllowFromScratch)
		if planErr != nil {
			j.Fail(fmt.Sprintf("%s -> %s:%s: %v", s.SourceDataset, remote.Name, remoteDataset, planErr))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}
		if plan.upToDate {
			j.Log(fmt.Sprintf("%s already holds %s - nothing to send", remoteDataset, snapshot))
			j.Done(map[string]any{"snapshot": snapshot, "remote": fmt.Sprintf("%s:%s", sshTarget, remoteDataset), "peer": remote.Name})
			updateScheduleStatus(s.ID, "done", j.ID, snapshot)
			return
		}
		if plan.destroyRemote {
			j.Log(fmt.Sprintf("WARN: no common snapshot with %s; replication from scratch is allowed: destroying it", remoteDataset))
			destroyArgs := append(append([]string{}, sshArgs...), sshTarget, "zfs", "destroy", "-r", remoteDataset)
			if out, err := executeCommandWithTimeout(TimeoutSlow, "ssh", destroyArgs); err != nil {
				j.Fail(fmt.Sprintf("Cannot destroy %s on %s for the full send: %v: %s", remoteDataset, remote.Name, err, out))
				updateScheduleStatus(s.ID, "failed", j.ID, "")
				return
			}
		}

		sendArgs := []string{"send", "-P"}
		if encrypted {
			// An encrypted dataset can only be sent with its properties raw:
			// the backup holds the ciphertext and needs no key.
			sendArgs = append(sendArgs, "-w")
		} else if s.Compress {
			sendArgs = append(sendArgs, "-c")
		}
		if !s.NonRecursive {
			sendArgs = append(sendArgs, "-R")
		}
		if plan.base != "" {
			// -I: the intermediate snapshots go along, so the backup keeps them.
			sendArgs = append(sendArgs, "-I", plan.base)
			j.Log(fmt.Sprintf("Incremental send: base=%s -> %s", plan.base, snapshot))
		} else {
			j.Log(fmt.Sprintf("Full send of %s (%s holds no snapshots yet)", snapshot, remoteDataset))
		}
		sendArgs = append(sendArgs, snapshot)

		var rateLimit []string
		if s.RateLimitMB > 0 {
			rateLimit = []string{fmt.Sprintf("%dM", s.RateLimitMB)}
		}

		j.Log(fmt.Sprintf("Replicating %s -> %s:%s", snapshot, sshTarget, remoteDataset))

		// Retry loop: retry up to 2 times on transient network failures.
		// Exponential back-off: 30s, 120s.
		const maxRetries = 2
		var execErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				backoff := time.Duration(attempt*attempt) * 30 * time.Second
				j.Log(fmt.Sprintf("Retry %d/%d in %v...", attempt, maxRetries, backoff))
				time.Sleep(backoff)
				// The interrupted receive (-s) left partial state that blocks a
				// new stream into the dataset: continue it from its token.
				if token := getResumeToken(sshArgs, sshTarget, remoteDataset); token != "" && isValidResumeToken(token) {
					j.Log("Resuming the interrupted stream")
					_, execErr = execPipedZFSSend(j, []string{"send", "-V", "-t", token},
						sshArgs, sshTarget, backupRecvArgs(remoteDataset), rateLimit)
					if execErr == nil {
						break
					}
					j.Log(fmt.Sprintf("Attempt %d failed: %v", attempt+1, execErr))
					continue
				}
			}
			_, execErr = execPipedZFSSend(
				j,
				sendArgs,
				sshArgs, sshTarget,
				backupRecvArgs(remoteDataset),
				rateLimit,
			)
			if execErr == nil {
				break
			}
			j.Log(fmt.Sprintf("Attempt %d failed: %v", attempt+1, execErr))
		}

		if execErr != nil {
			j.Fail(fmt.Sprintf("Replication failed after %d attempts: %v", maxRetries+1, execErr))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}

		// Post-receive integrity check: verify the snapshot exists on the remote.
		// zfs send exits 0 even when the remote recv stream is truncated by a
		// crash; confirming the snapshot is present catches that case.
		snapPartsVerify := strings.SplitN(snapshot, "@", 2)
		if len(snapPartsVerify) == 2 {
			remoteSnap := remoteDataset + "@" + snapPartsVerify[1]
			verifyArgs := append([]string{}, sshArgs...)
			verifyArgs = append(verifyArgs, sshTarget,
				"zfs", "list", "-H", "-t", "snapshot", "-o", "name", remoteSnap)
			verifyOut, verifyErr := executeCommandWithTimeout(TimeoutMedium, "ssh", verifyArgs)
			if verifyErr != nil || strings.TrimSpace(verifyOut) != remoteSnap {
				j.Fail(fmt.Sprintf("Replication integrity check failed: snapshot %q not found on remote after send (partial transfer?)", remoteSnap))
				updateScheduleStatus(s.ID, "failed", j.ID, "")
				return
			}
			j.Log(fmt.Sprintf("Integrity check passed: %q confirmed on remote", remoteSnap))
		}

		j.Done(map[string]any{
			"snapshot": snapshot,
			"remote":   fmt.Sprintf("%s:%s", sshTarget, remoteDataset),
			"peer":     remote.Name,
		})
		updateScheduleStatus(s.ID, "done", j.ID, snapshot)
	})
}

// updateScheduleStatus persists last status, job ID, and (on success) the replicated snapshot name.
func updateScheduleStatus(schedID, status, jobID, lastSnapshot string) {
	now := time.Now()
	name := schedID
	if err := atomicModifySchedules(func(schedules []ReplicationSchedule) ([]ReplicationSchedule, error) {
		for i := range schedules {
			if schedules[i].ID == schedID {
				if schedules[i].Name != "" {
					name = schedules[i].Name
				}
				schedules[i].LastRun = &now
				schedules[i].LastStatus = status
				schedules[i].LastJobID = jobID
				if lastSnapshot != "" {
					schedules[i].LastReplicatedSnapshot = lastSnapshot
				}
				break
			}
		}
		return schedules, nil
	}); err != nil {
		log.Printf("WARN: replication_schedule: failed to save status update: %v", err)
		return
	}

	// Failures and the first success after one; every run alerted before
	// (an hourly schedule sent 24 alerts a day).
	replOutcomeMu.Lock()
	prev := replOutcome[schedID]
	if status == "failed" || status == "done" {
		replOutcome[schedID] = status
	}
	replOutcomeMu.Unlock()
	switch {
	case status == "failed":
		DispatchAlert("warning", "replication.failed", name,
			fmt.Sprintf("Replication %s failed (job %s): the copy is not current. See the job log on the Replication page.", name, jobID))
	case status == "done" && prev == "failed":
		DispatchAlert("info", "replication.recovered", name, fmt.Sprintf("Replication %s works again.", name))
	}
}

// replOutcome: the last finished status per schedule ("running" is written
// in between, so the stored status cannot tell a recovery).
var (
	replOutcomeMu sync.Mutex
	replOutcome   = map[string]string{}
)

// TriggerPostSnapshotReplication fires replication for all enabled schedules
// that have TriggerOnSnapshot=true and match the given dataset.
// Called after a snapshot is successfully created.
//
// The "already running" guard and the status update to "running" happen inside
// a single atomicModifySchedules call so that two concurrent callers (e.g. a
// cron tick and a manual trigger) cannot both see "not running" and both launch
// duplicate jobs for the same schedule.
func TriggerPostSnapshotReplication(dataset string) {
	now := time.Now()
	var toFire []ReplicationSchedule

	if err := atomicModifySchedules(func(all []ReplicationSchedule) ([]ReplicationSchedule, error) {
		for i := range all {
			s := &all[i]
			if !s.Enabled || !s.TriggerOnSnapshot {
				continue
			}
			if s.SourceDataset != dataset && !strings.HasPrefix(dataset, s.SourceDataset+"/") {
				continue
			}
			if s.LastStatus == "running" {
				log.Printf("INFO: TriggerPostSnapshotReplication: skipping schedule %s (already running job %s)", s.ID, s.LastJobID)
				continue
			}
			toFire = append(toFire, *s)
			s.LastRun = &now
			s.LastStatus = "running"
			s.LastJobID = ""
		}
		return all, nil
	}); err != nil {
		log.Printf("WARN: TriggerPostSnapshotReplication: failed to mark schedules running: %v", err)
		return
	}

	for _, sched := range toFire {
		jobID := launchReplicationJob(sched)
		log.Printf("INFO: post-snapshot replication triggered for dataset=%s schedule=%s job=%s", dataset, sched.ID, jobID)
		if err := atomicModifySchedules(func(all []ReplicationSchedule) ([]ReplicationSchedule, error) {
			for i := range all {
				if all[i].ID == sched.ID {
					all[i].LastJobID = jobID
					break
				}
			}
			return all, nil
		}); err != nil {
			log.Printf("WARN: TriggerPostSnapshotReplication: failed to persist job ID for %s: %v", sched.ID, err)
		}
	}
}

// ============================================================
// BACKGROUND MONITOR
// ============================================================

// StartReplicationMonitor starts a background ticker that checks replication
// schedules every 5 minutes and fires any that are due. It also runs snapshot
// retention policies after each tick so that local snapshots are pruned once
// the remote backup is confirmed current.
func StartReplicationMonitor() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			runDueReplicationSchedules()
			// Run retention after replication so we only prune locally once the
			// remote copy is current. Running after every tick (not just after
			// successful jobs) is intentional: pruning is idempotent and the policy
			// handles the "what if remote is stale" case via the keep counts.
			RunRetentionPolicies()
		}
	}()
	log.Println("Replication schedule monitor started (checks every 5 min, retention after each tick)")
}

// runDueReplicationSchedules checks all enabled schedules and fires any that are
// due. The "already running" guard and the "mark running" write happen inside a
// single atomicModifySchedules call so that two concurrent ticker ticks (or a
// tick racing a manual trigger) cannot both observe "not running" and both
// launch duplicate jobs for the same schedule.
func runDueReplicationSchedules() {
	now := time.Now()
	var toFire []ReplicationSchedule

	if err := atomicModifySchedules(func(all []ReplicationSchedule) ([]ReplicationSchedule, error) {
		for i := range all {
			s := &all[i]
			if !s.Enabled || s.Interval == "manual" {
				continue
			}
			if s.LastStatus == "running" {
				log.Printf("INFO: replication monitor: skipping schedule %s (job %s still running)", s.ID, s.LastJobID)
				continue
			}
			if !isReplicationDue(*s, now) {
				continue
			}
			toFire = append(toFire, *s)
			s.LastRun = &now
			s.LastStatus = "running"
			s.LastJobID = ""
		}
		return all, nil
	}); err != nil {
		log.Printf("WARN: replication monitor: failed to mark schedules running: %v", err)
		return
	}

	for _, sched := range toFire {
		jobID := launchReplicationJob(sched)
		log.Printf("INFO: replication monitor: firing schedule id=%s dataset=%s interval=%s job=%s",
			sched.ID, sched.SourceDataset, sched.Interval, jobID)
		if err := atomicModifySchedules(func(all []ReplicationSchedule) ([]ReplicationSchedule, error) {
			for i := range all {
				if all[i].ID == sched.ID {
					all[i].LastJobID = jobID
					break
				}
			}
			return all, nil
		}); err != nil {
			log.Printf("WARN: replication monitor: failed to persist job ID for schedule %s: %v", sched.ID, err)
		}
	}
}

// isReplicationDue returns true if the schedule should fire at time t.
func isReplicationDue(s ReplicationSchedule, t time.Time) bool {
	if s.LastRun == nil {
		return true // Never run - fire immediately
	}

	elapsed := t.Sub(*s.LastRun)

	switch s.Interval {
	case "hourly":
		return elapsed >= time.Hour
	case "daily":
		return elapsed >= 24*time.Hour
	case "weekly":
		return elapsed >= 7*24*time.Hour
	default:
		return false
	}
}
