package handlers

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

// ═══════════════════════════════════════════════════════════════
//  LOGIN RATE LIMITING — Exponential Backoff per IP
// ═══════════════════════════════════════════════════════════════

type loginAttempt struct {
	failures    int
	lastAttempt time.Time
	lockedUntil time.Time
}

var (
	loginAttemptsMu sync.Mutex
	loginAttempts   = make(map[string]*loginAttempt)
)

// getLoginDelay returns the lockout duration based on failure count:
// 1 fail = 0s, 2 = 2s, 3 = 4s, 4 = 8s, 5 = 16s, 6+ = 30s (cap)
func getLoginDelay(failures int) time.Duration {
	if failures <= 1 {
		return 0
	}
	delay := time.Duration(1<<uint(failures-1)) * time.Second
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return delay
}

// checkLoginThrottle returns true if the IP is currently throttled
func checkLoginThrottle(ip string) (bool, time.Duration) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	attempt, exists := loginAttempts[ip]
	if !exists {
		return false, 0
	}

	// Clean up old entries (no failures for 15 min = reset)
	if time.Since(attempt.lastAttempt) > 15*time.Minute {
		delete(loginAttempts, ip)
		return false, 0
	}

	if time.Now().Before(attempt.lockedUntil) {
		remaining := time.Until(attempt.lockedUntil)
		return true, remaining
	}

	return false, 0
}

// recordLoginFailure increments failure count and sets lockout
func recordLoginFailure(ip string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	attempt, exists := loginAttempts[ip]
	if !exists {
		attempt = &loginAttempt{}
		loginAttempts[ip] = attempt
	}

	attempt.failures++
	attempt.lastAttempt = time.Now()
	attempt.lockedUntil = time.Now().Add(getLoginDelay(attempt.failures))
}

// recordLoginSuccess resets the failure counter
func recordLoginSuccess(ip string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	delete(loginAttempts, ip)
}

// ═══════════════════════════════════════════════════════════════
//  PASSWORD COMPLEXITY
// ═══════════════════════════════════════════════════════════════

// validatePasswordStrength checks for minimum complexity requirements:
// - At least 8 characters
// - At least 1 uppercase letter
// - At least 1 lowercase letter
// - At least 1 digit
// - At least 1 special character
func validatePasswordStrength(password string) (bool, string) {
	if len(password) < 8 {
		return false, "Password must be at least 8 characters"
	}
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, ch := range password {
		switch {
		case unicode.IsUpper(ch):
			hasUpper = true
		case unicode.IsLower(ch):
			hasLower = true
		case unicode.IsDigit(ch):
			hasDigit = true
		case unicode.IsPunct(ch) || unicode.IsSymbol(ch):
			hasSpecial = true
		}
	}
	var missing []string
	if !hasUpper {
		missing = append(missing, "uppercase letter")
	}
	if !hasLower {
		missing = append(missing, "lowercase letter")
	}
	if !hasDigit {
		missing = append(missing, "digit")
	}
	if !hasSpecial {
		missing = append(missing, "special character")
	}
	if len(missing) > 0 {
		return false, fmt.Sprintf("Password must contain at least one %s", strings.Join(missing, ", "))
	}
	return true, ""
}

// AuthHandler handles authentication endpoints
type AuthHandler struct {
	db *sql.DB
}

// NewAuthHandler creates a new auth handler
func NewAuthHandler(db *sql.DB) *AuthHandler {
	return &AuthHandler{db: db}
}

