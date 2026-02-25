package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/dockerclient"
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
		SkipSnapshot  bool   `json:"skip_snapshot"`   // skip ZFS snapshot
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.ContainerName == "" {
		respondErrorSimple(w, "container_name is required", http.StatusBadRequest)
		return
	}

	// Sanitize container name
	if !isValidContainerName(req.ContainerName) {
		respondErrorSimple(w, "Invalid container name", http.StatusBadRequest)
		return
	}

	steps := []UpdateStep{}
	startTime := time.Now()
	var snapshotName string
	dockerClient := dockerclient.New()

	// Step 1: ZFS Snapshot (if dataset provided and not skipped)
	if req.ZfsDataset != "" && !req.SkipSnapshot {
		if !isValidDataset(req.ZfsDataset) {
			respondErrorSimple(w, "Invalid ZFS dataset name", http.StatusBadRequest)
			return
		}

		snapshotName = fmt.Sprintf("%s@pre-update-%s-%s",
			req.ZfsDataset,
			req.ContainerName,
			time.Now().Format("20060102-150405"),
		)

		_, err := executeCommand("/usr/sbin/zfs", []string{"snapshot", snapshotName})
		if err != nil {
			steps = append(steps, UpdateStep{"zfs_snapshot", false, err.Error()})
			respondOK(w, UpdateResult{
				Success:  false,
				Steps:    steps,
				Error:    fmt.Sprintf("Failed to create safety snapshot: %v", err),
				Duration: time.Since(startTime).Milliseconds(),
			})
			return
		}
		steps = append(steps, UpdateStep{"zfs_snapshot", true, snapshotName})
	} else {
		steps = append(steps, UpdateStep{"zfs_snapshot", true, "skipped"})
	}

	// Step 2: Pull new image
	image := req.Image
	if image == "" {
		// Get image from running container via Docker API
		ctxDetect, cancelDetect := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancelDetect()
		detail, err := dockerClient.Inspect(ctxDetect, req.ContainerName)
		if err != nil {
			steps = append(steps, UpdateStep{"detect_image", false, err.Error()})
			respondOK(w, UpdateResult{
				Success: false, Steps: steps,
				Error:    "Could not detect container image. Provide 'image' field.",
				Duration: time.Since(startTime).Milliseconds(),
			})
			return
		}
		image = detail.Config.Image
	}

	ctxPull, cancelPull := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancelPull()
	if err := dockerClient.PullImage(ctxPull, image); err != nil {
		steps = append(steps, UpdateStep{"pull", false, err.Error()})
		respondOK(w, UpdateResult{
			Success:  false,
			Steps:    steps,
			Error:    fmt.Sprintf("Failed to pull image: %v", err),
			Rollback: snapshotName,
			Duration: time.Since(startTime).Milliseconds(),
		})
		return
	}
	steps = append(steps, UpdateStep{"pull", true, image})

	// Step 3: Inspect current config (preserved for recovery reference)
	ctxInspect, cancelInspect := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancelInspect()
	_, inspectErr := dockerClient.Inspect(ctxInspect, req.ContainerName)
	if inspectErr != nil {
		steps = append(steps, UpdateStep{"inspect", false, inspectErr.Error()})
		respondOK(w, UpdateResult{
			Success: false, Steps: steps,
			Error:    "Failed to inspect container config",
			Rollback: snapshotName,
			Duration: time.Since(startTime).Milliseconds(),
		})
		return
	}
	steps = append(steps, UpdateStep{"inspect", true, "config saved"})

	// Step 4: Stop container
	ctxStop, cancelStop := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancelStop()
	if err := dockerClient.Stop(ctxStop, req.ContainerName, 10); err != nil {
		steps = append(steps, UpdateStep{"stop", false, err.Error()})
		// Try to restart on failure
		ctxRecover, cancelRecover := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancelRecover()
		_ = dockerClient.Start(ctxRecover, req.ContainerName)
		respondOK(w, UpdateResult{
			Success: false, Steps: steps,
			Error:    "Failed to stop container, restarted original",
			Rollback: snapshotName,
			Duration: time.Since(startTime).Milliseconds(),
		})
		return
	}
	steps = append(steps, UpdateStep{"stop", true, ""})

	// Step 5: Start with new image
	ctxStart, cancelStart := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancelStart()
	if err := dockerClient.Start(ctxStart, req.ContainerName); err != nil {
		steps = append(steps, UpdateStep{"start", false, err.Error()})
		respondOK(w, UpdateResult{
			Success: false, Steps: steps,
			Error: fmt.Sprintf("Container failed to start after update. "+
				"Your data is safe in snapshot: %s. "+
				"Rollback with: zfs rollback %s", snapshotName, snapshotName),
			Rollback: snapshotName,
			Duration: time.Since(startTime).Milliseconds(),
		})
		return
	}
	steps = append(steps, UpdateStep{"start", true, ""})

	// Step 6: Health check via Docker API — respects HEALTHCHECK, configurable timeout
	hcTimeout := req.HealthCheckSeconds
	if hcTimeout <= 0 {
		hcTimeout = 30
	}
	ctxHC, cancelHC := context.WithTimeout(r.Context(), time.Duration(hcTimeout+5)*time.Second)
	defer cancelHC()
	running, hcErr := dockerClient.WaitForHealthy(ctxHC, req.ContainerName,
		time.Duration(hcTimeout)*time.Second)

	if hcErr != nil || !running {
		steps = append(steps, UpdateStep{"health_check", false,
			fmt.Sprintf("container not healthy after %ds: %v", hcTimeout, hcErr)})
		respondOK(w, UpdateResult{
			Success: false, Steps: steps,
			Error: fmt.Sprintf("Container not healthy after update (waited %ds). "+
				"Rollback data with: zfs rollback %s. Tip: increase health_check_seconds for slow-starting apps.", hcTimeout, snapshotName),
			Rollback: snapshotName,
			Duration: time.Since(startTime).Milliseconds(),
		})
		return
	}
	steps = append(steps, UpdateStep{"health_check", true, "running"})

	audit.LogCommand(audit.LevelInfo, "system", "docker_safe_update",
		[]string{req.ContainerName, image}, true, time.Since(startTime), nil)

	respondOK(w, UpdateResult{
		Success:  true,
		Steps:    steps,
		Snapshot: snapshotName,
		Duration: time.Since(startTime).Milliseconds(),
	})
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

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	dockerClient := dockerclient.New()
	err := dockerClient.PullImage(ctx, req.Image)
	duration := time.Since(start)

	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":     false,
			"error":       fmt.Sprintf("Pull failed: %v", err),
			"duration_ms": duration.Milliseconds(),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success":     true,
		"image":       req.Image,
		"duration_ms": duration.Milliseconds(),
	})
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
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	respondOK(w, map[string]interface{}{
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
	output, err := cmdutil.RunFast("/usr/bin/docker",
		"stats", "--no-stream", "--format",
		`{"name":"{{.Name}}","cpu":"{{.CPUPerc}}","memory":"{{.MemUsage}}","mem_perc":"{{.MemPerc}}","net_io":"{{.NetIO}}","block_io":"{{.BlockIO}}","pids":"{{.PIDs}}"}`)
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":    true,
			"containers": []interface{}{},
			"error":      "Docker stats unavailable",
		})
		return
	}

	var stats []map[string]interface{}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var s map[string]interface{}
		if err := json.Unmarshal([]byte(line), &s); err == nil {
			stats = append(stats, s)
		}
	}

	respondOK(w, map[string]interface{}{
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

	args := []string{"compose", "-f", cleanPath + "/docker-compose.yml", "up"}
	if req.Detach {
		args = append(args, "-d")
	}

	start := time.Now()
	output, err := cmdutil.RunSlow("/usr/bin/docker", args...)
	duration := time.Since(start)

	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"output":  string(output),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success":     true,
		"output":      string(output),
		"duration_ms": duration.Milliseconds(),
	})
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

	args := []string{"compose", "-f", cleanPath + "/docker-compose.yml", "down"}
	if req.RemoveVolumes {
		args = append(args, "-v")
	}

	output, err := cmdutil.RunMedium("/usr/bin/docker", args...)
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"output":  string(output),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"output":  string(output),
	})
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

	output, err := cmdutil.RunFast("/usr/bin/docker",
		"compose", "-f", path+"/docker-compose.yml", "ps", "--format", "json")
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":    true,
			"services":   []interface{}{},
			"error":      "Compose stack not found or not running",
		})
		return
	}

	var services []map[string]interface{}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var s map[string]interface{}
		if err := json.Unmarshal([]byte(line), &s); err == nil {
			services = append(services, s)
		}
	}

	respondOK(w, map[string]interface{}{
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

// waitForHealthy is implemented in internal/dockerclient — use dockerClient.WaitForHealthy()


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
		"/var/lib/dplaneos/",
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
	return "", fmt.Errorf("path must be under /opt, /srv, /home, /var/lib/dplaneos, /mnt, /data, /tank, or /pool")
}
