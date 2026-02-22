package handlers

import (
	"encoding/json"
	"net/http"
)

// respondJSON sends a JSON response with the given status code and payload.
func respondJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

// respondOK sends a 200 JSON response (convenience wrapper).
func respondOK(w http.ResponseWriter, payload interface{}) {
	respondJSON(w, http.StatusOK, payload)
}

// respondError sends a JSON error response.
func respondError(w http.ResponseWriter, status int, message string, err error) {
	response := map[string]interface{}{
		"error":  message,
		"status": status,
	}

	if err != nil {
		response["details"] = err.Error()
	}

	respondJSON(w, status, response)
}

// respondErrorSimple sends a JSON error response without an error object.
func respondErrorSimple(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(CommandResponse{
		Success: false,
		Error:   message,
	})
}

// getUserFromRequest extracts the authenticated username from request headers.
// The frontend sets X-User on every authenticated request.
func getUserFromRequest(r *http.Request) string {
	return r.Header.Get("X-User")
}
