// dplane-fenced holds SCSI-3 persistent reservations on all ZFS pool member
// disks. It runs as a standalone systemd service, NOT as a child of dplaned,
// so reservations survive dplaned restarts. This is critical: if reservations
// dropped every time the main daemon restarted, a failover during a daemon
// update would leave the pool unprotected.
//
// Protocol:
//   Listens on /run/dplaneos/fenced.sock (Unix stream).
//   Each connection sends one JSON line, receives one JSON line, then closes.
//
//   Request:  {"cmd": "STATUS"}
//             {"cmd": "RELEASE"}
//             {"cmd": "FENCE",   "device": "/dev/sgN"}
//             {"cmd": "UNFENCE", "device": "/dev/sgN"}
//
//   Response: {"ok": true, "data": ...}
//             {"ok": false, "error": "..."}
//
// Lifecycle:
//   Startup: derive key, enumerate pool disks, register + reserve all.
//   Every 30s: refresh disk list, fence any new disks.
//   SIGTERM: release all reservations, unregister, remove socket, exit.
//   Graceful failover: dplaned calls RELEASE via socket before exporting pools.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"dplaned/internal/scsipr"
)

const (
	socketPath     = "/run/dplaneos/fenced.sock"
	refreshInterval = 30 * time.Second
)

type request struct {
	Cmd    string `json:"cmd"`
	Device string `json:"device,omitempty"`
}

type response struct {
	OK    bool        `json:"ok"`
	Error string      `json:"error,omitempty"`
	Data  interface{} `json:"data,omitempty"`
}

// fenceState tracks which /dev/sgN devices we currently hold reservations on.
type fenceState struct {
	mu      sync.RWMutex
	key     scsipr.RegistrationKey
	devices map[string]bool // sgdev -> true if currently reserved
}

var state fenceState

func main() {
	log.SetPrefix("dplane-fenced: ")
	log.SetFlags(log.LstdFlags)

	key, err := scsipr.DeriveKey()
	if err != nil {
		log.Fatalf("cannot derive reservation key: %v", err)
	}
	state.key = key
	state.devices = make(map[string]bool)
	log.Printf("reservation key: %s", key)

	// Initial reservation pass.
	fenceAllPoolDisks()

	// Start Unix socket listener.
	os.Remove(socketPath)
	if err := os.MkdirAll(filepath.Dir(socketPath), 0750); err != nil {
		log.Fatalf("cannot create socket dir: %v", err)
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("cannot listen on %s: %v", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0660); err != nil {
		log.Printf("warning: cannot chmod socket: %v", err)
	}
	log.Printf("listening on %s", socketPath)

	// Periodic refresh goroutine: fence disks added after startup.
	go func() {
		t := time.NewTicker(refreshInterval)
		defer t.Stop()
		for range t.C {
			fenceAllPoolDisks()
		}
	}()

	// Accept loop goroutine.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				// Listener closed on shutdown.
				return
			}
			go handleConn(conn)
		}
	}()

	// Block until SIGTERM or SIGINT.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	s := <-sig
	log.Printf("received %s, releasing all reservations...", s)

	ln.Close()
	os.Remove(socketPath)
	releaseAll()
	log.Printf("shutdown complete")
}

// handleConn processes one socket connection (one request/response).
func handleConn(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	var req request
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
		writeResponse(conn, response{OK: false, Error: "invalid request: " + err.Error()})
		return
	}

	switch strings.ToUpper(req.Cmd) {
	case "STATUS":
		state.mu.RLock()
		devs := make([]string, 0, len(state.devices))
		for d, reserved := range state.devices {
			if reserved {
				devs = append(devs, d)
			}
		}
		state.mu.RUnlock()
		writeResponse(conn, response{OK: true, Data: map[string]interface{}{
			"key":     state.key.String(),
			"devices": devs,
		}})

	case "RELEASE":
		releaseAll()
		writeResponse(conn, response{OK: true})

	case "FENCE":
		if req.Device == "" {
			writeResponse(conn, response{OK: false, Error: "device required"})
			return
		}
		if err := fenceDevice(req.Device); err != nil {
			writeResponse(conn, response{OK: false, Error: err.Error()})
			return
		}
		writeResponse(conn, response{OK: true})

	case "UNFENCE":
		if req.Device == "" {
			writeResponse(conn, response{OK: false, Error: "device required"})
			return
		}
		if err := unfenceDevice(req.Device); err != nil {
			writeResponse(conn, response{OK: false, Error: err.Error()})
			return
		}
		writeResponse(conn, response{OK: true})

	default:
		writeResponse(conn, response{OK: false, Error: "unknown command: " + req.Cmd})
	}
}

