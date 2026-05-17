package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"dplaned/internal/storageops"
	"github.com/gorilla/mux"
)

// ListStorageOperations serves GET /api/storage/operations.
// Returns the 50 most recent storage operations so operators can audit
// in-progress or failed operations.
func ListStorageOperations(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	records, err := storageops.ListRecent(registryDB, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to list storage operations", err)
		return
	}
	if records == nil {
		records = []storageops.Record{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"operations": records})
}

// ClearStorageOperation serves DELETE /api/storage/operations/{id}.
// Marks a stuck pending operation as failed so a new operation can be started
// on the same target. Only pending operations can be cleared.
func ClearStorageOperation(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		respondError(w, http.StatusBadRequest, "Invalid operation ID", nil)
		return
	}
	if err := storageops.ClearStuck(registryDB, id); err != nil {
		respondError(w, http.StatusConflict, err.Error(), nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "id": id})
}
