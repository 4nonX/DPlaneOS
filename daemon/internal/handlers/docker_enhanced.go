package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/composegpu"
	"dplaned/internal/config"
	"dplaned/internal/dockerclient"
	"dplaned/internal/jobs"
	"dplaned/internal/libzfs"
)

// ═══════════════════════════════════════════════════════════════
//  Docker Update with ZFS Snapshot (the killer feature)
// ═══════════════════════════════════════════════════════════════

// SafeUpdate performs: ZFS snapshot → docker pull → docker stop → docker rm → docker run → health check → rollback on failure
// POST /api/docker/update
func (h *DockerHandler) SafeUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ContainerName      string `json:"container_name"`
		Image              string `json:"image"`                // e.g. "lscr.io/linuxserver/plex:latest"
		ZfsDataset         string `json:"zfs_dataset"`          // e.g. "tank/docker"
		HealthCheckSeconds int    `json:"health_check_seconds"` // 0 = use default (30s) (optional, auto-detected)
		SkipSnapshot       bool   `json:"skip_snapshot"`        // skip ZFS snapshot
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.ContainerName == "" {
		respondErrorSimple(w, "container_name is required", http.StatusBadRequest)
		return
	}
	if !isValidContainerName(req.ContainerName) {
		respondErrorSimple(w, "Invalid container name", http.StatusBadRequest)
		return
	}
	if req.ZfsDataset != "" && !req.SkipSnapshot && !isValidDataset(req.ZfsDataset) {
		respondErrorSimple(w, "Invalid ZFS dataset name", http.StatusBadRequest)
		return
	}

	ctxProj, cancelProj := context.WithTimeout(r.Context(), 10*time.Second)
	project, err := dockerclient.New().ComposeProject(ctxProj, req.ContainerName)
	cancelProj()
	if err != nil {
		respondErrorSimple(w, "Container not found: "+err.Error(), http.StatusNotFound)
		return
	}
	if project != "" {
		respondErrorSimple(w, fmt.Sprintf("%s belongs to the compose stack %q: update it with its stack (Docker → Compose → Update)", req.ContainerName, project), http.StatusConflict)
		return
	}

	id := jobs.Start("docker_safe_update", func(j *jobs.Job) {
		steps := []UpdateStep{}
		startTime := time.Now()
		var snapshotName string
		dockerClient := dockerclient.New()

		// Step 1: ZFS Snapshot (if dataset provided and not skipped)
		if req.ZfsDataset != "" && !req.SkipSnapshot {
			snapshotName = fmt.Sprintf("%s@pre-update-%s-%s",
				req.ZfsDataset,
				req.ContainerName,
				time.Now().Format("20060102-150405"),
			)
			err := libzfs.SnapshotCreate(snapshotName)
			if err != nil {
				j.Fail(fmt.Sprintf("Failed to create safety snapshot: %v", err))
				return
			}
			steps = append(steps, UpdateStep{"zfs_snapshot", true, snapshotName})
		} else {
			steps = append(steps, UpdateStep{"zfs_snapshot", true, "skipped"})
		}

		// Step 2: Pull new image
		image := req.Image
		if image == "" {
			ctxDetect, cancelDetect := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelDetect()
			detail, err := dockerClient.Inspect(ctxDetect, req.ContainerName)
			if err != nil {
				j.Fail("Could not detect container image. Provide 'image' field.")
				return
			}
			image = detail.Config.Image
		}

		ctxPull, cancelPull := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancelPull()
		if err := dockerClient.PullImage(ctxPull, image); err != nil {
			steps = append(steps, UpdateStep{"pull", false, err.Error()})
			j.Done(map[string]any{
				"success":     false,
				"steps":       steps,
				"error":       fmt.Sprintf("Failed to pull image: %v", err),
				"rollback":    snapshotName,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
			return
		}
		steps = append(steps, UpdateStep{"pull", true, image})

		// Step 3: Inspect
		ctxInspect, cancelInspect := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelInspect()
		if _, err := dockerClient.Inspect(ctxInspect, req.ContainerName); err != nil {
			steps = append(steps, UpdateStep{"inspect", false, err.Error()})
			j.Done(map[string]any{
				"success":     false,
				"steps":       steps,
				"error":       "Failed to inspect container config",
				"rollback":    snapshotName,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
			return
		}
		steps = append(steps, UpdateStep{"inspect", true, "config saved"})

		// Step 4: Stop the old container
		ctxStop, cancelStop := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelStop()
		if err := dockerClient.Stop(ctxStop, req.ContainerName, 10); err != nil {
			steps = append(steps, UpdateStep{"stop", false, err.Error()})
			_ = dockerClient.Start(context.Background(), req.ContainerName)
			j.Done(map[string]any{
				"success":     false,
				"steps":       steps,
				"error":       "Failed to stop container; it was left running on the old image",
				"rollback":    snapshotName,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
			return
		}
		steps = append(steps, UpdateStep{"stop", true, ""})

		// Step 5: Recreate from the new image with the same configuration (a
		// restarted container keeps running the image it was created from).
		// The old container is kept, renamed, until the new one is healthy.
		ctxNew, cancelNew := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelNew()
		rec, err := dockerClient.Recreate(ctxNew, req.ContainerName, image)
		fail := func(step, msg string) {
			steps = append(steps, UpdateStep{step, false, msg})
			detail := "the previous container was restored"
			if rec != nil {
				if rbErr := dockerClient.Rollback(context.Background(), rec); rbErr != nil {
					detail = "restoring the previous container failed: " + rbErr.Error()
				}
			} else if err := dockerClient.Start(context.Background(), req.ContainerName); err != nil {
				detail = "restarting the previous container failed: " + err.Error()
			}
			steps = append(steps, UpdateStep{"rollback", !strings.Contains(detail, "failed"), detail})
			j.Done(map[string]any{
				"success":     false,
				"steps":       steps,
				"error":       fmt.Sprintf("Update failed at %s: %s (%s)", step, msg, detail),
				"rollback":    snapshotName,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		if err != nil {
			fail("recreate", err.Error())
			return
		}
		steps = append(steps, UpdateStep{"recreate", true, image})

		// Step 6: Health check
		hcTimeout := req.HealthCheckSeconds
		if hcTimeout <= 0 {
			hcTimeout = 30
		}
		ctxHC, cancelHC := context.WithTimeout(context.Background(), time.Duration(hcTimeout+5)*time.Second)
		defer cancelHC()
		running, hcErr := dockerClient.WaitForHealthy(ctxHC, rec.NewID, time.Duration(hcTimeout)*time.Second)
		if hcErr != nil || !running {
			fail("health_check", fmt.Sprintf("not healthy after %ds: %v", hcTimeout, hcErr))
			return
		}
		if err := dockerClient.Commit(context.Background(), rec); err != nil {
			steps = append(steps, UpdateStep{"cleanup", false, "old container kept: " + err.Error()})
		}
		steps = append(steps, UpdateStep{"health_check", true, "running"})

		audit.LogCommand(audit.LevelInfo, "system", "docker_safe_update",
			[]string{req.ContainerName, image}, true, time.Since(startTime), nil)

		j.Done(map[string]any{
			"success":     true,
			"steps":       steps,
			"snapshot":    snapshotName,
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
	})

	respondOK(w, map[string]any{"job_id": id})
}

type UpdateStep struct {
	Step    string `json:"step"`
	Success bool   `json:"success"`
	Detail  string `json:"detail,omitempty"`
}

type UpdateResult struct {
	Success  bool         `json:"success"`
	Steps    []UpdateStep `json:"steps"`
	Error    string       `json:"error,omitempty"`
	Snapshot string       `json:"snapshot,omitempty"`
	Rollback string       `json:"rollback,omitempty"` // snapshot to rollback to
	Duration int64        `json:"duration_ms"`
}

// ═══════════════════════════════════════════════════════════════
//  Docker Pull
// ═══════════════════════════════════════════════════════════════

// PullImage pulls a Docker image
// POST /api/docker/pull { "image": "nginx:latest" }
func (h *DockerHandler) PullImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image string `json:"image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if req.Image == "" || len(req.Image) > 256 {
		respondErrorSimple(w, "Invalid image name", http.StatusBadRequest)
		return
	}

	image := req.Image
	id := jobs.Start("docker_pull", func(j *jobs.Job) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		dockerClient := dockerclient.New()
		err := dockerClient.PullImage(ctx, image)
		duration := time.Since(start)
		if err != nil {
			j.Fail(fmt.Sprintf("Pull failed: %v", err))
			return
		}
		j.Done(map[string]any{
			"image":       image,
			"duration_ms": duration.Milliseconds(),
		})
	})

	respondOK(w, map[string]any{"job_id": id})
}

// ═══════════════════════════════════════════════════════════════
//  Docker Remove
// ═══════════════════════════════════════════════════════════════

// RemoveContainer stops and removes a container
// POST /api/docker/remove { "container_name": "myapp", "force": true, "remove_volumes": false }
func (h *DockerHandler) RemoveContainer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ContainerName string `json:"container_name"`
		Force         bool   `json:"force"`
		RemoveVolumes bool   `json:"remove_volumes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if !isValidContainerName(req.ContainerName) {
		respondErrorSimple(w, "Invalid container name", http.StatusBadRequest)
		return
	}

	// args built by dockerclient.Remove

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	dockerClient := dockerclient.New()
	if err := dockerClient.Remove(ctx, req.ContainerName, req.Force, req.RemoveVolumes); err != nil {
		respondOK(w, map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	respondOK(w, map[string]any{
		"success": true,
		"message": fmt.Sprintf("Container %s removed", req.ContainerName),
	})
}

// ═══════════════════════════════════════════════════════════════
//  Docker Stats
// ═══════════════════════════════════════════════════════════════

// ContainerStats returns CPU, memory, network stats for all running containers
// GET /api/docker/stats
func (h *DockerHandler) ContainerStats(w http.ResponseWriter, r *http.Request) {
	output, err := cmdutil.RunFast("docker_cli",
		"stats", "--no-stream", "--format",
		`{"name":"{{.Name}}","cpu":"{{.CPUPerc}}","memory":"{{.MemUsage}}","mem_perc":"{{.MemPerc}}","net_io":"{{.NetIO}}","block_io":"{{.BlockIO}}","pids":"{{.PIDs}}"}`)
	if err != nil {
		respondOK(w, map[string]any{
			"success":    true,
			"containers": []any{},
			"error":      "Docker stats unavailable",
		})
		return
	}

	var stats []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		var s map[string]any
		if err := json.Unmarshal([]byte(line), &s); err == nil {
			stats = append(stats, s)
		}
	}

	respondOK(w, map[string]any{
		"success":    true,
		"containers": stats,
		"count":      len(stats),
	})
}

// ═══════════════════════════════════════════════════════════════
//  Docker Compose
// ═══════════════════════════════════════════════════════════════

// ComposeUp starts a docker-compose stack
// POST /api/docker/compose/up { "path": "/opt/stacks/plex", "detach": true }
func (h *DockerHandler) ComposeUp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`   // directory containing docker-compose.yml
		Detach bool   `json:"detach"` // -d flag
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	cleanPath, err := validateComposeDirPath(req.Path)
	if err != nil {
		respondErrorSimple(w, "Invalid path: "+err.Error(), http.StatusBadRequest)
		return
	}

	composeFile := filepath.Join(cleanPath, "docker-compose.yml")
	if raw, rerr := os.ReadFile(composeFile); rerr == nil {
		if err := composegpu.ValidateForDeploy(string(raw)); err != nil {
			respondErrorSimple(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	args := []string{"compose", "--project-directory", cleanPath, "-f", composeFile, "up"}
	if req.Detach {
		args = append(args, "-d")
	}

	id := jobs.Start("compose_up", func(j *jobs.Job) {
		start := time.Now()
		output, err := cmdutil.RunSlow("docker_compose", args...)
		duration := time.Since(start)
		if err != nil {
			j.Fail(fmt.Sprintf("%v\n%s", err, string(output)))
			return
		}
		j.Done(map[string]any{
			"output":      string(output),
			"duration_ms": duration.Milliseconds(),
		})
	})

	respondOK(w, map[string]any{"job_id": id})
}

// ComposeDown stops a docker-compose stack
// POST /api/docker/compose/down { "path": "/opt/stacks/plex" }
func (h *DockerHandler) ComposeDown(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path          string `json:"path"`
		RemoveVolumes bool   `json:"remove_volumes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	cleanPath, err := validateComposeDirPath(req.Path)
	if err != nil {
		respondErrorSimple(w, "Invalid path: "+err.Error(), http.StatusBadRequest)
		return
	}

	composeFile := filepath.Join(cleanPath, "docker-compose.yml")
	args := []string{"compose", "--project-directory", cleanPath, "-f", composeFile, "down"}
	if req.RemoveVolumes {
		args = append(args, "-v")
	}

	id := jobs.Start("compose_down", func(j *jobs.Job) {
		output, err := cmdutil.RunMedium("docker_compose", args...)
		if err != nil {
			j.Fail(fmt.Sprintf("%v\n%s", err, string(output)))
			return
		}
		j.Done(map[string]any{"output": string(output)})
	})

	respondOK(w, map[string]any{"job_id": id})
}

// ComposeStatus shows status of a docker-compose stack
// GET /api/docker/compose/status?path=/opt/stacks/plex
func (h *DockerHandler) ComposeStatus(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.Query().Get("path")
	path, pathErr := validateComposeDirPath(rawPath)
	if pathErr != nil {
		respondErrorSimple(w, "Invalid path: "+pathErr.Error(), http.StatusBadRequest)
		return
	}

	output, err := cmdutil.RunFast("docker_compose",
		"compose", "-f", path+"/docker-compose.yml", "ps", "--format", "json")
	if err != nil {
		respondOK(w, map[string]any{
			"success":  true,
			"services": []any{},
			"error":    "Compose stack not found or not running",
		})
		return
	}

	var services []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		var s map[string]any
		if err := json.Unmarshal([]byte(line), &s); err == nil {
			services = append(services, s)
		}
	}

	respondOK(w, map[string]any{
		"success":  true,
		"services": services,
		"count":    len(services),
	})
}

// ═══════════════════════════════════════════════════════════════
//  Helpers
// ═══════════════════════════════════════════════════════════════

var validContainerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.\-]*$`)

func isValidContainerName(name string) bool {
	return len(name) >= 1 && len(name) <= 128 && validContainerNameRe.MatchString(name)
}

// waitForHealthy is implemented in internal/dockerclient - use dockerClient.WaitForHealthy()

// validateComposeDirPath validates a directory path for docker compose operations.
// Rules:
//   - Must be an absolute path
//   - No null bytes
//   - After filepath.Clean, must still be absolute (no traversal to relative)
//   - Must be under one of the allowed base directories
//
// Allowed bases: /opt, /srv, /home, /var/lib/dplaneos/git-stacks, /mnt, /data, /tank, /pool
// This prevents arbitrary file access outside of typical Docker stack directories.
func validateComposeDirPath(p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("null byte in path")
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("must be an absolute path")
	}
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("path traversal detected")
	}

	allowedPrefixes := []string{
		"/opt/",
		"/srv/",
		"/home/",
		config.DBDir + "/",
		"/mnt/",
		"/data/",
		"/tank/",
		"/pool/",
	}
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(clean+"/", prefix) || clean == strings.TrimSuffix(prefix, "/") {
			return clean, nil
		}
	}
	return "", fmt.Errorf("path must be under /opt, /srv, /home, %s, /mnt, /data, /tank, or /pool", config.DBDir)
}
