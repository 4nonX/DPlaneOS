package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/dockerclient"
	"dplaned/internal/security"
)

type DockerHandler struct {
	docker *dockerclient.Client
}

type DockerImage struct {
	ID          string   `json:"id"`
	RepoTags    []string `json:"repo_tags"`
	Size        int64    `json:"size"`
	Created     int64    `json:"created"`
	Containers  int      `json:"containers"`
	VirtualSize int64    `json:"virtual_size"`
}

func NewDockerHandler() *DockerHandler {
	return &DockerHandler{
		docker: dockerclient.New(),
	}
}

// ListContainers returns all containers grouped by compose stack.
// GET /api/docker/containers
func (h *DockerHandler) ListContainers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// proceed
	default:
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if !security.IsValidSessionToken(sessionID) {
		audit.LogSecurityEvent("Invalid session token", user, r.RemoteAddr)
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	start := time.Now()
	containers, err := h.docker.ListAll(ctx)
	duration := time.Since(start)

	audit.LogCommand(audit.LevelInfo, user, "docker_ps", nil, err == nil, duration, err)

	if err != nil {
		respondOK(w, CommandResponse{
			Success:  false,
			Error:    err.Error(),
			Duration: duration.Milliseconds(),
		})
		return
	}

	raw := make([]map[string]interface{}, 0, len(containers))
	for _, c := range containers {
		raw = append(raw, containerToMap(c))
	}
	stacks := groupContainersByStack(containers)

	respondOK(w, map[string]interface{}{
		"success":          true,
		"data":             raw,
		"containers":       raw,
		"total_containers": len(raw),
		"stacks":           stacks,
		"total_stacks":     len(stacks),
		"duration_ms":      duration.Milliseconds(),
	})
}

