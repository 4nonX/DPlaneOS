package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/config"
	"dplaned/internal/gitops"
)

// ═══════════════════════════════════════════════════════════════
//  Docker Stack Management - D-PlaneOS YAML → Deploy workflow
// ═══════════════════════════════════════════════════════════════
//
//  Default stacks directory: /var/lib/dplaneos/stacks/
//  Each stack is a subdirectory containing docker-compose.yml.
//
//  Endpoints:
//    POST   /api/docker/stacks/deploy   - write YAML + compose up
//    GET    /api/docker/stacks          - list all stacks with status
//    GET    /api/docker/stacks/yaml     - read a stack's YAML
//    PUT    /api/docker/stacks/yaml     - update YAML + optional redeploy
//    DELETE /api/docker/stacks          - compose down + remove directory
//    POST   /api/docker/stacks/action   - start/stop/restart a stack

const defaultStacksDir = config.StacksDir

// validStackNameRe: lowercase alphanumeric, hyphens, underscores. No dots, no spaces.
var validStackNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// StackHandler manages docker compose stacks
type StackHandler struct {
	db *sql.DB
}

func NewStackHandler(db *sql.DB) *StackHandler {
	return &StackHandler{db: db}
}

// stackDir returns the validated directory for a stack name.
// Returns ("", error) if the name is invalid.
func stackDir(name string) (string, error) {
	if !validStackNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid stack name: must be lowercase alphanumeric with hyphens/underscores, 1-63 chars")
	}
	dir := filepath.Join(defaultStacksDir, name)
	// Double-check the path is under our stacks dir after Join
	clean := filepath.Clean(dir)
	if !strings.HasPrefix(clean, defaultStacksDir+"/") {
		return "", fmt.Errorf("path traversal detected")
	}
	return clean, nil
}

// ─────────────────────────────────────────────────────────────
//  POST /api/docker/stacks/deploy
//  Creates a stack directory, writes docker-compose.yml, runs compose up.
// ─────────────────────────────────────────────────────────────

func (h *StackHandler) DeployStack(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"` // stack name, becomes directory name
		YAML string `json:"yaml"` // docker-compose.yml content
		Env  string `json:"env"`  // .env file content (optional)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate name
	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	if req.Name == "" {
		respondErrorSimple(w, "Stack name is required", http.StatusBadRequest)
		return
	}

	dir, err := stackDir(req.Name)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Validate YAML has minimum structure
	yaml := strings.TrimSpace(req.YAML)
	if yaml == "" {
		respondErrorSimple(w, "YAML content is required", http.StatusBadRequest)
		return
	}
	if !strings.Contains(yaml, "services:") && !strings.Contains(yaml, "services :") {
		respondErrorSimple(w, "Invalid compose YAML: must contain 'services:' section", http.StatusBadRequest)
		return
	}

	user := getUserFromRequest(r)
	start := time.Now()

	// Ensure stacks base directory exists
	if err := os.MkdirAll(defaultStacksDir, 0750); err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create stacks directory: %v", err),
		})
		return
	}

	// Create stack directory
	if err := os.MkdirAll(dir, 0750); err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create stack directory: %v", err),
		})
		return
	}

	// Write docker-compose.yml
	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(yaml), 0640); err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to write compose file: %v", err),
		})
		return
	}

	// Write .env if provided
	if strings.TrimSpace(req.Env) != "" {
		envPath := filepath.Join(dir, ".env")
		if err := os.WriteFile(envPath, []byte(req.Env), 0640); err != nil {
			respondOK(w, map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Failed to write .env file: %v", err),
			})
			return
		}
	}

	// Run docker compose up -d
	output, composeErr := cmdutil.RunSlow("/usr/bin/docker",
		"compose", "--project-directory", dir, "-f", composePath, "up", "-d")
	duration := time.Since(start)

	audit.LogCommand(audit.LevelInfo, user, "stack_deploy",
		[]string{req.Name}, composeErr == nil, duration, composeErr)

	if composeErr != nil {
		respondOK(w, map[string]interface{}{
			"success":     false,
			"error":       fmt.Sprintf("Compose up failed: %v", composeErr),
			"output":      string(output),
			"stack":       req.Name,
			"duration_ms": duration.Milliseconds(),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success":     true,
		"message":     fmt.Sprintf("Stack '%s' deployed successfully", req.Name),
		"stack":       req.Name,
		"path":        dir,
		"output":      string(output),
		"duration_ms": duration.Milliseconds(),
	})

	// GITOPS HOOK: write state back to git
	go gitops.CommitAll(h.db)
}

