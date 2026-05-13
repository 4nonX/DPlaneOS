package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/dockerclient"
	"github.com/gorilla/mux"
)

// InspectContainer returns a flattened container config suitable for the edit modal.
// GET /api/docker/containers/{id}/inspect
func (h *DockerHandler) InspectContainer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	containerID := mux.Vars(r)["id"]
	if containerID == "" {
		respondErrorSimple(w, "Container ID required", http.StatusBadRequest)
		return
	}

	user := r.Header.Get("X-User")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	detail, err := h.docker.Inspect(ctx, containerID)
	if err != nil {
		audit.LogCommand(audit.LevelWarn, user, "docker_inspect", []string{containerID}, false, 0, err)
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	// Parse PortBindings: map["80/tcp"] → []{HostIp, HostPort}
	type portEntry struct {
		HostPort      string `json:"host_port"`
		ContainerPort string `json:"container_port"`
		Protocol      string `json:"protocol"`
	}
	var ports []portEntry
	for key, raw := range detail.HostConfig.PortBindings {
		containerPort, protocol := key, "tcp"
		if parts := strings.SplitN(key, "/", 2); len(parts) == 2 {
			containerPort, protocol = parts[0], parts[1]
		}
		if raw == nil {
			continue
		}
		if arr, ok := raw.([]interface{}); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					hp, _ := m["HostPort"].(string)
					ports = append(ports, portEntry{hp, containerPort, protocol})
				}
			}
		}
	}

	// Parse Env into key/value pairs
	type envEntry struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	var envList []envEntry
	for _, e := range detail.Config.Env {
		if idx := strings.IndexByte(e, '='); idx < 0 {
			envList = append(envList, envEntry{Key: e})
		} else {
			envList = append(envList, envEntry{Key: e[:idx], Value: e[idx+1:]})
		}
	}

	// Parse Binds into host/container/mode
	type volumeEntry struct {
		HostPath      string `json:"host_path"`
		ContainerPath string `json:"container_path"`
		Mode          string `json:"mode"`
	}
	var volumes []volumeEntry
	for _, b := range detail.HostConfig.Binds {
		ve := volumeEntry{Mode: "rw"}
		switch parts := strings.SplitN(b, ":", 3); len(parts) {
		case 1:
			ve.ContainerPath = parts[0]
		case 2:
			ve.HostPath, ve.ContainerPath = parts[0], parts[1]
		case 3:
			ve.HostPath, ve.ContainerPath, ve.Mode = parts[0], parts[1], parts[2]
		}
		volumes = append(volumes, ve)
	}

	// Null-safe defaults for empty slices
	if ports == nil {
		ports = []portEntry{}
	}
	if envList == nil {
		envList = []envEntry{}
	}
	if volumes == nil {
		volumes = []volumeEntry{}
	}

	name := strings.TrimPrefix(detail.Name, "/")
	icon := detail.Config.Labels["dplaneos.icon"]

	respondOK(w, map[string]interface{}{
		"success":        true,
		"id":             detail.ID,
		"name":           name,
		"image":          detail.Config.Image,
		"icon":           icon,
		"restart_policy": detail.HostConfig.RestartPolicy.Name,
		"ports":          ports,
		"volumes":        volumes,
		"env":            envList,
		"state":          detail.State.Status,
	})
}

// ReconfigureContainer stops, removes, and recreates a container with updated settings.
// POST /api/docker/containers/{id}/reconfigure
func (h *DockerHandler) ReconfigureContainer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	containerID := mux.Vars(r)["id"]
	if containerID == "" {
		respondErrorSimple(w, "Container ID required", http.StatusBadRequest)
		return
	}

	var req struct {
		Icon          string `json:"icon"`
		RestartPolicy string `json:"restart_policy"`
		Ports []struct {
			HostPort      string `json:"host_port"`
			ContainerPort string `json:"container_port"`
			Protocol      string `json:"protocol"`
		} `json:"ports"`
		Volumes []struct {
			HostPath      string `json:"host_path"`
			ContainerPath string `json:"container_path"`
			Mode          string `json:"mode"`
		} `json:"volumes"`
		Env []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	user := r.Header.Get("X-User")
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// 1. Inspect to get current full config
	detail, err := h.docker.Inspect(ctx, containerID)
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": fmt.Sprintf("inspect: %v", err)})
		return
	}
	name := strings.TrimPrefix(detail.Name, "/")

	// 2. Carry over labels, apply icon change
	newLabels := make(map[string]string, len(detail.Config.Labels))
	for k, v := range detail.Config.Labels {
		newLabels[k] = v
	}
	if req.Icon != "" {
		newLabels["dplaneos.icon"] = req.Icon
	} else {
		delete(newLabels, "dplaneos.icon")
	}

	// 3. Build Env slice
	var newEnv []string
	for _, e := range req.Env {
		if e.Key != "" {
			newEnv = append(newEnv, e.Key+"="+e.Value)
		}
	}

	// 4. Build Binds
	var newBinds []string
	for _, v := range req.Volumes {
		if v.HostPath == "" && v.ContainerPath == "" {
			continue
		}
		if v.HostPath == "" {
			newBinds = append(newBinds, v.ContainerPath)
		} else {
			mode := v.Mode
			if mode == "" {
				mode = "rw"
			}
			newBinds = append(newBinds, v.HostPath+":"+v.ContainerPath+":"+mode)
		}
	}

	// 5. Build PortBindings
	portBindings := make(map[string][]dockerclient.PortBinding)
	for _, p := range req.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		key := p.ContainerPort + "/" + proto
		portBindings[key] = append(portBindings[key], dockerclient.PortBinding{HostPort: p.HostPort})
	}

	restartPolicy := req.RestartPolicy
	if restartPolicy == "" {
		restartPolicy = detail.HostConfig.RestartPolicy.Name
	}

	createCfg := dockerclient.CreateConfig{
		Image:  detail.Config.Image,
		Env:    newEnv,
		Labels: newLabels,
		HostConfig: dockerclient.CreateHostConfig{
			Binds:        newBinds,
			PortBindings: portBindings,
			RestartPolicy: dockerclient.RestartPolicySpec{
				Name: restartPolicy,
			},
		},
	}

	// 6. Stop if running
	wasRunning := detail.State.Running
	if wasRunning {
		if err := h.docker.Stop(ctx, containerID, 10); err != nil {
			respondOK(w, map[string]interface{}{"success": false, "error": fmt.Sprintf("stop: %v", err)})
			return
		}
	}

	// 7. Remove
	if err := h.docker.Remove(ctx, containerID, true, false); err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": fmt.Sprintf("remove: %v", err)})
		return
	}

	// 8. Recreate with new config
	newID, err := h.docker.Create(ctx, name, createCfg)
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": fmt.Sprintf("create: %v", err)})
		return
	}

	// 9. Start if was running
	if wasRunning {
		if err := h.docker.Start(ctx, newID); err != nil {
			respondOK(w, map[string]interface{}{"success": false, "error": fmt.Sprintf("start: %v", err)})
			return
		}
	}

	audit.LogCommand(audit.LevelInfo, user, "docker_reconfigure", []string{name}, true, 0, nil)

	respondOK(w, map[string]interface{}{
		"success": true,
		"id":      newID,
		"name":    name,
		"message": "Container reconfigured",
	})
}
