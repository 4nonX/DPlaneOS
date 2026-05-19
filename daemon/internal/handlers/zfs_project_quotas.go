package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

var projDatasetRe = regexp.MustCompile(`^[a-zA-Z0-9_\-\./:]+$`)
var projIDRe = regexp.MustCompile(`^[0-9]+$`)
var projSizeRe = regexp.MustCompile(`^[0-9]+[KMGTP]?$`)

// ProjectQuota represents a ZFS project quota entry.
type ProjectQuota struct {
	Dataset string `json:"dataset"`
	ID      string `json:"id"`
	Quota   string `json:"quota"`
	Used    string `json:"used,omitempty"`
}

// GetProjectQuotas handles GET /api/zfs/quota/project
func GetProjectQuotas(w http.ResponseWriter, r *http.Request) {
	dataset := r.URL.Query().Get("dataset")
	if dataset == "" {
		respondErrorSimple(w, "dataset parameter required", http.StatusBadRequest)
		return
	}
	if !projDatasetRe.MatchString(dataset) {
		respondErrorSimple(w, "invalid dataset name", http.StatusBadRequest)
		return
	}
	out, err := executeCommandWithTimeout(TimeoutFast, "zfs", []string{"get", "-H", "all", dataset})
	if err != nil {
		respondErrorSimple(w, "Failed to get project quotas: "+err.Error(), http.StatusInternalServerError)
		return
	}

	quotas := []ProjectQuota{}
	usedMap := map[string]string{}

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		prop := fields[1]
		val := fields[2]
		if strings.HasPrefix(prop, "projectquota@") {
			id := strings.TrimPrefix(prop, "projectquota@")
			if val != "none" && val != "-" {
				quotas = append(quotas, ProjectQuota{Dataset: dataset, ID: id, Quota: val})
			}
		} else if strings.HasPrefix(prop, "projectused@") {
			usedMap[strings.TrimPrefix(prop, "projectused@")] = val
		}
	}
	for i := range quotas {
		if used, ok := usedMap[quotas[i].ID]; ok {
			quotas[i].Used = used
		}
	}
	respondOK(w, map[string]interface{}{"success": true, "quotas": quotas})
}

// SetProjectQuota handles POST /api/zfs/quota/project
func SetProjectQuota(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset string `json:"dataset"`
		ID      string `json:"id"`
		Quota   string `json:"quota"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !projDatasetRe.MatchString(req.Dataset) {
		respondErrorSimple(w, "invalid dataset name", http.StatusBadRequest)
		return
	}
	if !projIDRe.MatchString(req.ID) {
		respondErrorSimple(w, "invalid project ID: must be a numeric ID", http.StatusBadRequest)
		return
	}
	if !projSizeRe.MatchString(strings.ToUpper(req.Quota)) {
		respondErrorSimple(w, "invalid quota format: use e.g. 10G, 500M", http.StatusBadRequest)
		return
	}
	prop := "projectquota@" + req.ID + "=" + req.Quota
	if _, err := executeCommandWithTimeout(TimeoutFast, "zfs", []string{"set", prop, req.Dataset}); err != nil {
		respondErrorSimple(w, "Failed to set project quota: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, CommandResponse{Success: true, Output: "Project quota set: " + prop + " on " + req.Dataset})
}

// RemoveProjectQuota handles DELETE /api/zfs/quota/project
func RemoveProjectQuota(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset string `json:"dataset"`
		ID      string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !projDatasetRe.MatchString(req.Dataset) {
		respondErrorSimple(w, "invalid dataset name", http.StatusBadRequest)
		return
	}
	if !projIDRe.MatchString(req.ID) {
		respondErrorSimple(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	prop := "projectquota@" + req.ID + "=none"
	if _, err := executeCommandWithTimeout(TimeoutFast, "zfs", []string{"set", prop, req.Dataset}); err != nil {
		respondErrorSimple(w, "Failed to remove project quota: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, CommandResponse{Success: true, Output: "Project quota removed for project " + req.ID + " on " + req.Dataset})
}
