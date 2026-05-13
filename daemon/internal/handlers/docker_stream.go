package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/composegpu"
	"dplaned/internal/security"

	"github.com/creack/pty"
)

// streamCompose runs a docker compose command and pipes combined stdout+stderr
// to an SSE response, one line per event. Returns the process exit error.
func streamCompose(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, dir string, args []string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir

	// Pipe writer that merges stdout + stderr
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		return err
	}

	// Close pipe writer when process exits so scanner sees EOF
	waitDone := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pw.Close()
		waitDone <- err
	}()

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "event: output\ndata: %s\n\n", line)
		flusher.Flush()

		// If client disconnected, stop reading
		select {
		case <-ctx.Done():
			cmd.Process.Kill() //nolint:errcheck
			pr.Close()
			return ctx.Err()
		default:
		}
	}
	pr.Close()
	return <-waitDone
}

func sendSSEMsg(w http.ResponseWriter, f http.Flusher, eventType, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
	f.Flush()
}

// StreamStackAction - GET /api/docker/stacks/stream?name=X&action=Y
// SSE endpoint: runs a compose action and streams output line-by-line.
// Valid actions: start, stop, restart, down, update
func (h *StackHandler) StreamStackAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
	action := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))

	if name == "" {
		respondErrorSimple(w, "Stack name required", http.StatusBadRequest)
		return
	}

	validActions := map[string]bool{"start": true, "stop": true, "restart": true, "down": true, "update": true}
	if !validActions[action] {
		respondErrorSimple(w, "Invalid action: must be start, stop, restart, down, or update", http.StatusBadRequest)
		return
	}

	dir, err := stackDir(name)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	composePath := filepath.Join(dir, "docker-compose.yml")
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		respondErrorSimple(w, fmt.Sprintf("Stack '%s' not found", name), http.StatusNotFound)
		return
	}

	if action == "start" || action == "update" {
		if raw, rerr := os.ReadFile(composePath); rerr == nil {
			if err := composegpu.ValidateForDeploy(string(raw)); err != nil {
				respondErrorSimple(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondErrorSimple(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	user := getUserFromRequest(r)
	startTime := time.Now()

	var runErr error
	switch action {
	case "start":
		runErr = streamCompose(ctx, w, flusher, dir, []string{
			"compose", "--project-directory", dir, "-f", composePath, "up", "-d", "--remove-orphans",
		})
	case "stop":
		runErr = streamCompose(ctx, w, flusher, dir, []string{
			"compose", "--project-directory", dir, "-f", composePath, "stop",
		})
	case "restart":
		runErr = streamCompose(ctx, w, flusher, dir, []string{
			"compose", "--project-directory", dir, "-f", composePath, "restart",
		})
	case "down":
		runErr = streamCompose(ctx, w, flusher, dir, []string{
			"compose", "--project-directory", dir, "-f", composePath, "down", "--remove-orphans",
		})
	case "update":
		sendSSEMsg(w, flusher, "output", "==> Pulling latest images...")
		if err := streamCompose(ctx, w, flusher, dir, []string{
			"compose", "--project-directory", dir, "-f", composePath, "pull",
		}); err != nil {
			runErr = err
			break
		}
		sendSSEMsg(w, flusher, "output", "==> Restarting services...")
		runErr = streamCompose(ctx, w, flusher, dir, []string{
			"compose", "--project-directory", dir, "-f", composePath, "up", "-d", "--remove-orphans",
		})
	}

	duration := time.Since(startTime)
	audit.LogCommand(audit.LevelInfo, user, "stack_stream_"+action,
		[]string{name}, runErr == nil, duration, runErr)

	if runErr != nil {
		sendSSEMsg(w, flusher, "error", runErr.Error())
	} else {
		sendSSEMsg(w, flusher, "done", "")
	}
}

// StreamStackLogs - GET /api/docker/stacks/logs/stream?name=X
// SSE: tails combined docker compose logs, stays open until client disconnects.
func (h *StackHandler) StreamStackLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
	if name == "" {
		respondErrorSimple(w, "Stack name required", http.StatusBadRequest)
		return
	}

	dir, err := stackDir(name)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	composePath := filepath.Join(dir, "docker-compose.yml")
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		respondErrorSimple(w, fmt.Sprintf("Stack '%s' not found", name), http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondErrorSimple(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker",
		"compose", "--project-directory", dir, "-f", composePath,
		"logs", "-f", "--tail", "100")
	cmd.Dir = dir

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(w, "event: error\ndata: Failed to start log stream: %v\n\n", err)
		flusher.Flush()
		return
	}

	go func() {
		cmd.Wait() //nolint:errcheck
		pw.Close()
	}()

	const maxLines = 10000
	count := 0
	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			pr.Close()
			return
		default:
		}
		line := scanner.Text()
		escaped := strings.ReplaceAll(line, "\n", " ")
		fmt.Fprintf(w, "event: log\ndata: %s\n\n", escaped)
		flusher.Flush()
		count++
		if count >= maxLines {
			pr.Close()
			return
		}
	}
}

// ServiceAction - POST /api/docker/stacks/services/action
// Start, stop, or restart a single service within a compose stack.
func (h *StackHandler) ServiceAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Stack   string `json:"stack"`
		Service string `json:"service"`
		Action  string `json:"action"` // "start", "stop", "restart"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	req.Stack = strings.ToLower(strings.TrimSpace(req.Stack))
	req.Service = strings.TrimSpace(req.Service)
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))

	if req.Stack == "" || req.Service == "" {
		respondErrorSimple(w, "Stack and service names required", http.StatusBadRequest)
		return
	}

	validActions := map[string]bool{"start": true, "stop": true, "restart": true}
	if !validActions[req.Action] {
		respondErrorSimple(w, "Invalid action: must be start, stop, or restart", http.StatusBadRequest)
		return
	}

	for _, c := range req.Service {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			respondErrorSimple(w, "Invalid service name", http.StatusBadRequest)
			return
		}
	}

	dir, err := stackDir(req.Stack)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	composePath := filepath.Join(dir, "docker-compose.yml")
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		respondErrorSimple(w, fmt.Sprintf("Stack '%s' not found", req.Stack), http.StatusNotFound)
		return
	}

	user := getUserFromRequest(r)
	started := time.Now()

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker",
		"compose", "--project-directory", dir, "-f", composePath,
		req.Action, req.Service)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	duration := time.Since(started)

	audit.LogCommand(audit.LevelInfo, user, "service_"+req.Action,
		[]string{req.Stack, req.Service}, err == nil, duration, err)

	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"output":  string(out),
			"error":   err.Error(),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"output":  string(out),
		"message": fmt.Sprintf("Service '%s' %sed", req.Service, req.Action),
	})
}

