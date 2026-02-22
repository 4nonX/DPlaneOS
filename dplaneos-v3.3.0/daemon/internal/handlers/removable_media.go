package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"dplaned/internal/cmdutil"
	"log"

	"dplaned/internal/security"
)

type RemovableMediaHandler struct{}

func NewRemovableMediaHandler() *RemovableMediaHandler {
	return &RemovableMediaHandler{}
}

type Device struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Size       string `json:"size"`
	Model      string `json:"model"`
	MountPoint string `json:"mount_point"`
	Mounted    bool   `json:"mounted"`
}

// ListDevices lists all removable devices
func (h *RemovableMediaHandler) ListDevices(w http.ResponseWriter, r *http.Request) {
	// Use lsblk to list removable devices
	output, err := cmdutil.RunFast("lsblk", "-J", "-o", "NAME,SIZE,MODEL,MOUNTPOINT,RM,TYPE")
	
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list devices: %v", err), http.StatusInternalServerError)
		return
	}

	// Parse lsblk JSON output
	var lsblkOutput struct {
		Blockdevices []struct {
			Name       string `json:"name"`
			Size       string `json:"size"`
			Model      string `json:"model"`
			Mountpoint string `json:"mountpoint"`
			Rm         string `json:"rm"`
			Type       string `json:"type"`
		} `json:"blockdevices"`
	}

	if err := json.Unmarshal(output, &lsblkOutput); err != nil {
		http.Error(w, fmt.Sprintf("Failed to parse lsblk output: %v", err), http.StatusInternalServerError)
		return
	}

	devices := make([]Device, 0)
	for _, dev := range lsblkOutput.Blockdevices {
		// Only include removable devices (USB, SD cards, etc.)
		if dev.Rm == "1" && dev.Type == "disk" {
			devices = append(devices, Device{
				Name:       dev.Name,
				Path:       "/dev/" + dev.Name,
				Size:       dev.Size,
				Model:      dev.Model,
				MountPoint: dev.Mountpoint,
				Mounted:    dev.Mountpoint != "",
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"devices": devices,
	})
}

// MountDevice mounts a removable device
func (h *RemovableMediaHandler) MountDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device     string `json:"device"`
		MountPoint string `json:"mount_point"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate inputs before exec.Command — prevents injection
	if err := security.ValidateDevicePath(req.Device); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := security.ValidateMountPoint(req.MountPoint); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Create mount point if it doesn't exist
	if _, err := cmdutil.RunFast("mkdir", "-p", req.MountPoint); err != nil { log.Printf("WARN: mkdir: %v", err) }

	// Mount the device
	output, err := cmdutil.RunMedium("mount", req.Device, req.MountPoint)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to mount: %v - %s", err, string(output)),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// UnmountDevice unmounts a removable device
func (h *RemovableMediaHandler) UnmountDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device string `json:"device"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if err := security.ValidateDevicePath(req.Device); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	output, err := cmdutil.RunFast("umount", req.Device)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to unmount: %v - %s", err, string(output)),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// EjectDevice safely ejects a removable device
func (h *RemovableMediaHandler) EjectDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device string `json:"device"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if err := security.ValidateDevicePath(req.Device); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// First unmount
	if _, err := cmdutil.RunFast("umount", req.Device); err != nil { log.Printf("WARN: pre-eject umount: %v", err) }

	// Then eject
	output, err := cmdutil.RunFast("eject", req.Device)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to eject: %v - %s", err, string(output)),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}