// --- POST /api/auth/login ---

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// === LOGIN RATE LIMITING ===
	clientIP := r.RemoteAddr
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		clientIP = strings.Split(forwarded, ",")[0]
	}
	if throttled, remaining := checkLoginThrottle(clientIP); throttled {
		h.auditLog("", "login_throttled", fmt.Sprintf("IP %s throttled for %.0fs", clientIP, remaining.Seconds()), clientIP)
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(remaining.Seconds())+1))
		respondJSON(w, http.StatusTooManyRequests, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Too many failed attempts. Try again in %d seconds.", int(remaining.Seconds())+1),
		})
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "Invalid request body",
		})
		return
	}

	// Allowlist validation
	if !isAlphanumericDash(req.Username) || len(req.Username) > 64 {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "Invalid username format",
		})
		return
	}

	// Lookup user
	var userID int
	var storedHash string
	var active, mustChange int
	err := h.db.QueryRow(
		`SELECT id, password_hash, active, COALESCE(must_change_password, 0) FROM users WHERE username = ? LIMIT 1`,
		req.Username,
	).Scan(&userID, &storedHash, &active, &mustChange)

	if err == sql.ErrNoRows {
		// Constant-time: still do a bcrypt compare to prevent timing attacks
		bcrypt.CompareHashAndPassword([]byte("$2a$10$dummyhashfortimingoracle000000000000000000000000000000"), []byte(req.Password))
		recordLoginFailure(clientIP)
		log.Printf("AUTH FAIL: unknown user %q from %s", req.Username, r.RemoteAddr)
		respondJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"success": false, "error": "Invalid credentials",
		})
		return
	} else if err != nil {
		log.Printf("AUTH ERROR: db query failed: %v", err)
		respondJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": "Internal error",
		})
		return
	}

	if active != 1 {
		recordLoginFailure(clientIP)
		log.Printf("AUTH FAIL: disabled user %q from %s", req.Username, r.RemoteAddr)
		respondJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"success": false, "error": "Account disabled",
		})
		return
	}

	// Verify password (bcrypt)
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(req.Password)); err != nil {
		recordLoginFailure(clientIP)
		log.Printf("AUTH FAIL: wrong password for %q from %s", req.Username, r.RemoteAddr)
		respondJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"success": false, "error": "Invalid credentials",
		})
		return
	}

	// Generate session token (32 bytes = 64 hex chars)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		log.Printf("AUTH ERROR: failed to generate session token: %v", err)
		respondJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": "Internal error",
		})
		return
	}
	sessionID := hex.EncodeToString(tokenBytes)

	// Check if user has TOTP enabled
	var totpEnabled int
	h.db.QueryRow(`SELECT COALESCE(totp_enabled, 0) FROM users WHERE id = ?`, userID).Scan(&totpEnabled)

	// Session expires in 24 hours
	expiresAt := time.Now().Add(24 * time.Hour).Unix()

	if totpEnabled == 1 {
		// Create a short-lived pending session (5 minutes) for TOTP verification
		pendingExpiry := time.Now().Add(5 * time.Minute).Unix()
		now := time.Now().Unix()
		_, err = h.db.Exec(
			`INSERT INTO sessions (session_id, user_id, username, created_at, expires_at, status) VALUES (?, ?, ?, ?, ?, 'pending_totp')`,
			sessionID, userID, req.Username, now, pendingExpiry,
		)
		if err != nil {
			log.Printf("AUTH ERROR: failed to create pending session: %v", err)
			respondJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false, "error": "Internal error",
			})
			return
		}
		log.Printf("AUTH PENDING TOTP: %q from %s", req.Username, r.RemoteAddr)
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success":        true,
			"requires_totp":  true,
			"pending_token":  sessionID,
		})
		return
	}

	// Insert full active session (created_at required by schema)
	now := time.Now().Unix()
	_, err = h.db.Exec(
		`INSERT INTO sessions (session_id, user_id, username, created_at, expires_at, status) VALUES (?, ?, ?, ?, ?, 'active')`,
		sessionID, userID, req.Username, now, expiresAt,
	)
	if err != nil {
		log.Printf("AUTH ERROR: failed to create session: %v", err)
		respondJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": "Internal error",
		})
		return
	}

	// Audit log
	recordLoginSuccess(clientIP)
	h.auditLog(req.Username, "login", "Session created", r.RemoteAddr)

	log.Printf("AUTH OK: %q from %s", req.Username, r.RemoteAddr)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":              true,
		"session_id":           sessionID,
		"username":             req.Username,
		"expires_at":           expiresAt,
		"must_change_password": mustChange == 1,
	})
}

// --- POST /api/auth/logout ---

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("X-Session-ID")
	username := r.Header.Get("X-User")

	if sessionID != "" {
		h.db.Exec(`DELETE FROM sessions WHERE session_id = ?`, sessionID)
		h.auditLog(username, "logout", "Session destroyed", r.RemoteAddr)
		log.Printf("LOGOUT: %q from %s", username, r.RemoteAddr)
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
	})
}

// --- GET /api/auth/check ---