func writeResponse(conn net.Conn, r response) {
	b, _ := json.Marshal(r)
	b = append(b, '\n')
	conn.Write(b)
}

// fenceDevice registers and reserves a single /dev/sgN device.
func fenceDevice(dev string) error {
	key := state.key
	if err := scsipr.Register(dev, key); err != nil {
		return fmt.Errorf("register %s: %w", dev, err)
	}
	if err := scsipr.Reserve(dev, key); err != nil {
		return fmt.Errorf("reserve %s: %w", dev, err)
	}
	state.mu.Lock()
	state.devices[dev] = true
	state.mu.Unlock()
	log.Printf("fenced: %s", dev)
	return nil
}

// unfenceDevice releases and unregisters a single /dev/sgN device.
func unfenceDevice(dev string) error {
	key := state.key
	if err := scsipr.Release(dev, key); err != nil {
		log.Printf("release %s: %v (continuing)", dev, err)
	}
	state.mu.Lock()
	delete(state.devices, dev)
	state.mu.Unlock()
	log.Printf("unfenced: %s", dev)
	return nil
}

// releaseAll releases reservations on all tracked devices.
func releaseAll() {
	state.mu.Lock()
	devs := make([]string, 0, len(state.devices))
	for d := range state.devices {
		devs = append(devs, d)
	}
	state.mu.Unlock()

	for _, dev := range devs {
		if err := scsipr.Release(dev, state.key); err != nil {
			log.Printf("release %s: %v", dev, err)
		} else {
			log.Printf("released: %s", dev)
		}
	}

	state.mu.Lock()
	state.devices = make(map[string]bool)
	state.mu.Unlock()
}

// fenceAllPoolDisks enumerates pool member disks and reserves any that are not
// already tracked.
func fenceAllPoolDisks() {
	disks, err := enumPoolDisks()
	if err != nil {
		log.Printf("disk enumeration failed: %v", err)
		return
	}

	state.mu.RLock()
	already := make(map[string]bool, len(state.devices))
	for d := range state.devices {
		already[d] = true
	}
	state.mu.RUnlock()

	for _, sg := range disks {
		if already[sg] {
			continue
		}
		if err := fenceDevice(sg); err != nil {
			log.Printf("fence %s: %v", sg, err)
		}
	}
}

// enumPoolDisks returns /dev/sgN paths for all current ZFS pool member disks.
// It calls zpool status -P to get full /dev paths, then resolves each block
// device to its scsi_generic counterpart via sysfs.
func enumPoolDisks() ([]string, error) {
	out, err := exec.Command("zpool", "status", "-P").Output()
	if err != nil {
		return nil, fmt.Errorf("zpool status: %w", err)
	}

	var sgDevs []string
	seen := make(map[string]bool)

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// vdev member lines start with /dev/ and have status columns after
		if !strings.HasPrefix(line, "/dev/") {
			continue
		}
		// First field is the device path
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		blockDev := fields[0]

		sg, err := blockDevToSG(blockDev)
		if err != nil {
			// Not all vdev types (cache, log labels) have an sg device; skip silently.
			continue
		}
		if seen[sg] {
			continue
		}
		seen[sg] = true
		sgDevs = append(sgDevs, sg)
	}

	return sgDevs, nil
}

// blockDevToSG resolves a block device path (e.g. /dev/sda or a by-id path)
// to its /dev/sgN counterpart by walking sysfs.
func blockDevToSG(blockDev string) (string, error) {
	// Resolve symlinks (by-id paths are symlinks to ../../sdX)
	resolved, err := filepath.EvalSymlinks(blockDev)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", blockDev, err)
	}

	// Strip partition suffix: sda1 -> sda, nvme0n1p1 -> nvme0n1
	base := filepath.Base(resolved)
	// For NVMe: nvme0n1 has no sg device; skip
	if strings.HasPrefix(base, "nvme") {
		return "", fmt.Errorf("NVMe devices do not use SG_IO (%s)", base)
	}

	// /sys/class/block/sda/device/generic -> symlink to /sys/class/scsi_generic/sg0
	genericLink := fmt.Sprintf("/sys/class/block/%s/device/generic", base)
	sgTarget, err := filepath.EvalSymlinks(genericLink)
	if err != nil {
		return "", fmt.Errorf("no scsi_generic for %s: %w", base, err)
	}

	sgName := filepath.Base(sgTarget) // "sg0"
	return "/dev/" + sgName, nil
}
