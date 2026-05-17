package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/hardware"
	"github.com/gorilla/mux"
)

var sgDeviceRe = regexp.MustCompile(`^/dev/sg[0-9]+$`)

// ListEnclosures serves GET /api/enclosure.
// Returns all enclosures and their slots as enumerated from /sys/class/enclosure.
// On systems without SES hardware the enclosures array will be empty.
func ListEnclosures(w http.ResponseWriter, r *http.Request) {
	encs, err := hardware.ListEnclosures()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to enumerate enclosures", err)
		return
	}
	if encs == nil {
		encs = []hardware.Enclosure{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"enclosures": encs})
}

// SetEnclosureLocate serves PUT /api/enclosure/{id}/slot/{index}/locate.
// Body: {"locate": true|false}
func SetEnclosureLocate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	encID := vars["id"]
	indexStr := vars["index"]

	slotIndex, err := strconv.Atoi(indexStr)
	if err != nil || slotIndex < 0 {
		respondError(w, http.StatusBadRequest, "Invalid slot index", nil)
		return
	}

	var req struct {
		Locate bool `json:"locate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if err := hardware.SetLocateLED(encID, slotIndex, req.Locate); err != nil {
		respondError(w, http.StatusBadRequest, "Failed to set locate LED", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "locate": req.Locate})
}

// GetEnclosureSESStatus serves GET /api/enclosure/{id}/ses-status.
// Runs sg_ses --page=es against the enclosure's sg device and returns parsed element status.
// Returns 404 when no sg device can be resolved (virtual/no-hardware systems).
func GetEnclosureSESStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	encID := vars["id"]

	sgDev, err := hardware.FindSGDevice(encID)
	if err != nil {
		respondError(w, http.StatusNotFound, "SG device not found for enclosure", err)
		return
	}
	if !sgDeviceRe.MatchString(sgDev) {
		respondError(w, http.StatusInternalServerError, "Resolved SG device path is invalid", nil)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "sg_ses", "--page=es", sgDev).CombinedOutput()
	if err != nil {
		respondError(w, http.StatusInternalServerError,
			fmt.Sprintf("sg_ses failed: %s", strings.TrimSpace(string(out))), err)
		return
	}

	elements := hardware.ParseSGSesOutput(string(out))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"enclosure_id": encID,
		"device":       sgDev,
		"elements":     elements,
	})
}
