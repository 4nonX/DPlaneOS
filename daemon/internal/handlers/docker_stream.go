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
	"sync"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/composegpu"
	"dplaned/internal/security"

	"github.com/creack/pty"
)

// sseOut writes SSE events while the client is there; once it has gone the
// writes are dropped. The action itself goes on: a stack half-way through
// `compose up` must not be killed because a browser tab was closed.
type sseOut struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	f       http.Flusher
	clients context.Context // the request's context
}

func (o *sseOut) send(eventType, data string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.clients.Err() != nil {
		return
	}
	fmt.Fprintf(o.w, "event: %s\ndata: %s\n\n", eventType, strings.ReplaceAll(data, "\n", " "))
	o.f.Flush()
}

// streamCompose runs a docker compose command and sends its combined
// output as events. ctx bounds the command (not the client connection).
func streamCompose(ctx context.Context, out *sseOut, dir string, args []string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return err
	}
	waitDone := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pw.Close()
		waitDone <- err
	}()
	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		out.send("output", scanner.Text())
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

	unlock, ok := lockStack(name)
	if !ok {
		respondErrorSimple(w, fmt.Sprintf("Stack '%s' is busy with another operation", name), http.StatusConflict)
		return
	}
	defer unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	out := &sseOut{w: w, f: flusher, clients: r.Context()}

	// Bounded by the server, not by the connection (see sseOut).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	user := getUserFromRequest(r)
	startTime := time.Now()

	var runErr error
	switch action {
	case "start":
		runErr = streamCompose(ctx, out, dir, stackComposeArgs(name, dir, "up", "-d", "--remove-orphans"))
	case "stop":
		runErr = streamCompose(ctx, out, dir, stackComposeArgs(name, dir, "stop"))
	case "restart":
		runErr = streamCompose(ctx, out, dir, stackComposeArgs(name, dir, "restart"))
	case "down":
		runErr = streamCompose(ctx, out, dir, stackComposeArgs(name, dir, "down", "--remove-orphans"))
	case "update":
		// Pull, recreate, wait until every service is up; otherwise back
		// to the previous images (see updateStack).
		res := updateStack(ctx, func(s string) { out.send("output", "==> "+s) }, name, stackUpdateOptionsFor(name))
		recordStackUpdate(name, res)
		if res.Error != "" {
			if res.RolledBack {
				runErr = fmt.Errorf("update failed and was rolled back to the previous images: %s", res.Error)
			} else {
				runErr = fmt.Errorf("%s", res.Error)
			}
		}
	}

	duration := time.Since(startTime)
	audit.LogCommand(audit.LevelInfo, user, "stack_stream_"+action,
		[]string{name}, runErr == nil, duration, runErr)

	if runErr != nil {
		out.send("error", runErr.Error())
	} else {
		out.send("done", "")
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

	cmd := exec.CommandContext(ctx, "docker", stackComposeArgs(name, dir, "logs", "-f", "--tail", "100")...)
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

	cmd := exec.CommandContext(ctx, "docker", stackComposeArgs(req.Stack, dir, req.Action, req.Service)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	duration := time.Since(started)

	audit.LogCommand(audit.LevelInfo, user, "service_"+req.Action,
		[]string{req.Stack, req.Service}, err == nil, duration, err)

	if err != nil {
		respondOK(w, map[string]any{
			"success": false,
			"output":  string(out),
			"error":   err.Error(),
		})
		return
	}

	respondOK(w, map[string]any{
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
