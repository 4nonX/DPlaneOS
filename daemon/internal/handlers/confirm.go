package handlers

import (
	"encoding/json"
	"net/http"

	"dplaned/internal/middleware"
	"dplaned/internal/security"
)

// HandleIssueConfirmToken handles POST /api/confirm/issue.
// Issues a short-lived (60 s) single-use confirmation token for a named
// destructive operation. The token must be presented as X-Confirm-Token on
// the actual destructive request within 60 seconds.
func HandleIssueConfirmToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operation string `json:"operation"`
		Target    string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !security.ValidConfirmOp(req.Operation) {
		respondErrorSimple(w, "unknown operation", http.StatusBadRequest)
		return
	}
	if req.Target == "" {
		respondErrorSimple(w, "target is required", http.StatusBadRequest)
		return
	}

	user, ok := middleware.GetUserFromContext(r)
	if !ok || user == nil {
		respondErrorSimple(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	token, err := security.IssueConfirmToken(req.Operation, req.Target, user.ID)
	if err != nil {
		respondErrorSimple(w, "failed to issue token", http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{
		"success":    true,
		"token":      token,
		"expires_in": 60,
	})
}
