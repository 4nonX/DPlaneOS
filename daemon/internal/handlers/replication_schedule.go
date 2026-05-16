package handlers

import (
	"database/sql"
	"dplaned/internal/gitops"
	"encoding/json"
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
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	SourceDataset          string     `json:"source_dataset"`
	RemoteID               string     `json:"remote_id"`            // references a Remote peer
	RemotePool             string     `json:"remote_pool"`          // destination pool/dataset on the remote
	Interval               string     `json:"interval"`             // "hourly","daily","weekly","manual"
	TriggerOnSnapshot      bool       `json:"trigger_on_snapshot"`  // replicate after each auto-snapshot
	Incremental            bool       `json:"incremental"`          // use -i with last replicated snapshot as base
	Resume                 bool       `json:"resume"`               // check for resume token before sending
	Compress               bool       `json:"compress"`
	NonRecursive           bool       `json:"non_recursive,omitempty"` // when true, omit -R from zfs send
	RateLimitMB            int        `json:"rate_limit_mb"`
	Enabled                bool       `json:"enabled"`
	LastRun                *time.Time `json:"last_run,omitempty"`
	LastStatus             string     `json:"last_status,omitempty"`
	LastJobID              string     `json:"last_job_id,omitempty"`
	LastReplicatedSnapshot string     `json:"last_replicated_snapshot,omitempty"` // set on success, used as incremental base
}

// replSchedMu guards in-memory schedule list and the on-disk JSON file.
var replSchedMu sync.RWMutex

const replScheduleFile = "replication-schedules.json"

func loadReplicationSchedules() ([]ReplicationSchedule, error) {
	replSchedMu.RLock()
	defer replSchedMu.RUnlock()

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

func saveReplicationSchedules(schedules []ReplicationSchedule) error {
	replSchedMu.Lock()
	defer replSchedMu.Unlock()

	os.MkdirAll(ConfigDir, 0755)
	data, err := json.MarshalIndent(schedules, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(replScheduleFile), data, 0644)
}

// HandleListReplicationSchedules serves GET /api/replication/schedules
func (h *ReplicationScheduleHandler) HandleListReplicationSchedules(w http.ResponseWriter, r *http.Request) {
	schedules, err := loadReplicationSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "schedules": schedules})
}

// HandleCreateReplicationSchedule serves POST /api/replication/schedules
func (h *ReplicationScheduleHandler) HandleCreateReplicationSchedule(w http.ResponseWriter, r *http.Request) {
	var s ReplicationSchedule
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate
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

	// Generate unique ID
	s.ID = fmt.Sprintf("rs-%d", time.Now().UnixNano())

	schedules, err := loadReplicationSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules", http.StatusInternalServerError)
		return
	}

	schedules = append(schedules, s)
	if err := saveReplicationSchedules(schedules); err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{"success": true, "schedule": s})

	// GITOPS HOOK: write state back to git
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

	schedules, err := loadReplicationSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules", http.StatusInternalServerError)
		return
	}

	found := false
	for i := range schedules {
		if schedules[i].ID == id {
			// Update fields
			if req.Name != "" {
				schedules[i].Name = req.Name
			}
			if req.SourceDataset != "" {
				if !isValidDataset(req.SourceDataset) {
					respondErrorSimple(w, "Invalid source_dataset", http.StatusBadRequest)
					return
				}
				schedules[i].SourceDataset = req.SourceDataset
			}
			if req.Interval != "" {
				validIntervals := map[string]bool{"hourly": true, "daily": true, "weekly": true, "manual": true}
				if !validIntervals[req.Interval] {
					respondErrorSimple(w, "interval must be hourly, daily, weekly, or manual", http.StatusBadRequest)
					return
				}
				schedules[i].Interval = req.Interval
			}
			if req.RemoteID != "" {
				if _, err := ResolveRemoteByID(req.RemoteID); err != nil {
					respondErrorSimple(w, "Peer not found: "+err.Error(), http.StatusBadRequest)
					return
				}
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

			found = true
			break
		}
	}

	if !found {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}

	if err := saveReplicationSchedules(schedules); err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{"success": true})

	// GITOPS HOOK: write state back to git
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

	schedules, err := loadReplicationSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules", http.StatusInternalServerError)
		return
	}

	var newSchedules []ReplicationSchedule
	found := false
	for _, s := range schedules {
		if s.ID == id {
			found = true
			continue
		}
		newSchedules = append(newSchedules, s)
	}
	if !found {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}

	if err := saveReplicationSchedules(newSchedules); err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{"success": true})

	// GITOPS HOOK: write state back to git
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

	schedules, err := loadReplicationSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules", http.StatusInternalServerError)
		return
	}

	var target *ReplicationSchedule
	for i := range schedules {
		if schedules[i].ID == id {
			target = &schedules[i]
			break
		}
	}
	if target == nil {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}

	sched := *target // copy for goroutine
	jobID := launchReplicationJob(sched)

	// Update last run + job ID in persisted schedules
	for i := range schedules {
		if schedules[i].ID == id {
			now := time.Now()
			schedules[i].LastRun = &now
			schedules[i].LastStatus = "running"
			schedules[i].LastJobID = jobID
			break
		}
	}
	_ = saveReplicationSchedules(schedules)

	respondOK(w, map[string]interface{}{"success": true, "job_id": jobID})
}