// ─────────────────────────────────────────────────────────────
//  GET /api/docker/stacks
//  Lists all stacks with their compose status.
// ─────────────────────────────────────────────────────────────

type StackInfo struct {
	Name         string                   `json:"name"`
	Path         string                   `json:"path"`
	Status       string                   `json:"status"`   // "running", "partial", "stopped", "unknown"
	Services     []map[string]interface{} `json:"services"` // compose ps output
	FileSize     int64                    `json:"file_size"`
	CreatedAt    string                   `json:"created_at"`
	UpdatedAt    string                   `json:"updated_at"`
	TemplateID   string                   `json:"template_id,omitempty"`   // set if deployed from a template
	TemplateName string                   `json:"template_name,omitempty"` // human-readable template name
	Labels       map[string]string        `json:"labels,omitempty"`        // dplaneos.* labels from first container
}

func (h *StackHandler) ListStacks(w http.ResponseWriter, r *http.Request) {
	// Ensure directory exists
	if err := os.MkdirAll(defaultStacksDir, 0750); err != nil {
		respondOK(w, map[string]interface{}{
			"success": true,
			"stacks":  []interface{}{},
			"count":   0,
		})
		return
	}

	entries, err := os.ReadDir(defaultStacksDir)
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": true,
			"stacks":  []interface{}{},
			"count":   0,
		})
		return
	}

	stacks := []StackInfo{}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !validStackNameRe.MatchString(name) {
			continue
		}

		dir := filepath.Join(defaultStacksDir, name)
		composePath := filepath.Join(dir, "docker-compose.yml")

		// Check if compose file exists
		fileInfo, err := os.Stat(composePath)
		if err != nil {
			continue // skip directories without docker-compose.yml
		}

		stack := StackInfo{
			Name:     name,
			Path:     dir,
			FileSize: fileInfo.Size(),
		}

		// Read template marker if present
		markerPath := filepath.Join(dir, ".dplane-template")
		if markerData, err := os.ReadFile(markerPath); err == nil {
			var m map[string]string
			if json.Unmarshal(markerData, &m) == nil {
				stack.TemplateID = m["template_id"]
				stack.TemplateName = m["template_name"]
			}
		}

		// Get timestamps
		if dirInfo, err := entry.Info(); err == nil {
			stack.CreatedAt = dirInfo.ModTime().UTC().Format(time.RFC3339)
		}
		stack.UpdatedAt = fileInfo.ModTime().UTC().Format(time.RFC3339)

		// Get compose status (best effort, don't block on failures)
		output, composeErr := cmdutil.RunFast("/usr/bin/docker",
			"compose", "--project-directory", dir, "-f", composePath,
			"ps", "--format", "json")

		if composeErr != nil {
			stack.Status = "stopped"
			stack.Services = []map[string]interface{}{}
		} else {
			services := parseComposePS(output)
			stack.Services = services
			stack.Status = computeStackStatus(services)
		}

		// Extract dplaneos.icon label from the first running container in this
		// stack so ModulesPage can resolve a custom icon without needing per-container data.
		// Uses `docker ps --filter label=com.docker.compose.project=<name>` to find
		// containers belonging to this stack.
		labelOut, labelErr := cmdutil.RunFast("/usr/bin/docker", "ps",
			"--filter", "label=com.docker.compose.project="+name,
			"--format", "{{.Label \"dplaneos.icon\"}}",
			"--no-trunc")
		if labelErr == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(labelOut)), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					if stack.Labels == nil {
						stack.Labels = map[string]string{}
					}
					stack.Labels["dplaneos.icon"] = line
					break // use first non-empty value found
				}
			}
		}

		stacks = append(stacks, stack)
	}

	// Sort alphabetically
	sort.Slice(stacks, func(i, j int) bool { return stacks[i].Name < stacks[j].Name })

	respondOK(w, map[string]interface{}{
		"success":    true,
		"stacks":     stacks,
		"count":      len(stacks),
		"stacks_dir": defaultStacksDir,
	})
}

