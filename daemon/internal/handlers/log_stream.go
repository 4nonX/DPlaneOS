package handlers

// log_stream.go — D-PlaneOS v3.3.2
//
// GET /api/system/logs/stream?unit=<service>
//
// Server-Sent Events endpoint that tails journald output for the requested
// unit (or all units if unit is empty). Safe: service name is strictly
// validated before being passed to journalctl.
//
// The frontend opens an EventSource and receives events of type "log"
// containing plain-text log lines. The connection is closed when the
// client disconnects or after maxLines (safety cap, default 2000).
//
// No goroutine leak: io.Copy blocks on the journalctl stdout pipe;
// when the client disconnects, r.Context() is cancelled and we kill
// the subprocess.

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
)

// LogStreamHandler streams journald lines as Server-Sent Events.
func LogStreamHandler(w http.ResponseWriter, r *http.Request) {
	// Require GET
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Optional ?unit= filter — validate strictly
	unit := r.URL.Query().Get("unit")
	if unit != "" {
		for _, c := range unit {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
				http.Error(w, "Invalid unit name", http.StatusBadRequest)
				return
			}
		}
	}

	// Check SSE is supported
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	// Build journalctl args — tail last 20 lines then follow
	args := []string{"--no-pager", "--follow", "-n", "20", "--output=short-iso"}
	if unit != "" {
		args = append(args, "-u", unit)
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cmd := exec.CommandContext(ctx, "journalctl", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		sendSSEEvent(w, flusher, "error", "Failed to open log pipe")
		return
	}
	if err := cmd.Start(); err != nil {
		sendSSEEvent(w, flusher, "error", "Failed to start log stream")
		return
	}

	// Stream lines until client disconnects or cap reached
	const maxLines = 2000
	lineCount := 0
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := scanner.Text()
		if line == "" {
			continue
		}
		sendSSEEvent(w, flusher, "log", line)
		lineCount++
		if lineCount >= maxLines {
			sendSSEEvent(w, flusher, "info", fmt.Sprintf("[Stream capped at %d lines. Open System Logs for full history.]", maxLines))
			return
		}
	}
}

func sendSSEEvent(w http.ResponseWriter, f http.Flusher, eventType, data string) {
	// SSE spec: escape newlines in data
	escaped := strings.ReplaceAll(data, "\n", "\ndata: ")
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, escaped)
	f.Flush()
}