// launchReplicationJob starts an async replication job and returns the job ID.
// It resolves the peer at runtime, verifies authorization and TCP reachability,
// then executes the ZFS send pipeline.
func launchReplicationJob(s ReplicationSchedule) string {
	return jobs.Start("replication_schedule", func(j *jobs.Job) {
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

		// Step 4: Find the newest snapshot for the source dataset
		snapOutput, snapErr := executeCommandWithTimeout(TimeoutFast, "zfs",
			[]string{"list", "-t", "snapshot", "-H", "-o", "name", "-s", "creation", "-r", s.SourceDataset})
		if snapErr != nil {
			j.Fail(fmt.Sprintf("Failed to list snapshots for %s: %v", s.SourceDataset, snapErr))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}

		lines := strings.Split(strings.TrimSpace(snapOutput), "\n")
		if len(lines) == 0 || lines[0] == "" {
			j.Fail(fmt.Sprintf("No snapshots found for dataset %s - create a snapshot first", s.SourceDataset))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}
		snapshot := strings.TrimSpace(lines[len(lines)-1])

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

		// Step 6: Check for a resume token from an interrupted prior transfer
		if s.Resume {
			token := getResumeToken(sshArgs, sshTarget, remoteDataset)
			if token != "" && isValidResumeToken(token) {
				j.Log("Found resume token - resuming interrupted transfer...")
				_, resumeErr := execPipedZFSSend(
					j,
					[]string{"send", "-V", "-t", token},
					sshArgs, sshTarget,
					[]string{"recv", "-s", "-F", remoteDataset},
					nil,
				)
				if resumeErr != nil {
					j.Log(fmt.Sprintf("Resume failed (%v) - falling through to full send", resumeErr))
				} else {
					j.Done(map[string]interface{}{
						"snapshot": snapshot,
						"remote":   fmt.Sprintf("%s:%s", sshTarget, remoteDataset),
						"peer":     remote.Name,
						"resumed":  true,
					})
					updateScheduleStatus(s.ID, "done", j.ID, snapshot)
					return
				}
			}
		}

		// Step 7: Build send args - incremental if a prior snapshot is recorded, otherwise full
		sendArgs := []string{"send", "-P"}
		if !s.NonRecursive {
			sendArgs = append(sendArgs, "-R")
		}
		if s.Compress {
			sendArgs = append(sendArgs, "-c")
		}
		if s.Incremental && s.LastReplicatedSnapshot != "" && isValidSnapshotName(s.LastReplicatedSnapshot) {
			// Verify the base snapshot still exists - it may have been pruned since last replication
			_, snapCheckErr := executeCommandWithTimeout(TimeoutFast, "zfs",
				[]string{"list", "-t", "snapshot", "-H", "-o", "name", s.LastReplicatedSnapshot})
			if snapCheckErr != nil {
				j.Log(fmt.Sprintf("WARN: incremental base %q no longer exists - falling back to full send", s.LastReplicatedSnapshot))
			} else {
				sendArgs = append(sendArgs, "-i", s.LastReplicatedSnapshot)
				j.Log(fmt.Sprintf("Incremental send: base=%s -> %s", s.LastReplicatedSnapshot, snapshot))
			}
		} else if s.Incremental {
			j.Log("Incremental requested but no prior snapshot recorded - sending full stream")
		}
		sendArgs = append(sendArgs, snapshot)

		var rateLimit []string
		if s.RateLimitMB > 0 {
			rateLimit = []string{fmt.Sprintf("%dM", s.RateLimitMB)}
		}

		j.Log(fmt.Sprintf("Replicating %s -> %s:%s", snapshot, sshTarget, remoteDataset))

		_, execErr := execPipedZFSSend(
			j,
			sendArgs,
			sshArgs, sshTarget,
			[]string{"recv", "-s", "-F", remoteDataset},
			rateLimit,
		)
		if execErr != nil {
			j.Fail(fmt.Sprintf("Replication failed: %v", execErr))
			updateScheduleStatus(s.ID, "failed", j.ID, "")
			return
		}

		j.Done(map[string]interface{}{
			"snapshot": snapshot,
			"remote":   fmt.Sprintf("%s:%s", sshTarget, remoteDataset),
			"peer":     remote.Name,
		})
		updateScheduleStatus(s.ID, "done", j.ID, snapshot)
	})
}

