package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"time"

	"dplaned/internal/acl"
	"dplaned/internal/audit"
	"dplaned/internal/security"
)

// GetNFS4ACL handles GET /api/nfs4acl?path=...
// Returns the NFSv4 ACL for a file or directory via nfs4_getfacl.
func GetNFS4ACL(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	path := r.URL.Query().Get("path")

	if !security.IsValidPath(path) {
		respondErrorSimple(w, "Invalid or disallowed path", http.StatusBadRequest)
		return
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "nfs4_getfacl", path).CombinedOutput()
	duration := time.Since(start)
	audit.LogCommand(audit.LevelInfo, user, "nfs4_getfacl", []string{path}, err == nil, duration, err)

	if err != nil {
		respondOK(w, acl.ACLResult{Path: path, Error: string(out)})
		return
	}

	aces := acl.ParseNFSv4ACL(string(out))
	respondOK(w, acl.ACLResult{Path: path, NFSv4ACEs: aces})
}

// SetNFS4ACL handles PUT /api/nfs4acl
// Replaces the full NFSv4 ACL on a file or directory via nfs4_setfacl -s.
func SetNFS4ACL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := r.Header.Get("X-User")

	var req struct {
		Path string        `json:"path"`
		ACEs []acl.NFSv4ACE `json:"aces"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !security.IsValidPath(req.Path) {
		respondErrorSimple(w, "Invalid or disallowed path", http.StatusBadRequest)
		return
	}

	if len(req.ACEs) == 0 {
		respondErrorSimple(w, "ACE list cannot be empty", http.StatusBadRequest)
		return
	}

	for i, ace := range req.ACEs {
		if err := acl.ValidateACE(ace); err != nil {
			respondErrorSimple(w, "ACE["+string(rune('0'+i))+"]: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	aceSpec := acl.FormatACESpec(req.ACEs)

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "nfs4_setfacl", "-s", aceSpec, req.Path).CombinedOutput()
	duration := time.Since(start)
	audit.LogCommand(audit.LevelInfo, user, "nfs4_setfacl", []string{req.Path}, err == nil, duration, err)

	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   string(out),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"path":    req.Path,
	})
}
