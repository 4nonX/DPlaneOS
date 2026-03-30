package zfs

import (
	"bufio"
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StartZEDListener creates a Unix socket to listen for events from the ZED hook script.
// If the socket directory does not exist, it logs a warning and returns (safe fallback).
// On receiving an event, it calls the provided callbacks.
// The function returns when ctx is cancelled, allowing clean daemon shutdown.
func StartZEDListener(ctx context.Context, socketPath string, broadcast func(eventType string, data interface{}, level string), dispatchAlert func(event, pool, msg string)) {
	dir := filepath.Dir(socketPath)

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		log.Printf("Warning: ZED listener socket directory %s does not exist. Falling back to polling.", dir)
		return
	}

	// Remove existing socket if it exists
	os.Remove(socketPath)

	l, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Printf("Warning: Failed to listen on ZED socket %s: %v", socketPath, err)
		return
	}
	defer l.Close()

	if err := os.Chmod(socketPath, 0600); err != nil {
		log.Printf("Warning: Failed to chmod ZED socket: %v", err)
	}

	ul := l.(*net.UnixListener)
	for {
		// Poll the context by applying a short Accept deadline.
		// This lets the loop exit cleanly when the daemon shuts down.
		ul.SetDeadline(time.Now().Add(1 * time.Second))
		conn, err := ul.Accept()
		if err != nil {
			// Context cancelled → clean exit.
			select {
			case <-ctx.Done():
				log.Printf("ZED listener: context cancelled, shutting down")
				return
			default:
			}
			// Deadline exceeded → loop and check ctx again.
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			log.Printf("ZED socket accept error: %v", err)
			continue
		}

		go handleZEDConnection(conn, broadcast, dispatchAlert)
	}
}

func handleZEDConnection(conn net.Conn, broadcast func(eventType string, data interface{}, level string), dispatchAlert func(event, pool, msg string)) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()

		// Expected format: zfs_event:severity:pool:subclass:state
		parts := strings.Split(line, ":")
		if len(parts) < 5 || parts[0] != "zfs_event" {
			continue
		}

		severity := parts[1]
		pool := parts[2]
		subclass := parts[3]
		state := parts[4]

		log.Printf("ZED Event received: Pool=%s Subclass=%s State=%s Severity=%s", pool, subclass, state, severity)

		// Broadcast to UI via WebSocket
		eventData := map[string]string{
			"pool":     pool,
			"subclass": subclass,
			"state":    state,
		}

		var wsLevel string
		var alertMsg string

		switch severity {
		case "critical":
			wsLevel = "error"
			alertMsg = "CRITICAL ZFS EVENT\nPool: " + pool + "\nEvent: " + subclass + "\nState: " + state
		case "warning":
			wsLevel = "warning"
			alertMsg = "ZFS WARNING EVENT\nPool: " + pool + "\nEvent: " + subclass + "\nState: " + state
		default:
			wsLevel = "info"
		}

		broadcast("zfs.event."+subclass, eventData, wsLevel)

		// Dispatch alert for warning and critical events
		if severity == "critical" || severity == "warning" {
			dispatchAlert("zfs."+severity+"."+subclass, pool, alertMsg)
		}
	}
}
