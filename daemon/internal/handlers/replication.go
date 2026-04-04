package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/jobs"
	"dplaned/internal/security"
	"dplaned/internal/zfs"

	"time"
)

// ZFSSend enqueues a ZFS send operation and returns a job ID immediately.
// Poll GET /api/jobs/{id} for status.
func ZFSSend(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Snapshot string `json:"snapshot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := security.ValidateSnapshotName(req.Snapshot); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	snapshot := req.Snapshot
	id := jobs.Start("zfs_send", func(j *jobs.Job) {
		err := runZFSSendWithProgress(j, []string{"send", "-P", "-R", snapshot})
		audit.LogActivity(user, "zfs_send", map[string]interface{}{
			"snapshot": snapshot,
			"success":  err == nil,
		})
		if err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done(map[string]interface{}{
			"note": "Stream completed (stdout discarded). Use replication/remote to send to a peer with recv.",
		})
	})

	respondOK(w, map[string]interface{}{"job_id": id})
}

// ZFSSendIncremental enqueues an incremental ZFS send and returns a job ID.
func ZFSSendIncremental(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		BaseSnapshot string `json:"base_snapshot"`
		NewSnapshot  string `json:"new_snapshot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := security.ValidateSnapshotName(req.BaseSnapshot); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := security.ValidateSnapshotName(req.NewSnapshot); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	base, newsnap := req.BaseSnapshot, req.NewSnapshot
	id := jobs.Start("zfs_send_incremental", func(j *jobs.Job) {
		err := runZFSSendWithProgress(j, []string{"send", "-P", "-R", "-i", base, newsnap})
		audit.LogActivity(user, "zfs_send_incremental", map[string]interface{}{
			"base_snapshot": base,
			"new_snapshot":  newsnap,
			"success":       err == nil,
		})
		if err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done(map[string]interface{}{
			"note": "Stream completed (stdout discarded). Use replication/remote for remote recv.",
		})
	})

	respondOK(w, map[string]interface{}{"job_id": id})
}

// ZFSReceive enqueues a ZFS receive and returns a job ID.
func ZFSReceive(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Dataset string `json:"dataset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := security.ValidateDatasetName(req.Dataset); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid dataset name", err)
		return
	}

	dataset := req.Dataset
	id := jobs.Start("zfs_receive", func(j *jobs.Job) {
		output, err := cmdutil.RunSlow("zfs", "receive", "-F", dataset)
		audit.LogActivity(user, "zfs_receive", map[string]interface{}{
			"dataset": dataset,
			"success": err == nil,
		})
		if err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done(map[string]interface{}{"output": string(output)})
	})

	respondOK(w, map[string]interface{}{"job_id": id})
}

// runZFSSendWithProgress runs zfs with sendArgs (must include -P). Stdout is discarded
// so large sends do not buffer in memory; stderr feeds job progress for UI/poll.
func runZFSSendWithProgress(j *jobs.Job, sendArgs []string) error {
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "zfs", sendArgs...)
	cmd.Stdout = io.Discard
	rpipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(rpipe)
	var st zfs.SendProgressState
	for sc.Scan() {
		if up, ok := zfs.FeedSendProgressLine(sc.Text(), &st, 500*time.Millisecond); ok {
			j.Progress(up)
		}
	}
	_ = rpipe.Close()
	return cmd.Wait()
}