func (h *AuthHandler) Check(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("X-Session-ID")

	if sessionID == "" {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"authenticated": false,
		})
		return
	}

	var username string
	var expiresAt int64
	err := h.db.QueryRow(
		`SELECT username, COALESCE(expires_at, 0) FROM sessions 
		 WHERE session_id = ? AND (expires_at IS NULL OR expires_at > ?)`,
		sessionID, time.Now().Unix(),
	).Scan(&username, &expiresAt)

	if err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"authenticated": false,
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"authenticated": true,
		"user": map[string]interface{}{
			"username": username,
		},
	})
}

// --- GET /api/auth/session ---

func (h *AuthHandler) Session(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("X-Session-ID")

	if sessionID == "" {
		respondJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"success": false, "error": "No session",
		})
		return
	}

	var username, email, role string
	var userID int
	var mustChange int
	err := h.db.QueryRow(
		`SELECT u.id, u.username, COALESCE(u.email,''), COALESCE(u.role,'user'), COALESCE(u.must_change_password,0)
		 FROM sessions s JOIN users u ON s.username = u.username
		 WHERE s.session_id = ? AND (s.expires_at IS NULL OR s.expires_at > ?) AND u.active = 1`,
		sessionID, time.Now().Unix(),
	).Scan(&userID, &username, &email, &role, &mustChange)

	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"success": false, "error": "Invalid session",
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"user": map[string]interface{}{
			"id":                    userID,
			"username":              username,
			"email":                 email,
			"role":                  role,
			"must_change_password":  mustChange == 1,
		},
	})
}

// --- POST /api/auth/change-password ---

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.Header.Get("X-Session-ID")
	if sessionID == "" {
		respondJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"success": false, "error": "Not authenticated",
		})
		return
	}

	// Get username from session
	var username string
	err := h.db.QueryRow(
		`SELECT username FROM sessions WHERE session_id = ? AND (expires_at IS NULL OR expires_at > ?)`,
		sessionID, time.Now().Unix(),
	).Scan(&username)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"success": false, "error": "Invalid session",
		})
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": "Invalid request",
		})
		return
	}

	// Trim accidental leading/trailing whitespace (copy-paste from terminal)
	req.CurrentPassword = strings.TrimSpace(req.CurrentPassword)
	req.NewPassword = strings.TrimSpace(req.NewPassword)

	// Validate password strength (complexity requirements)
	if ok, msg := validatePasswordStrength(req.NewPassword); !ok {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false, "error": msg,
		})
		return
	}

	// Verify current password
	var storedHash string
	err = h.db.QueryRow(`SELECT password_hash FROM users WHERE username = ?`, username).Scan(&storedHash)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": "Internal error",
		})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(req.CurrentPassword)); err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"success": false, "error": "Current password is incorrect",
		})
		return
	}

	// Hash new password
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": "Internal error",
		})
		return
	}

	// Update
	_, err = h.db.Exec(
		`UPDATE users SET password_hash = ?, must_change_password = 0 WHERE username = ?`,
		string(newHash), username,
	)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false, "error": "Failed to update password",
		})
		return
	}

	h.auditLog(username, "password_changed", "Password changed", r.RemoteAddr)
	log.Printf("PASSWORD CHANGED: %q from %s", username, r.RemoteAddr)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Password changed successfully",
	})
}

// --- GET /api/csrf ---

func (h *AuthHandler) CSRFToken(w http.ResponseWriter, r *http.Request) {
	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"csrf_token": token,
	})
}

// --- Helpers ---

func (h *AuthHandler) auditLog(user, action, details, ip string) {
	h.db.Exec(
		`INSERT INTO audit_logs (user, action, details, ip_address) VALUES (?, ?, ?, ?)`,
		user, action, details, ip,
	)
}


func isAlphanumericDash(s string) bool {
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return len(s) > 0
}

// CleanExpiredSessions removes expired sessions (call periodically)
func (h *AuthHandler) CleanExpiredSessions() {
	result, err := h.db.Exec(`DELETE FROM sessions WHERE expires_at IS NOT NULL AND expires_at < ?`, time.Now().Unix())
	if err != nil {
		log.Printf("Session cleanup error: %v", err)
		return
	}
	if count, _ := result.RowsAffected(); count > 0 {
		log.Printf("Cleaned %d expired sessions", count)
	}
}