// updateScheduleStatus persists last status, job ID, and (on success) the replicated snapshot name.
func updateScheduleStatus(schedID, status, jobID, lastSnapshot string) {
	schedules, err := loadReplicationSchedules()
	if err != nil {
		log.Printf("WARN: replication_schedule: failed to load schedules for status update: %v", err)
		return
	}
	now := time.Now()
	for i := range schedules {
		if schedules[i].ID == schedID {
			schedules[i].LastRun = &now
			schedules[i].LastStatus = status
			schedules[i].LastJobID = jobID
			if lastSnapshot != "" {
				schedules[i].LastReplicatedSnapshot = lastSnapshot
			}
			break
		}
	}
	if err := saveReplicationSchedules(schedules); err != nil {
		log.Printf("WARN: replication_schedule: failed to save status update: %v", err)
	}

	// Broadcast schedule update to UI
	DispatchAlert("info", "replication.schedule_updated", schedID,
		fmt.Sprintf("Schedule %s status updated to %s", schedID, status))
}

// TriggerPostSnapshotReplication fires replication for all enabled schedules
// that have TriggerOnSnapshot=true and match the given dataset.
// Called after a snapshot is successfully created.
func TriggerPostSnapshotReplication(dataset string) {
	schedules, err := loadReplicationSchedules()
	if err != nil {
		log.Printf("WARN: TriggerPostSnapshotReplication: failed to load schedules: %v", err)
		return
	}

	for _, s := range schedules {
		if !s.Enabled || !s.TriggerOnSnapshot {
			continue
		}
		// Match exact dataset or a child dataset
		if s.SourceDataset != dataset && !strings.HasPrefix(dataset, s.SourceDataset+"/") {
			continue
		}
		// Skip if a job for this schedule is already running - don't pile up concurrent sends
		if s.LastStatus == "running" {
			log.Printf("INFO: TriggerPostSnapshotReplication: skipping schedule %s (already running job %s)", s.ID, s.LastJobID)
			continue
		}

		sched := s // copy
		jobID := launchReplicationJob(sched)
		log.Printf("INFO: post-snapshot replication triggered for dataset=%s schedule=%s job=%s", dataset, sched.ID, jobID)

		// Update schedule status
		for i := range schedules {
			if schedules[i].ID == sched.ID {
				now := time.Now()
				schedules[i].LastRun = &now
				schedules[i].LastStatus = "running"
				schedules[i].LastJobID = jobID
				break
			}
		}
	}

	// Persist the updated statuses (best-effort)
	if saveErr := saveReplicationSchedules(schedules); saveErr != nil {
		log.Printf("WARN: TriggerPostSnapshotReplication: failed to persist status: %v", saveErr)
	}
}

// ============================================================
// BACKGROUND MONITOR
// ============================================================

// StartReplicationMonitor starts a background ticker that checks replication
// schedules every 5 minutes and fires any that are due.
func StartReplicationMonitor() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			runDueReplicationSchedules()
		}
	}()
	log.Println("Replication schedule monitor started (checks every 5 min)")
}

func runDueReplicationSchedules() {
	schedules, err := loadReplicationSchedules()
	if err != nil {
		log.Printf("WARN: replication monitor: failed to load schedules: %v", err)
		return
	}

	now := time.Now()
	updated := false

	for i, s := range schedules {
		if !s.Enabled || s.Interval == "manual" {
			continue
		}

		if !isReplicationDue(s, now) {
			continue
		}

		sched := s // copy
		jobID := launchReplicationJob(sched)
		log.Printf("INFO: replication monitor: firing schedule id=%s dataset=%s interval=%s job=%s",
			sched.ID, sched.SourceDataset, sched.Interval, jobID)

		schedules[i].LastRun = &now
		schedules[i].LastStatus = "running"
		schedules[i].LastJobID = jobID
		updated = true
	}

	if updated {
		if err := saveReplicationSchedules(schedules); err != nil {
			log.Printf("WARN: replication monitor: failed to save after firing schedules: %v", err)
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