// ExecContainer - GET /ws/docker/exec?container=X&shell=bash
// WebSocket PTY: runs `docker exec -it <container> <shell>`.
// Auth is validated from the ?session= query param (WebSocket can't send headers).
func ExecContainer(w http.ResponseWriter, r *http.Request) {
	// Validate session from query param (WebSocket can't set custom headers)
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		sessionID = r.Header.Get("X-Session-ID")
	}
	if sessionID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if _, err := security.ValidateSessionAndGetUser(sessionID); err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	container := strings.TrimSpace(r.URL.Query().Get("container"))
	shell := strings.TrimSpace(r.URL.Query().Get("shell"))

	if container == "" {
		http.Error(w, "Container name required", http.StatusBadRequest)
		return
	}
	for _, c := range container {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			http.Error(w, "Invalid container name", http.StatusBadRequest)
			return
		}
	}
	if shell != "bash" && shell != "sh" {
		shell = "sh"
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	cmd := exec.Command("docker", "exec", "-it", container, shell)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		sendTermMsg(conn, "error", "Failed to exec into container: "+err.Error())
		return
	}
	defer func() {
		ptmx.Close()
		cmd.Process.Kill() //nolint:errcheck
		cmd.Wait()         //nolint:errcheck
	}()

	// PTY output → WebSocket
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				msg, _ := json.Marshal(termMsg{Type: "output", Data: string(buf[:n])})
				if err := conn.WriteMessage(1, msg); err != nil {
					return
				}
			}
			if err != nil {
				sendTermMsg(conn, "exit", "")
				return
			}
		}
	}()

	// WebSocket input → PTY
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg termMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "input":
			ptmx.Write([]byte(msg.Data)) //nolint:errcheck
		case "resize":
			cols, rows := msg.Cols, msg.Rows
			if cols == 0 {
				cols = 80
			}
			if rows == 0 {
				rows = 24
			}
			pty.Setsize(ptmx, &pty.Winsize{Cols: cols, Rows: rows}) //nolint:errcheck
		}
	}
}
