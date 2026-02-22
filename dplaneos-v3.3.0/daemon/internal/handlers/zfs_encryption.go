package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"dplaned/internal/cmdutil"
	"dplaned/internal/security"
)

type ZFSEncryptionHandler struct{}

func NewZFSEncryptionHandler() *ZFSEncryptionHandler {
	return &ZFSEncryptionHandler{}
}

type EncryptedDataset struct {
	Name          string `json:"name"`
	Encryption    string `json:"encryption"`
	KeyStatus     string `json:"keystatus"`
	KeyLocation   string `json:"keylocation"`
	KeyFormat     string `json:"keyformat"`
}

// ListEncryptedDatasets lists all encrypted ZFS datasets
func (h *ZFSEncryptionHandler) ListEncryptedDatasets(w http.ResponseWriter, r *http.Request) {
	out, err := cmdutil.RunFast("zfs", "list", "-H", "-o", "name,encryption,keystatus,keylocation,keyformat", "-t", "filesystem,volume")

	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list datasets: %v", err), http.StatusInternalServerError)
		return
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	datasets := make([]EncryptedDataset, 0)

	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) < 5 {
			fields = strings.Fields(line)
		}
		if len(fields) >= 5 && fields[1] != "off" && fields[1] != "-" {
			datasets = append(datasets, EncryptedDataset{
				Name:        fields[0],
				Encryption:  fields[1],
				KeyStatus:   fields[2],
				KeyLocation: fields[3],
				KeyFormat:   fields[4],
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"datasets": datasets,
	})
}

// UnlockDataset unlocks an encrypted dataset
func (h *ZFSEncryptionHandler) UnlockDataset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset string `json:"dataset"`
		Key     string `json:"key"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if err := security.ValidateDatasetName(req.Dataset); err != nil {
		http.Error(w, "Invalid dataset name: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Key == "" {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}

	// Create temporary key file
	output, err := cmdutil.RunWithStdin(cmdutil.TimeoutMedium, req.Key+"\n", "zfs", "load-key", req.Dataset)

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to unlock: %v - %s", err, string(output)),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// LockDataset locks an encrypted dataset
func (h *ZFSEncryptionHandler) LockDataset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset string `json:"dataset"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if err := security.ValidateDatasetName(req.Dataset); err != nil {
		http.Error(w, "Invalid dataset name: "+err.Error(), http.StatusBadRequest)
		return
	}

	output, err := cmdutil.RunFast("zfs", "unload-key", req.Dataset)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to lock: %v - %s", err, string(output)),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// CreateEncryptedDataset creates a new encrypted dataset
func (h *ZFSEncryptionHandler) CreateEncryptedDataset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		Encryption string `json:"encryption"`
		Key        string `json:"key"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Encryption == "" {
		req.Encryption = "aes-256-gcm"
	}

	if req.Key == "" {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}

	if err := security.ValidateDatasetName(req.Name); err != nil {
		http.Error(w, "Invalid dataset name: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Whitelist encryption algorithms
	validAlgos := map[string]bool{"aes-128-ccm": true, "aes-192-ccm": true, "aes-256-ccm": true, "aes-128-gcm": true, "aes-192-gcm": true, "aes-256-gcm": true}
	if !validAlgos[req.Encryption] {
		http.Error(w, "Invalid encryption algorithm", http.StatusBadRequest)
		return
	}

	out, err := cmdutil.RunWithStdin(cmdutil.TimeoutMedium, req.Key+"\n"+req.Key+"\n",
		"zfs", "create",
		"-o", fmt.Sprintf("encryption=%s", req.Encryption),
		"-o", "keyformat=passphrase",
		"-o", "keylocation=prompt",
		req.Name,
	)
	
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create: %v - %s", err, string(out)),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// ChangeKey changes the encryption key
func (h *ZFSEncryptionHandler) ChangeKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset string `json:"dataset"`
		OldKey  string `json:"old_key"`
		NewKey  string `json:"new_key"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if err := security.ValidateDatasetName(req.Dataset); err != nil {
		http.Error(w, "Invalid dataset name: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.OldKey == "" || req.NewKey == "" {
		http.Error(w, "old_key and new_key are required", http.StatusBadRequest)
		return
	}

	out, err := cmdutil.RunWithStdin(cmdutil.TimeoutMedium, req.OldKey+"\n"+req.NewKey+"\n"+req.NewKey+"\n",
		"zfs", "change-key", "-l", req.Dataset)

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to change key: %v - %s", err, string(out)),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}