// ContainerAction starts, stops, restarts, pauses or unpauses a container.
// POST /api/docker/action
func (h *DockerHandler) ContainerAction(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		// proceed
	default:
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Action      string `json:"action"`
		ContainerID string `json:"container_id"`
		SessionID   string `json:"session_id"`
		User        string `json:"user"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if !security.IsValidSessionToken(req.SessionID) {
		audit.LogSecurityEvent("Invalid session token", req.User, r.RemoteAddr)
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	validActions := map[string]bool{
		"start": true, "stop": true, "restart": true,
		"pause": true, "unpause": true,
	}
	if !validActions[req.Action] {
		respondErrorSimple(w, "Invalid action", http.StatusBadRequest)
		return
	}
	if len(req.ContainerID) < 3 || len(req.ContainerID) > 64 {
		respondErrorSimple(w, "Invalid container ID", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	start := time.Now()
	var err error

	switch req.Action {
	case "start":
		err = h.docker.Start(ctx, req.ContainerID)
	case "stop":
		err = h.docker.Stop(ctx, req.ContainerID, 10)
	case "restart":
		err = h.docker.Restart(ctx, req.ContainerID, 10)
	case "pause":
		err = h.docker.Pause(ctx, req.ContainerID)
	case "unpause":
		err = h.docker.Unpause(ctx, req.ContainerID)
	}

	duration := time.Since(start)

	audit.LogCommand(audit.LevelInfo, req.User, "docker_"+req.Action,
		[]string{req.ContainerID}, err == nil, duration, err)

	if err != nil {
		respondOK(w, CommandResponse{Success: false, Error: err.Error(), Duration: duration.Milliseconds()})
		return
	}
	respondOK(w, CommandResponse{Success: true, Duration: duration.Milliseconds()})
}

// ─────────────────────────────────────────────
//  Helpers
// ─────────────────────────────────────────────

func containerToMap(c dockerclient.Container) map[string]interface{} {
	// Build ports in both Docker API format (PascalCase) and frontend format (snake_case)
	ports := make([]map[string]interface{}, 0, len(c.Ports))
	for _, p := range c.Ports {
		ports = append(ports, map[string]interface{}{
			// Docker API PascalCase (kept for any tooling that reads raw data)
			"IP": p.IP, "PrivatePort": p.PrivatePort,
			"PublicPort": p.PublicPort, "Type": p.Type,
			// Frontend snake_case aliases
			"host_port":      p.PublicPort,
			"container_port": p.PrivatePort,
			"protocol":       p.Type,
		})
	}
	shortName := c.ShortName()
	return map[string]interface{}{
		// Docker API PascalCase originals
		"Id": c.ID, "Names": c.Names, "Image": c.Image,
		"ImageID": c.ImageID, "Command": c.Command,
		"Created": c.Created, "State": c.State, "Status": c.Status,
		"Ports": ports, "Labels": c.Labels,
		// Frontend-expected lowercase aliases (matches Container interface in DockerPage.tsx)
		"id":     c.ID,
		"name":   shortName,
		"image":  c.Image,
		"state":  c.State,
		"status": c.Status,
		"ports":  ports,
		"stack":  c.StackName(),
		// Convenience
		"Name":  shortName,
		"Stack": c.StackName(),
	}
}

func groupContainersByStack(containers []dockerclient.Container) []map[string]interface{} {
	type stackEntry struct {
		containers []map[string]interface{}
		originals  []dockerclient.Container
	}
	grouped := map[string]*stackEntry{}
	for _, c := range containers {
		name := c.StackName()
		if grouped[name] == nil {
			grouped[name] = &stackEntry{}
		}
		grouped[name].containers = append(grouped[name].containers, containerToMap(c))
		grouped[name].originals = append(grouped[name].originals, c)
	}
	stacks := make([]map[string]interface{}, 0, len(grouped))
	for name, entry := range grouped {
		running := 0
		totalPorts := 0
		for _, c := range entry.originals {
			if c.State == "running" {
				running++
			}
			totalPorts += len(c.Ports)
		}
		stacks = append(stacks, map[string]interface{}{
			"name":               name,
			"containers":         entry.containers,
			"count":              len(entry.containers),
			"total_containers":   len(entry.containers),
			"running_containers": running,
			"total_ports":        totalPorts,
		})
	}
	return stacks
}

// PruneDocker handles POST /api/docker/prune
// Removes stopped containers, dangling images, and unused volumes.
func (h *DockerHandler) PruneDocker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := r.Header.Get("X-User")
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	containers, images, volumes, spaceBytes, err := h.docker.PruneAll(ctx)
	if err != nil {
		audit.LogCommand(audit.LevelWarn, user, "docker_prune", nil, false, 0, err)
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	// Format space reclaimed
	spaceStr := ""
	if spaceBytes > 0 {
		mb := float64(spaceBytes) / (1024 * 1024)
		if mb >= 1024 {
			spaceStr = fmt.Sprintf("%.1f GB", mb/1024)
		} else {
			spaceStr = fmt.Sprintf("%.0f MB", mb)
		}
	}

	audit.LogCommand(audit.LevelInfo, user, "docker_prune", nil, true, 0, nil)
	respondOK(w, map[string]interface{}{
		"success":            true,
		"containers_removed": containers,
		"images_removed":     images,
		"volumes_removed":    volumes,
		"space_reclaimed":    spaceStr,
	})
}

// RemoveImage handles DELETE /api/docker/images/{id}
// and also POST /api/docker/image/remove { "id": "sha256:..." }
func (h *DockerHandler) RemoveImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Force bool   `json:"force"`
	}

	switch r.Method {
	case http.MethodPost:
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
			return
		}
	case http.MethodDelete:
		// Extract from URL if needed, but main.go uses POST for this often
		req.ID = r.URL.Query().Get("id")
	}

	if req.ID == "" {
		respondErrorSimple(w, "Image ID required", http.StatusBadRequest)
		return
	}

	user := r.Header.Get("X-User")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	start := time.Now()
	err := h.docker.RemoveImage(ctx, req.ID, req.Force)
	duration := time.Since(start)

	audit.LogCommand(audit.LevelInfo, user, "docker_rmi", []string{req.ID}, err == nil, duration, err)

	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	respondOK(w, map[string]interface{}{"success": true, "message": "Image removed"})
}

// ListImages handles GET /api/docker/images
func (h *DockerHandler) ListImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := r.Header.Get("X-User")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	start := time.Now()
	images, err := h.docker.ListImages(ctx)
	duration := time.Since(start)

	audit.LogCommand(audit.LevelInfo, user, "docker_images", nil, err == nil, duration, err)

	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	// Transform to frontend format
	res := make([]DockerImage, 0, len(images))
	for _, img := range images {
		res = append(res, DockerImage{
			ID:          img.ID,
			RepoTags:    img.RepoTags,
			Size:        img.Size,
			Created:     img.Created,
			VirtualSize: img.VirtualSize,
		})
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"images":  res,
		"total":   len(res),
	})
}