// ─────────────────────────────────────────────────────────────
//  GET /api/docker/stacks/yaml?name=mystack
//  Returns the raw YAML content of a stack's compose file.
// ─────────────────────────────────────────────────────────────

func (h *StackHandler) GetStackYAML(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
	dir, err := stackDir(name)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	composePath := filepath.Join(dir, "docker-compose.yml")
	data, err := os.ReadFile(composePath)
	if err != nil {
		if os.IsNotExist(err) {
			respondErrorSimple(w, fmt.Sprintf("Stack '%s' not found", name), http.StatusNotFound)
		} else {
			respondOK(w, map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Failed to read compose file: %v", err),
			})
		}
		return
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"name":    name,
		"yaml":    string(data),
		"env":     readEnvFile(dir),
		"path":    composePath,
	})
}

// ─────────────────────────────────────────────────────────────
//  PUT /api/docker/stacks/yaml
//  Updates the YAML and optionally redeploys.
// ─────────────────────────────────────────────────────────────

func (h *StackHandler) UpdateStackYAML(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		YAML     string `json:"yaml"`
		Env      string `json:"env"`      // .env content (optional)
		Redeploy bool   `json:"redeploy"` // if true, runs compose up after writing
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	dir, err := stackDir(req.Name)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	yaml := strings.TrimSpace(req.YAML)
	if yaml == "" {
		respondErrorSimple(w, "YAML content is required", http.StatusBadRequest)
		return
	}

	composePath := filepath.Join(dir, "docker-compose.yml")

	// Verify stack exists
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		respondErrorSimple(w, fmt.Sprintf("Stack '%s' not found", req.Name), http.StatusNotFound)
		return
	}

	// Write updated YAML
	if err := os.WriteFile(composePath, []byte(yaml), 0640); err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to write compose file: %v", err),
		})
		return
	}

	// Write .env if provided (empty string = delete .env)
	envPath := filepath.Join(dir, ".env")
	if req.Env != "" {
		if err := os.WriteFile(envPath, []byte(req.Env), 0640); err != nil {
			respondOK(w, map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Failed to write .env file: %v", err),
			})
			return
		}
	}

	user := getUserFromRequest(r)
	result := map[string]interface{}{
		"success": true,
		"name":    req.Name,
		"message": "YAML updated",
	}

	// Optionally redeploy
	if req.Redeploy {
		start := time.Now()
		output, composeErr := cmdutil.RunSlow("/usr/bin/docker",
			"compose", "--project-directory", dir, "-f", composePath, "up", "-d")
		duration := time.Since(start)

		audit.LogCommand(audit.LevelInfo, user, "stack_redeploy",
			[]string{req.Name}, composeErr == nil, duration, composeErr)

		result["redeployed"] = composeErr == nil
		result["output"] = string(output)
		result["duration_ms"] = duration.Milliseconds()
		if composeErr != nil {
			result["redeploy_error"] = composeErr.Error()
		} else {
			result["message"] = "YAML updated and stack redeployed"
		}
	}

	respondOK(w, result)

	// GITOPS HOOK: write state back to git
	go gitops.CommitAll(h.db)
}

// ─────────────────────────────────────────────────────────────
//  DELETE /api/docker/stacks?name=mystack
//  Runs compose down and removes the stack directory.
// ─────────────────────────────────────────────────────────────

func (h *StackHandler) DeleteStack(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
	dir, err := stackDir(name)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	composePath := filepath.Join(dir, "docker-compose.yml")

	// Verify stack exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		respondErrorSimple(w, fmt.Sprintf("Stack '%s' not found", name), http.StatusNotFound)
		return
	}

	user := getUserFromRequest(r)
	start := time.Now()

	// Run compose down first (best effort - stack might already be stopped)
	if _, err := os.Stat(composePath); err == nil {
		output, downErr := cmdutil.RunMedium("/usr/bin/docker",
			"compose", "--project-directory", dir, "-f", composePath, "down")
		if downErr != nil {
			// Log but don't fail - user may still want the directory removed
			audit.LogCommand(audit.LevelWarn, user, "stack_down",
				[]string{name}, false, time.Since(start), downErr)
			_ = output // consumed
		}
	}

	// Remove the stack directory
	if err := os.RemoveAll(dir); err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Compose down succeeded but failed to remove directory: %v", err),
		})
		return
	}

	duration := time.Since(start)
	audit.LogCommand(audit.LevelInfo, user, "stack_delete",
		[]string{name}, true, duration, nil)

	respondOK(w, map[string]interface{}{
		"success":     true,
		"message":     fmt.Sprintf("Stack '%s' removed", name),
		"duration_ms": duration.Milliseconds(),
	})

	// GITOPS HOOK: write state back to git
	go gitops.CommitAll(h.db)
}

