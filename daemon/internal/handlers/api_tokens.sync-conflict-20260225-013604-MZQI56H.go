package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"dplaned/internal/audit"
)

// APITokenHandler manages long-lived API tokens for automation
type APITokenHandler struct {
	db *sql.DB
}

func NewAPITokenHandler(db *sql.DB) *APITokenHandler {
	return &APITokenHandler{db: db}
}

type apiToken struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Prefix      string  `json:"prefix"`
	Scopes      string  `json:"scopes"`
	LastUsed    *string `json:"last_used"`
	ExpiresAt   *string `json:"expires_at"`
	CreatedAt   string  `json:"created_at"`
}

// generateToken creates a new dpl_<prefix>_<random> token
// Returns (fullToken, prefix, hash)
func generateToken() (string, string, string, error) {
	// 32 bytes of randomness = 64 hex chars
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", err
	}
	fullHex := hex.EncodeToString(raw)
	prefix := fullHex[:8]           // first 8 chars for display
	full := "dpl_" + fullHex        // full token shown once at creation

	// Hash for storage (SHA-256 — tokens are long enough to be safe without bcrypt)
	h := sha256.Sum256([]byte(full))
	hash := hex.EncodeToString(h[:])

	return full, prefix, hash, nil
}

// HashToken hashes a raw token for lookup
func HashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// ValidateAPIToken checks if a bearer token is valid and returns the user_id
// Used by session middleware to support token-based auth
func ValidateAPIToken(db *sql.DB, token string) (int, string, error) {
	if !strings.HasPrefix(token, "dpl_") {
		return 0, "", fmt.Errorf("not an API token")
	}

	hash := HashToken(token)

	var userID int
	var username, scopes string
	var expiresAt *string

	err := db.QueryRow(`
		SELECT at.user_id, u.username, at.scopes, at.expires_at
		FROM api_tokens at
		JOIN users u ON u.id = at.user_id
		WHERE at.token_hash = ? AND u.active = 1
	`, hash).Scan(&userID, &username, &scopes, &expiresAt)
	if err == sql.ErrNoRows {
		return 0, "", fmt.Errorf("invalid token")
	}
	if err != nil {
		return 0, "", err
	}

	// Check expiry
	if expiresAt != nil && *expiresAt != "" {
		t, err := time.Parse("2006-01-02 15:04:05", *expiresAt)
		if err == nil && time.Now().After(t) {
			return 0, "", fmt.Errorf("token expired")
		}
	}

	// Update last_used asynchronously
	go db.Exec(`UPDATE api_tokens SET last_used = CURRENT_TIMESTAMP WHERE token_hash = ?`, hash)

	return userID, username, nil
}

// HandleTokens — GET: list tokens for user, POST: create/revoke
func (h *APITokenHandler) HandleTokens(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	if user == "" {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Get user ID
	var userID int
	if err := h.db.QueryRow(`SELECT id FROM users WHERE username = ?`, user).Scan(&userID); err != nil {
		respondErrorSimple(w, "User not found", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.listTokens(w, userID)
	case http.MethodPost:
		h.tokenAction(w, r, userID, user)
	case http.MethodDelete:
		h.revokeTokenByID(w, r, userID, user)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *APITokenHandler) listTokens(w http.ResponseWriter, userID int) {
	rows, err := h.db.Query(`
		SELECT id, name, token_prefix, scopes,
		       strftime('%Y-%m-%dT%H:%M:%SZ', last_used),
		       strftime('%Y-%m-%dT%H:%M:%SZ', expires_at),
		       strftime('%Y-%m-%dT%H:%M:%SZ', created_at)
		FROM api_tokens WHERE user_id = ?
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		respondErrorSimple(w, "Failed to list tokens", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var tokens []apiToken
	for rows.Next() {
		var t apiToken
		rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.Scopes, &t.LastUsed, &t.ExpiresAt, &t.CreatedAt)
		tokens = append(tokens, t)
	}
	if tokens == nil {
		tokens = []apiToken{}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"tokens":  tokens,
	})
}

func (h *APITokenHandler) tokenAction(w http.ResponseWriter, r *http.Request, userID int, username string) {
	var req struct {
		Action    string `json:"action"`
		Name      string `json:"name"`
		Scopes    string `json:"scopes"`
		ExpiresIn int    `json:"expires_in_days"` // 0 = never
		ID        int    `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	switch req.Action {
	case "create":
		h.createToken(w, userID, username, req.Name, req.Scopes, req.ExpiresIn)
	case "revoke":
		h.revokeByID(w, req.ID, userID, username)
	default:
		respondErrorSimple(w, "Unknown action: "+req.Action, http.StatusBadRequest)
	}
}

func (h *APITokenHandler) createToken(w http.ResponseWriter, userID int, username, name, scopes string, expiresDays int) {
	if name == "" {
		respondErrorSimple(w, "Token name is required", http.StatusBadRequest)
		return
	}
	if len(name) > 64 {
		respondErrorSimple(w, "Token name too long (max 64 chars)", http.StatusBadRequest)
		return
	}
	if scopes == "" {
		scopes = "read"
	}

	// Validate scopes
	validScopes := map[string]bool{"read": true, "write": true, "admin": true}
	for _, s := range strings.Split(scopes, ",") {
		if !validScopes[strings.TrimSpace(s)] {
			respondErrorSimple(w, "Invalid scope: "+s+" (valid: read, write, admin)", http.StatusBadRequest)
			return
		}
	}

	fullToken, prefix, hash, err := generateToken()
	if err != nil {
		respondErrorSimple(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	var expiresAt interface{}
	if expiresDays > 0 {
		expiresAt = time.Now().AddDate(0, 0, expiresDays).Format("2006-01-02 15:04:05")
	}

	result, err := h.db.Exec(`
		INSERT INTO api_tokens (user_id, name, token_hash, token_prefix, scopes, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, userID, name, hash, prefix, scopes, expiresAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			respondErrorSimple(w, "A token named '"+name+"' already exists", http.StatusConflict)
			return
		}
		respondErrorSimple(w, "Failed to create token", http.StatusInternalServerError)
		log.Printf("API TOKEN CREATE ERROR: %v", err)
		return
	}

	id, _ := result.LastInsertId()
	audit.LogAction("api_token", username, fmt.Sprintf("Created API token '%s' (scopes: %s)", name, scopes), true, 0)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"token":   fullToken, // shown ONCE only
		"id":      id,
		"name":    name,
		"prefix":  prefix,
		"scopes":  scopes,
		"message": "Store this token securely — it will not be shown again.",
	})
}

func (h *APITokenHandler) revokeByID(w http.ResponseWriter, tokenID, userID int, username string) {
	if tokenID == 0 {
		respondErrorSimple(w, "Token ID required", http.StatusBadRequest)
		return
	}

	// Get token name for audit before deleting
	var name string
	h.db.QueryRow(`SELECT name FROM api_tokens WHERE id = ? AND user_id = ?`, tokenID, userID).Scan(&name)

	result, err := h.db.Exec(`DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, tokenID, userID)
	if err != nil {
		respondErrorSimple(w, "Failed to revoke token", http.StatusInternalServerError)
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondErrorSimple(w, "Token not found", http.StatusNotFound)
		return
	}

	audit.LogAction("api_token", username, fmt.Sprintf("Revoked API token '%s'", name), true, 0)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Token revoked",
	})
}

func (h *APITokenHandler) revokeTokenByID(w http.ResponseWriter, r *http.Request, userID int, username string) {
	var req struct{ ID int `json:"id"` }
	json.NewDecoder(r.Body).Decode(&req)
	h.revokeByID(w, req.ID, userID, username)
}
