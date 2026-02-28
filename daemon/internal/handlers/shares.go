package handlers

import (
	"encoding/json"
	"net/http"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/security"
)

// ReloadSMBConfig reloads Samba configuration
func ReloadSMBConfig(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	output, err := cmdutil.RunFast("systemctl", "reload", "smbd")

	audit.LogActivity(user, "samba_reload", map[string]interface{}{
		"success": err == nil,
	})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"output":  string(output),
	})
}

// TestSMBConfig tests Samba configuration
func TestSMBConfig(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	output, err := cmdutil.RunFast("testparm", "-s")

	audit.LogActivity(user, "samba_test", map[string]interface{}{
		"success": err == nil,
	})

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": err == nil,
		"output":  string(output),
	})
}

// ReloadNFSExports reloads NFS exports
func ReloadNFSExports(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	output, err := cmdutil.RunFast("exportfs", "-ra")

	audit.LogActivity(user, "nfs_reload", map[string]interface{}{
		"success": err == nil,
	})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"output":  string(output),
	})
}

// ListNFSExports lists current NFS exports
func ListNFSExports(w http.ResponseWriter, r *http.Request) {
	output, err := cmdutil.RunFast("exportfs", "-v")
	if err != nil {
		// exportfs not installed or NFS not configured — return empty list, not 500
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"exports": []string{},
			"output":  "",
			"note":    "NFS server not installed or no exports configured",
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"output":  string(output),
	})
}