// ─────────────────────────────────────────────────────────────
//  POST /api/docker/stacks/action
//  Start, stop, or restart a stack via compose.
// ─────────────────────────────────────────────────────────────

func (h *StackHandler) StackAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Action string `json:"action"` // "start", "stop", "restart"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	dir, err := stackDir(req.Name)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	validActions := map[string]bool{"start": true, "stop": true, "restart": true, "down": true}
	if !validActions[req.Action] {
		respondErrorSimple(w, "Invalid action: must be start, stop, restart, or down", http.StatusBadRequest)
		return
	}

	composePath := filepath.Join(dir, "docker-compose.yml")
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		respondErrorSimple(w, fmt.Sprintf("Stack '%s' not found", req.Name), http.StatusNotFound)
		return
	}

	user := getUserFromRequest(r)
	start := time.Now()

	var args []string
	switch req.Action {
	case "start":
		args = []string{"compose", "--project-directory", dir, "-f", composePath, "up", "-d", "--remove-orphans"}
	case "stop":
		args = []string{"compose", "--project-directory", dir, "-f", composePath, "stop"}
	case "restart":
		args = []string{"compose", "--project-directory", dir, "-f", composePath, "restart"}
	case "down":
		args = []string{"compose", "--project-directory", dir, "-f", composePath, "down", "--remove-orphans"}
	}

	output, composeErr := cmdutil.RunSlow("/usr/bin/docker", args...)
	duration := time.Since(start)

	audit.LogCommand(audit.LevelInfo, user, "stack_"+req.Action,
		[]string{req.Name}, composeErr == nil, duration, composeErr)

	if composeErr != nil {
		respondOK(w, map[string]interface{}{
			"success":     false,
			"error":       composeErr.Error(),
			"output":      string(output),
			"duration_ms": duration.Milliseconds(),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success":     true,
		"message":     fmt.Sprintf("Stack '%s' %sed", req.Name, req.Action),
		"output":      string(output),
		"duration_ms": duration.Milliseconds(),
	})

	// GITOPS HOOK: write state back to git
	go gitops.CommitAll(h.db)
}

// ─────────────────────────────────────────────────────────────
//  Helpers
// ─────────────────────────────────────────────────────────────

func parseComposePS(output []byte) []map[string]interface{} {
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
	if services == nil {
		return []map[string]interface{}{}
	}
	return services
}

func computeStackStatus(services []map[string]interface{}) string {
	if len(services) == 0 {
		return "stopped"
	}

	running := 0
	for _, s := range services {
		state, _ := s["State"].(string)
		if state == "running" {
			running++
		}
	}

	switch {
	case running == len(services):
		return "running"
	case running > 0:
		return "partial"
	default:
		return "stopped"
	}
}

// getUserFromRequest is defined in auth.go - reused here for cookie-based auth

// Ensure stacks directory exists and is writable.
// Called once at daemon startup.
func EnsureStacksDir() error {
	if err := os.MkdirAll(defaultStacksDir, 0750); err != nil {
		return fmt.Errorf("stacks dir: %w", err)
	}
	// Write probe to verify the directory is writable
	probe := filepath.Join(defaultStacksDir, ".probe")
	if err := os.WriteFile(probe, []byte("ok"), 0640); err != nil {
		return fmt.Errorf("stacks dir not writable: %w", err)
	}
	_ = os.Remove(probe)
	return nil
}

// readEnvFile reads the .env file from a stack directory (best effort)
func readEnvFile(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		return ""
	}
	return string(data)
}

// ─────────────────────────────────────────────────────────────
//  POST /api/docker/convert-run
//  Converts a "docker run ..." command to a compose.yaml snippet.
//  This is a best-effort client-side-assisted conversion.
// ─────────────────────────────────────────────────────────────

