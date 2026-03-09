package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"dplaned/internal/jobs"
	"github.com/gorilla/mux"
)

// ============================================================
// REPLICATION SCHEDULES
// ============================================================

// ReplicationSchedule defines when and how to trigger replication.
type ReplicationSchedule struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	SourceDataset     string     `json:"source_dataset"`
	RemoteID          string     `json:"remote_id"`           // references replication remote config
	RemoteHost        string     `json:"remote_host"`         // direct host (when not using RemoteID)
	RemoteUser        string     `json:"remote_user"`         // SSH user, default "root"
	RemotePort        int        `json:"remote_port"`         // SSH port, default 22
	RemotePool        string     `json:"remote_pool"`         // destination pool on remote
	SSHKeyPath        string     `json:"ssh_key_path"`        // path to SSH private key
	Interval          string     `json:"interval"`            // "hourly","daily","weekly","manual"
	TriggerOnSnapshot bool       `json:"trigger_on_snapshot"` // replicate after each auto-snapshot
	Compress          bool       `json:"compress"`
	RateLimitMB       int        `json:"rate_limit_mb"`
	Enabled           bool       `json:"enabled"`
	LastRun           *time.Time `json:"last_run,omitempty"`
	LastStatus        string     `json:"last_status,omitempty"`
	LastJobID         string     `json:"last_job_id,omitempty"`
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
func HandleListReplicationSchedules(w http.ResponseWriter, r *http.Request) {
	schedules, err := loadReplicationSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "schedules": schedules})
}

// HandleCreateReplicationSchedule serves POST /api/replication/schedules
func HandleCreateReplicationSchedule(w http.ResponseWriter, r *http.Request) {
	var s ReplicationSchedule
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate
	if !isValidDataset(s.SourceDataset) {
		respondErrorSimple(w, "Invalid source_dataset", http.StatusBadRequest)
		return
	}
	if s.Name == "" {
		respondErrorSimple(w, "name is required", http.StatusBadRequest)
		return
	}
	validIntervals := map[string]bool{"hourly": true, "daily": true, "weekly": true, "manual": true}
	if !validIntervals[s.Interval] {
		respondErrorSimple(w, "interval must be hourly, daily, weekly, or manual", http.StatusBadRequest)
		return
	}
	if s.RemoteUser == "" {
		s.RemoteUser = "root"
	}
	if s.RemotePort == 0 {
		s.RemotePort = 22
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
}

// HandleDeleteReplicationSchedule serves DELETE /api/replication/schedules/{id}
func HandleDeleteReplicationSchedule(w http.ResponseWriter, r *http.Request) {
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
}

// HandleRunReplicationScheduleNow serves POST /api/replication/schedules/{id}/run
func HandleRunReplicationScheduleNow(w http.ResponseWriter, r *http.Request) {
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
func launchReplicationJob(s ReplicationSchedule) string {
	return jobs.Start("replication_schedule", func(j *jobs.Job) {
		// Find the latest snapshot for the source dataset
		snapOutput, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zfs",
			[]string{"list", "-t", "snapshot", "-H", "-o", "name", "-s", "creation", "-r", s.SourceDataset})
		if err != nil {
			j.Fail(fmt.Sprintf("Failed to list snapshots for %s: %v", s.SourceDataset, err))
			updateScheduleStatus(s.ID, "failed", j.ID)
			return
		}

		lines := strings.Split(strings.TrimSpace(snapOutput), "\n")
		if len(lines) == 0 || lines[0] == "" {
			j.Fail(fmt.Sprintf("No snapshots found for dataset %s", s.SourceDataset))
			updateScheduleStatus(s.ID, "failed", j.ID)
			return
		}
		// Use the last (newest) snapshot
		snapshot := strings.TrimSpace(lines[len(lines)-1])

		host := s.RemoteHost
		user := s.RemoteUser
		if user == "" {
			user = "root"
		}
		port := s.RemotePort
		if port == 0 {
			port = 22
		}
		remotePool := s.RemotePool
		if remotePool == "" {
			remotePool = s.SourceDataset
		}

		// Validate before using in exec args
		if !isValidSnapshotName(snapshot) {
			j.Fail("Invalid snapshot name: " + snapshot)
			updateScheduleStatus(s.ID, "failed", j.ID)
			return
		}
		if !isValidDataset(remotePool) {
			j.Fail("Invalid remote pool: " + remotePool)
			updateScheduleStatus(s.ID, "failed", j.ID)
			return
		}
		if strings.ContainsAny(host, ";|&$`\\\"'") || host == "" {
			j.Fail("Invalid remote host")
			updateScheduleStatus(s.ID, "failed", j.ID)
			return
		}
		if !isValidSSHUser(user) {
			j.Fail("Invalid remote user")
			updateScheduleStatus(s.ID, "failed", j.ID)
			return
		}

		sshArgs := []string{
			"-o", "StrictHostKeyChecking=accept-new",
			"-o", "ConnectTimeout=10",
			"-o", "ServerAliveInterval=30",
			"-o", "ServerAliveCountMax=3",
			"-p", fmt.Sprintf("%d", port),
		}
		if s.SSHKeyPath != "" && !strings.ContainsAny(s.SSHKeyPath, ";|&$`\\\"'") {
			sshArgs = append(sshArgs, "-i", s.SSHKeyPath)
		}
		sshTarget := fmt.Sprintf("%s@%s", user, host)

		// Build remote dataset path
		snapParts := strings.SplitN(snapshot, "@", 2)
		datasetName := snapParts[0]
		parts := strings.Split(datasetName, "/")
		remoteDataset := remotePool + "/" + parts[len(parts)-1]

		sendArgs := []string{"send", "-R"}
		if s.Compress {
			sendArgs = append(sendArgs, "-c")
		}
		sendArgs = append(sendArgs, snapshot)

		var rateLimit []string
		if s.RateLimitMB > 0 {
			rateLimit = []string{fmt.Sprintf("%dM", s.RateLimitMB)}
		}

		_, execErr := execPipedZFSSend(
			sendArgs,
			sshArgs, sshTarget,
			[]string{"recv", "-s", "-F", remoteDataset},
			rateLimit,
		)
		if execErr != nil {
			j.Fail(fmt.Sprintf("Replication failed: %v", execErr))
			updateScheduleStatus(s.ID, "failed", j.ID)
			return
		}

		j.Done(map[string]interface{}{
			"snapshot": snapshot,
			"remote":   fmt.Sprintf("%s:%s", sshTarget, remoteDataset),
		})
		updateScheduleStatus(s.ID, "done", j.ID)
	})
}

// updateScheduleStatus persists last status and job ID for a schedule.
func updateScheduleStatus(schedID, status, jobID string) {
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
			break
		}
	}
	if err := saveReplicationSchedules(schedules); err != nil {
		log.Printf("WARN: replication_schedule: failed to save status update: %v", err)
	}
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
		return true // Never run — fire immediately
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
