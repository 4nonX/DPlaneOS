package handlers

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"dplaned/internal/dockerclient"
)

// ContainerLogs returns recent logs for a container.
// GET /api/docker/logs?container=NAME&lines=100
func (h *DockerHandler) ContainerLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	containerName := r.URL.Query().Get("container")
	if containerName == "" {
		respondErrorSimple(w, "container parameter required", http.StatusBadRequest)
		return
	}

	validName := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	if !validName.MatchString(containerName) {
		respondErrorSimple(w, "Invalid container name", http.StatusBadRequest)
		return
	}

	lines := r.URL.Query().Get("lines")
	validLines := regexp.MustCompile(`^\d{1,5}$`)
	if !validLines.MatchString(lines) {
		lines = "200"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	logs, err := h.docker.Logs(ctx, containerName, dockerclient.LogOptions{
		Stdout: true,
		Stderr: true,
		Tail:   lines,
	})
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"logs":    "",
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"container": containerName,
		"logs":      logs,
	})
}