func (h *StackHandler) ConvertDockerRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string `json:"command"` // e.g. "docker run -d -p 8080:80 -v /data:/app --name myapp nginx:latest"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	cmd := strings.TrimSpace(req.Command)
	if cmd == "" {
		respondErrorSimple(w, "Command is required", http.StatusBadRequest)
		return
	}

	// Remove "docker run" prefix
	cmd = strings.TrimPrefix(cmd, "docker run ")
	cmd = strings.TrimPrefix(cmd, "docker run")
	cmd = strings.TrimSpace(cmd)

	// Parse into compose YAML
	yaml, name := parseDockerRun(cmd)

	respondOK(w, map[string]interface{}{
		"success": true,
		"yaml":    yaml,
		"name":    name,
	})
}

// parseDockerRun converts a docker run command string into a compose YAML.
// Best-effort parser covering common flags.
func parseDockerRun(cmd string) (string, string) {
	var (
		image     string
		name      = "app"
		ports     []string
		volumes   []string
		envVars   []string
		restart   string
		detach    bool
		networks  []string
		extraArgs []string
	)

	args := shellSplit(cmd)
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-d" || a == "--detach":
			detach = true
			_ = detach
		case (a == "-p" || a == "--publish") && i+1 < len(args):
			i++
			ports = append(ports, args[i])
		case (a == "-v" || a == "--volume") && i+1 < len(args):
			i++
			volumes = append(volumes, args[i])
		case (a == "-e" || a == "--env") && i+1 < len(args):
			i++
			envVars = append(envVars, args[i])
		case (a == "--name") && i+1 < len(args):
			i++
			name = args[i]
		case (a == "--restart") && i+1 < len(args):
			i++
			restart = args[i]
		case strings.HasPrefix(a, "--restart="):
			restart = strings.TrimPrefix(a, "--restart=")
		case strings.HasPrefix(a, "--name="):
			name = strings.TrimPrefix(a, "--name=")
		case strings.HasPrefix(a, "-p"):
			ports = append(ports, strings.TrimPrefix(a, "-p"))
		case strings.HasPrefix(a, "-v"):
			volumes = append(volumes, strings.TrimPrefix(a, "-v"))
		case strings.HasPrefix(a, "-e"):
			envVars = append(envVars, strings.TrimPrefix(a, "-e"))
		case (a == "--network" || a == "--net") && i+1 < len(args):
			i++
			networks = append(networks, args[i])
		case strings.HasPrefix(a, "--network="):
			networks = append(networks, strings.TrimPrefix(a, "--network="))
		case strings.HasPrefix(a, "-"):
			extraArgs = append(extraArgs, a)
			_ = extraArgs
		default:
			// Last positional arg = image
			image = a
		}
		i++
	}

	if image == "" {
		image = "IMAGE_HERE"
	}
	if restart == "" {
		restart = "unless-stopped"
	}

	// Build YAML
	var b strings.Builder
	b.WriteString("services:\n")
	b.WriteString("  " + sanitizeServiceName(name) + ":\n")
	b.WriteString("    image: " + image + "\n")
	if name != "" {
		b.WriteString("    container_name: " + name + "\n")
	}
	b.WriteString("    restart: " + restart + "\n")
	if len(ports) > 0 {
		b.WriteString("    ports:\n")
		for _, p := range ports {
			b.WriteString("      - \"" + p + "\"\n")
		}
	}
	if len(volumes) > 0 {
		b.WriteString("    volumes:\n")
		for _, v := range volumes {
			b.WriteString("      - " + v + "\n")
		}
	}
	if len(envVars) > 0 {
		b.WriteString("    environment:\n")
		for _, e := range envVars {
			b.WriteString("      - " + e + "\n")
		}
	}
	if len(networks) > 0 {
		b.WriteString("    networks:\n")
		for _, n := range networks {
			b.WriteString("      - " + n + "\n")
		}
		b.WriteString("\nnetworks:\n")
		for _, n := range networks {
			b.WriteString("  " + n + ":\n    external: true\n")
		}
	}

	return b.String(), sanitizeServiceName(name)
}

func sanitizeServiceName(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9_-]`).ReplaceAllString(s, "-")
	if s == "" {
		return "app"
	}
	return s
}

// shellSplit splits a command string into arguments, respecting quotes.
func shellSplit(s string) []string {
	var args []string
	var current strings.Builder
	inSingle, inDouble, escaped := false, false, false
	for _, r := range s {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		switch {
		case r == '\\' && !inSingle:
			escaped = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case r == ' ' && !inSingle && !inDouble:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

