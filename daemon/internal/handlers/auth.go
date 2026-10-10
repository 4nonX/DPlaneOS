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

	"dplaned/internal/audit"
	ldapinternal "dplaned/internal/ldap"
	"dplaned/internal/middleware"
	"dplaned/internal/scram"
	"dplaned/internal/secrets"
	"dplaned/internal/security"
	"golang.org/x/crypto/bcrypt"
)

// ═══════════════════════════════════════════════════════════════
//  LOGIN RATE LIMITING - Exponential Backoff per IP
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

// InitAuth initializes background maintenance tasks for the auth system.
func InitAuth() {
	// Periodic cleanup of the loginAttempts map to prevent memory leaks (Finding 24)
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			loginAttemptsMu.Lock()
			now := time.Now()
			for ip, attempt := range loginAttempts {
				if now.Sub(attempt.lastAttempt) > 15*time.Minute {
					delete(loginAttempts, ip)
				}
			}
			loginAttemptsMu.Unlock()
		}
	}()
}

// getLoginDelay returns the lockout duration based on failure count:
// 1 fail = 0s, 2 = 2s, 3 = 4s, 4 = 8s, 5 = 16s, 6+ = 30s (cap)
func getLoginDelay(failures int) time.Duration {
	if failures <= 1 {
		return 0
	}
	// Cap before shifting: 1<<(n-1) seconds overflows time.Duration around
	// the 35th failure and turned the lockout negative (no throttling).
	if failures >= 6 {
		return 30 * time.Second
	}
	return time.Duration(1<<uint(failures-1)) * time.Second
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
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// === LOGIN RATE LIMITING === (Finding 27)
	clientIP := security.RealIP(r)
	if throttled, remaining := checkLoginThrottle(clientIP); throttled {
		h.auditLog("", "login_throttled", fmt.Sprintf("IP %s throttled for %.0fs", clientIP, remaining.Seconds()), clientIP)
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(remaining.Seconds())+1))
		respondJSON(w, http.StatusTooManyRequests, map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Too many failed attempts. Try again in %d seconds.", int(remaining.Seconds())+1),
		})
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "error": "Invalid request body",
		})
		return
	}

	// Allowlist validation
	if !isAlphanumericDash(req.Username) || len(req.Username) > 64 {
		respondJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "error": "Invalid username format",
		})
		return
	}

	// Lookup user
	var userID int
	var storedHash, source string
	var active, mustChange int
	err := h.db.QueryRow(
		`SELECT id, password_hash, active, COALESCE(must_change_password, 0), COALESCE(source,'local') FROM users WHERE username = $1 LIMIT 1`,
		req.Username,
	).Scan(&userID, &storedHash, &active, &mustChange, &source)

	if err == sql.ErrNoRows {
		// Constant-time: still do a bcrypt compare to prevent timing attacks
		bcrypt.CompareHashAndPassword([]byte("$2a$10$dummyhashfortimingoracle000000000000000000000000000000"), []byte(req.Password))
		recordLoginFailure(clientIP)
		log.Printf("AUTH FAIL: unknown user %q from %s", req.Username, clientIP)
		respondJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false, "error": "Invalid credentials",
		})
		return
	} else if err != nil {
		log.Printf("AUTH ERROR: db query failed: %v", err)
		respondJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false, "error": "Internal error",
		})
		return
	}

	if active != 1 {
		recordLoginFailure(clientIP)
		log.Printf("AUTH FAIL: disabled user %q from %s", req.Username, clientIP)
		respondJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false, "error": "Account disabled",
		})
		return
	}

	// Verify password - LDAP users bind against the directory; local users use bcrypt
	if source == "ldap" {
		if authErr := h.ldapAuthenticate(req.Username, req.Password); authErr != nil {
			recordLoginFailure(clientIP)
			log.Printf("AUTH FAIL: LDAP bind failed for %q from %s: %v", req.Username, clientIP, authErr)
			respondJSON(w, http.StatusUnauthorized, map[string]any{
				"success": false, "error": "Invalid credentials",
			})
			return
		}
	} else {
		if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(req.Password)); err != nil {
			recordLoginFailure(clientIP)
			log.Printf("AUTH FAIL: wrong password for %q from %s", req.Username, clientIP)
			respondJSON(w, http.StatusUnauthorized, map[string]any{
				"success": false, "error": "Invalid credentials",
			})
			return
		}
	}

	// Generate session token (32 bytes = 64 hex chars)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		log.Printf("AUTH ERROR: failed to generate session token: %v", err)
		respondJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false, "error": "Internal error",
		})
		return
	}
	sessionID := hex.EncodeToString(tokenBytes)

	// Check if user has TOTP enabled
	var totpEnabled int
	h.db.QueryRow(`SELECT COALESCE(totp_enabled, 0) FROM users WHERE id = $1`, userID).Scan(&totpEnabled)

	// Session expires in 24 hours
	expiresAt := time.Now().Add(24 * time.Hour).Unix()

	if totpEnabled == 1 {
		// Create a short-lived pending session (5 minutes) for TOTP verification
		pendingExpiry := time.Now().Add(5 * time.Minute).Unix()
		pendingCreated := time.Now().Unix()
		_, err = h.db.Exec(
			`INSERT INTO sessions (session_id, user_id, username, created_at, expires_at, status) VALUES ($1, $2, $3, $4, $5, 'pending_totp')`,
			sessionID, userID, req.Username, pendingCreated, pendingExpiry,
		)
		if err != nil {
			log.Printf("AUTH ERROR: failed to create pending session: %v", err)
			respondJSON(w, http.StatusInternalServerError, map[string]any{
				"success": false, "error": "Internal error",
			})
			return
		}
		log.Printf("AUTH PENDING TOTP: %q from %s", req.Username, clientIP)
		respondJSON(w, http.StatusOK, map[string]any{
			"success":       true,
			"requires_totp": true,
			"pending_token": sessionID,
		})
		return
	}

	// Insert session. Restricted status when must_change_password is set so the
	// middleware rejects all endpoints except change-password until the user complies.
	sessionStatus := "active"
	if mustChange == 1 {
		sessionStatus = "must_change_password"
	}
	ip := clientIP
	userAgent := r.Header.Get("User-Agent")
	createdAt := time.Now().Unix()
	_, err = h.db.Exec(
		`INSERT INTO sessions (session_id, user_id, username, ip_address, user_agent, created_at, expires_at, status) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		sessionID, userID, req.Username, ip, userAgent, createdAt, expiresAt, sessionStatus,
	)
	if err != nil {
		log.Printf("AUTH ERROR: failed to create session: %v", err)
		respondJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false, "error": "Internal error",
		})
		return
	}

	// Audit log
	recordLoginSuccess(clientIP)
	h.auditLog(req.Username, "login", "Session created", clientIP)

	log.Printf("AUTH OK: %q from %s", req.Username, clientIP)
	if _, err := h.db.Exec(`UPDATE users SET last_login = NOW() WHERE id = $1`, userID); err != nil {
		log.Printf("AUTH: failed to update last_login for user %d: %v", userID, err)
	}

	respondJSON(w, http.StatusOK, map[string]any{
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
		if _, err := h.db.Exec(`DELETE FROM sessions WHERE session_id = $1`, sessionID); err != nil {
			log.Printf("AUTH: failed to delete session: %v", err)
		}
		clientIP := security.RealIP(r)
		h.auditLog(username, "logout", "Session destroyed", clientIP)
		log.Printf("LOGOUT: %q from %s", username, clientIP)
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
	})
}

// --- GET /api/auth/check ---

func (h *AuthHandler) Check(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("X-Session-ID")

	if sessionID == "" {
		respondJSON(w, http.StatusOK, map[string]any{
			"authenticated": false,
		})
		return
	}

	var username string
	var expiresAt int64
	err := h.db.QueryRow(
		`SELECT username, COALESCE(expires_at, 0) FROM sessions 
		 WHERE session_id = $1 AND (expires_at IS NULL OR expires_at > $2)`,
		sessionID, time.Now().Unix(),
	).Scan(&username, &expiresAt)

	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"authenticated": false,
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user": map[string]any{
			"username": username,
		},
	})
}

// --- GET /api/auth/session ---

func (h *AuthHandler) Session(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("X-Session-ID")

	if sessionID == "" {
		respondJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false, "error": "No session",
		})
		return
	}

	var username, email, role, source string
	var userID int
	var mustChange int
	err := h.db.QueryRow(
		`SELECT u.id, u.username, COALESCE(u.email,''), COALESCE(u.role,'user'), COALESCE(u.must_change_password,0), COALESCE(u.source,'local')
		 FROM sessions s JOIN users u ON s.username = u.username
		 WHERE s.session_id = $1 AND (s.expires_at IS NULL OR s.expires_at > $2) AND u.active = 1`,
		sessionID, time.Now().Unix(),
	).Scan(&userID, &username, &email, &role, &mustChange, &source)

	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false, "error": "Invalid session",
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"user": map[string]any{
			"id":                   userID,
			"username":             username,
			"email":                email,
			"role":                 role,
			"must_change_password": mustChange == 1,
			"source":               source,
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
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.Header.Get("X-Session-ID")
	if sessionID == "" {
		respondJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false, "error": "Not authenticated",
		})
		return
	}

	// Get username from session
	var username string
	err := h.db.QueryRow(
		`SELECT username FROM sessions WHERE session_id = $1 AND (expires_at IS NULL OR expires_at > $2)`,
		sessionID, time.Now().Unix(),
	).Scan(&username)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false, "error": "Invalid session",
		})
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "error": "Invalid request",
		})
		return
	}

	// Trim accidental leading/trailing whitespace (copy-paste from terminal)
	req.CurrentPassword = strings.TrimSpace(req.CurrentPassword)
	req.NewPassword = strings.TrimSpace(req.NewPassword)

	// Validate password strength (complexity requirements)
	if ok, msg := validatePasswordStrength(req.NewPassword); !ok {
		respondJSON(w, http.StatusBadRequest, map[string]any{
			"success": false, "error": msg,
		})
		return
	}

	// Verify current password
	var storedHash string
	err = h.db.QueryRow(`SELECT password_hash FROM users WHERE username = $1`, username).Scan(&storedHash)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false, "error": "Internal error",
		})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(req.CurrentPassword)); err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false, "error": "Current password is incorrect",
		})
		return
	}

	// Hash new password with bcrypt (backward compat) and derive SCRAM-SHA-512 keys.
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false, "error": "Internal error",
		})
		return
	}
	scramKeys, err := scram.Derive(req.NewPassword)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false, "error": "Internal error",
		})
		return
	}

	// Update - store both bcrypt hash and SCRAM keys atomically
	_, err = h.db.Exec(
		`UPDATE users SET
			password_hash     = $1,
			scram_salt        = $2,
			scram_iterations  = $3,
			scram_stored_key  = $4,
			scram_server_key  = $5,
			must_change_password = 0
		WHERE username = $6`,
		string(newHash),
		scram.EncodeBase64(scramKeys.Salt),
		scramKeys.Iterations,
		scram.EncodeBase64(scramKeys.StoredKey),
		scram.EncodeBase64(scramKeys.ServerKey),
		username,
	)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false, "error": "Failed to update password",
		})
		return
	}

	// Revoke all other sessions - the current session remains active
	if _, err := h.db.Exec(`DELETE FROM sessions WHERE username = $1 AND session_id != $2`, username, sessionID); err != nil {
		log.Printf("AUTH: failed to revoke other sessions for %s: %v", username, err)
	}

	clientIP := security.RealIP(r)
	h.auditLog(username, "password_changed", "Password changed", clientIP)
	log.Printf("PASSWORD CHANGED: %q from %s", username, clientIP)

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Password changed successfully",
	})
}

// --- GET /api/csrf ---

// --- GET /api/csrf --- (Finding 22)
func (h *AuthHandler) CSRFToken(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("X-Session-ID")
	if sessionID == "" {
		// Fallback to cookie
		if cookie, err := r.Cookie("session_id"); err == nil {
			sessionID = cookie.Value
		}
	}

	if sessionID == "" {
		respondJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false, "error": "No session",
		})
		return
	}

	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)

	// Persist to session in DB
	_, err := h.db.Exec(`UPDATE sessions SET csrf_token = $1 WHERE session_id = $2`, token, sessionID)
	if err != nil {
		log.Printf("AUTH ERROR: failed to save CSRF token: %v", err)
		respondJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false, "error": "Internal error",
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"csrf_token": token,
	})
}

// --- Helpers ---

func (h *AuthHandler) auditLog(user, action, details, _ string) {
	audit.LogAction(action, user, details, true, 0)
}

func isAlphanumericDash(s string) bool {
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return len(s) > 0
}

// ── LDAP circuit breaker ──────────────────────────────────────────────────────
// Prevents cascading login timeouts when the LDAP server is unreachable.
// After ldapCBThreshold consecutive connection failures the breaker opens for
// ldapCBResetAfter, during which all LDAP logins fail immediately.
// Successful authentications reset the failure counter.

const (
	ldapCBThreshold  = 3                // open after this many consecutive failures
	ldapCBResetAfter = 30 * time.Second // half-open after this duration
)

var (
	ldapCBMu        sync.Mutex
	ldapCBFailures  int
	ldapCBOpenUntil time.Time
)

func ldapCBAllow() bool {
	ldapCBMu.Lock()
	defer ldapCBMu.Unlock()
	if ldapCBFailures >= ldapCBThreshold {
		if time.Now().Before(ldapCBOpenUntil) {
			return false // breaker open - reject immediately
		}
		// Half-open: allow one attempt through
		ldapCBFailures = ldapCBThreshold - 1
	}
	return true
}

func ldapCBSuccess() {
	ldapCBMu.Lock()
	ldapCBFailures = 0
	ldapCBMu.Unlock()
}

func ldapCBFailure() {
	ldapCBMu.Lock()
	ldapCBFailures++
	if ldapCBFailures >= ldapCBThreshold {
		ldapCBOpenUntil = time.Now().Add(ldapCBResetAfter)
		log.Printf("AUTH: LDAP circuit breaker opened - server unreachable (%d consecutive failures)", ldapCBFailures)
	}
	ldapCBMu.Unlock()
}

// ldapAuthenticate binds against the configured LDAP server to verify credentials
// for users whose source='ldap'. Returns nil on success, error on failure.
// Uses a circuit breaker to fail fast when the LDAP server is unreachable.
func (h *AuthHandler) ldapAuthenticate(username, password string) error {
	if !ldapCBAllow() {
		return fmt.Errorf("LDAP server unavailable (circuit breaker open) - try again shortly")
	}

	var server, bindDN, bindPassword, baseDN, userFilter, userIDAttr, userNameAttr, userEmailAttr string
	var port, useTLS, timeout int
	err := h.db.QueryRow(`
		SELECT server, port, bind_dn, COALESCE(bind_password,''), base_dn,
		       COALESCE(user_filter,'(sAMAccountName={username})'),
		       COALESCE(user_id_attribute,'sAMAccountName'),
		       COALESCE(user_name_attribute,'displayName'),
		       COALESCE(user_email_attribute,'mail'),
		       COALESCE(use_tls,1),
		       COALESCE(timeout,10)
		FROM ldap_config WHERE id=1`).Scan(
		&server, &port, &bindDN, &bindPassword, &baseDN,
		&userFilter, &userIDAttr, &userNameAttr, &userEmailAttr, &useTLS, &timeout,
	)
	if err != nil || server == "" {
		return fmt.Errorf("LDAP not configured")
	}

	plainPwd, openErr := secrets.Open(bindPassword)
	if openErr != nil {
		return fmt.Errorf("decrypting LDAP bind_password: %w", openErr)
	}

	cfg := &ldapinternal.Config{
		Server:             server,
		Port:               port,
		BindDN:             bindDN,
		BindPassword:       plainPwd,
		BaseDN:             baseDN,
		UserFilter:         userFilter,
		UserIDAttribute:    userIDAttr,
		UserNameAttribute:  userNameAttr,
		UserEmailAttribute: userEmailAttr,
		UseTLS:             useTLS == 1,
		Timeout:            timeout,
	}

	client, err := ldapinternal.NewClient(cfg)
	if err != nil {
		ldapCBFailure()
		return fmt.Errorf("LDAP client error: %w", err)
	}

	_, err = client.Authenticate(username, password)
	if err != nil {
		// Only count connection-level failures toward the breaker,
		// not credential failures (wrong password is not a server outage).
		if isLDAPConnError(err) {
			ldapCBFailure()
		} else {
			ldapCBSuccess() // server responded - reset breaker
		}
		return err
	}

	ldapCBSuccess()
	return nil
}

// isLDAPConnError returns true for errors that indicate the server is
// unreachable (connection refused, timeout, TLS handshake failure) as
// opposed to a valid "wrong credentials" response from a reachable server.
func isLDAPConnError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "tls:") ||
		strings.Contains(msg, "EOF")
}

// CleanExpiredSessions removes expired sessions (call periodically)
func (h *AuthHandler) CleanExpiredSessions() {
	result, err := h.db.Exec(`DELETE FROM sessions WHERE expires_at IS NOT NULL AND expires_at < $1`, time.Now().Unix())
	if err != nil {
		log.Printf("Session cleanup error: %v", err)
		return
	}
	if count, _ := result.RowsAffected(); count > 0 {
		log.Printf("Cleaned %d expired sessions", count)
	}
}
func (h *AuthHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// sessionUser is injected by middleware
	user, ok := r.Context().Value(middleware.UserContextKey).(*middleware.User)
	if !ok || user == nil {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	sessions, err := security.GetUserSessions(user.Username)
	if err != nil {
		log.Printf("AUTH ERROR: failed to list sessions: %v", err)
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}

	currentSession := ""
	authHeader := r.Header.Get("Authorization")
	if v, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
		currentSession = v
	} else {
		cookie, err := r.Cookie("session_id")
		if err == nil {
			currentSession = cookie.Value
		}
	}

	// Mask session IDs except for the current one (security best practice - Finding 25)
	type SessionEntry struct {
		ID           string `json:"id"`
		IPAddress    string `json:"ip_address"`
		UserAgent    string `json:"user_agent"`
		CreatedAt    int64  `json:"created_at"`
		LastActivity int64  `json:"last_activity"`
		IsCurrent    bool   `json:"is_current"`
	}
	var entries []SessionEntry
	for _, s := range sessions {
		isCurrent := s.SessionID == currentSession
		displayID := "REDACTED"
		if isCurrent {
			displayID = s.SessionID
		} else if len(s.SessionID) > 8 {
			displayID = s.SessionID[:8] + "..."
		}

		entries = append(entries, SessionEntry{
			ID:           displayID,
			IPAddress:    s.IPAddress,
			UserAgent:    s.UserAgent,
			CreatedAt:    s.CreatedAt,
			LastActivity: s.LastActivity,
			IsCurrent:    isCurrent,
		})
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"sessions": entries,
	})
}

func (h *AuthHandler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, ok := r.Context().Value(middleware.UserContextKey).(*middleware.User)
	if !ok || user == nil {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Safety: Verify the session belongs to the user
	var sessionOwner string
	err := h.db.QueryRow("SELECT username FROM sessions WHERE session_id = $1", req.ID).Scan(&sessionOwner)
	if err != nil {
		if err == sql.ErrNoRows {
			respondErrorSimple(w, "Session not found", http.StatusNotFound)
		} else {
			respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		}
		return
	}

	if sessionOwner != user.Username && user.Username != "admin" {
		respondErrorSimple(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := security.RevokeSession(req.ID); err != nil {
		log.Printf("AUTH ERROR: failed to revoke session: %v", err)
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}

	h.auditLog(user.Username, "session_revoked", fmt.Sprintf("Revoked session %s", req.ID[:8]), r.RemoteAddr)
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ═══════════════════════════════════════════════════════════════
//  SCRAM-SHA-512 Challenge/Response Authentication (RFC 5802)
//  Endpoint 1: POST /api/auth/scram/challenge
//  Endpoint 2: POST /api/auth/scram/verify
// ═══════════════════════════════════════════════════════════════

// SCRAMChallenge begins a SCRAM-SHA-512 handshake. The client sends its username
// and a random nonce; the server returns the challenge parameters needed to
// compute the client proof.
//
// POST /api/auth/scram/challenge
// Request:  {"username":"<str>", "client_nonce":"<base64url>"}
// Response: {"challenge_id":"<str>", "server_nonce":"<base64url>", "salt":"<base64>", "iterations":<int>}
func (h *AuthHandler) SCRAMChallenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string `json:"username"`
		ClientNonce string `json:"client_nonce"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid request"})
		return
	}
	if !isAlphanumericDash(req.Username) || len(req.Username) > 64 || req.ClientNonce == "" {
		respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid parameters"})
		return
	}

	// Look up SCRAM credentials. Return a synthetic challenge even for unknown users
	// to prevent username enumeration timing attacks.
	var saltB64, storedKeyB64, serverKeyB64 string
	var iterations int
	err := h.db.QueryRow(
		`SELECT COALESCE(scram_salt,''), COALESCE(scram_iterations,0),
		        COALESCE(scram_stored_key,''), COALESCE(scram_server_key,'')
		 FROM users WHERE username = $1 AND active = 1 LIMIT 1`,
		req.Username,
	).Scan(&saltB64, &iterations, &storedKeyB64, &serverKeyB64)

	hasSCRAM := err == nil && iterations > 0 && storedKeyB64 != ""

	// For users without SCRAM keys yet, use synthetic params so timing is uniform.
	if !hasSCRAM {
		saltB64 = scram.EncodeBase64(make([]byte, 16))
		iterations = scram.DefaultIterations
		storedKeyB64 = ""
		serverKeyB64 = ""
	}

	serverNonce, err := scram.RandomNonce()
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Internal error"})
		return
	}

	var storedKey, serverKey []byte
	if hasSCRAM {
		storedKey, _ = scram.DecodeBase64(storedKeyB64)
		serverKey, _ = scram.DecodeBase64(serverKeyB64)
	}

	challengeID := scram.NewChallenge(&scram.Challenge{
		Username:    req.Username,
		ClientNonce: req.ClientNonce,
		ServerNonce: serverNonce,
		StoredKey:   storedKey,
		ServerKey:   serverKey,
		SaltB64:     saltB64,
		Iterations:  iterations,
	})

	respondJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"challenge_id": challengeID,
		"server_nonce": serverNonce,
		"salt":         saltB64,
		"iterations":   iterations,
	})
}

// SCRAMVerify completes the SCRAM-SHA-512 handshake. The client computes a
// ClientProof from the challenge parameters; the server verifies it and returns
// a session token plus the ServerProof so the client can confirm server identity.
//
// POST /api/auth/scram/verify
// Request:  {"challenge_id":"<str>", "client_proof":"<base64>"}
// Response: {"success":true, "token":"<str>", "server_proof":"<base64>"}
func (h *AuthHandler) SCRAMVerify(w http.ResponseWriter, r *http.Request) {
	clientIP := security.RealIP(r)

	if throttled, wait := checkLoginThrottle(clientIP); throttled {
		respondJSON(w, http.StatusTooManyRequests, map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Too many attempts. Try again in %.0f seconds.", wait.Seconds()),
		})
		return
	}

	var req struct {
		ChallengeID string `json:"challenge_id"`
		ClientProof string `json:"client_proof"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid request"})
		return
	}

	fail := func() {
		recordLoginFailure(clientIP)
		respondJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": "Authentication failed"})
	}

	ch := scram.TakeChallenge(req.ChallengeID)
	if ch == nil {
		fail()
		return
	}

	clientProof, err := scram.DecodeBase64(req.ClientProof)
	if err != nil || len(ch.StoredKey) == 0 {
		fail()
		return
	}

	authMsg := scram.AuthMessage(ch.Username, ch.ClientNonce, ch.ServerNonce, ch.SaltB64, ch.Iterations)
	serverProof, ok := scram.Verify(ch.StoredKey, ch.ServerKey, clientProof, authMsg)
	if !ok {
		log.Printf("SCRAM FAIL: bad proof for %q from %s", ch.Username, clientIP)
		fail()
		return
	}

	// Proof valid - look up the user and create a session
	var userID int
	var active, mustChange, totpEnabled int
	err = h.db.QueryRow(
		`SELECT u.id, u.active, COALESCE(u.must_change_password,0), COALESCE(t.totp_enabled,0)
		 FROM users u LEFT JOIN totp_secrets t ON t.user_id = u.id
		 WHERE u.username = $1 LIMIT 1`, ch.Username,
	).Scan(&userID, &active, &mustChange, &totpEnabled)
	if err != nil || active != 1 {
		fail()
		return
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Internal error"})
		return
	}
	sessionID := hex.EncodeToString(tokenBytes)
	createdAt := time.Now().Unix()

	// Mirror the bcrypt login flow exactly: TOTP takes priority over must_change_password.
	if totpEnabled == 1 {
		// Issue a short-lived pending session; the client must complete TOTP via
		// POST /api/auth/totp/verify before it is promoted to 'active'.
		pendingExpiry := time.Now().Add(5 * time.Minute).Unix()
		if _, err := h.db.Exec(
			`INSERT INTO sessions (session_id, user_id, username, created_at, expires_at, status)
			 VALUES ($1, $2, $3, $4, $5, 'pending_totp')`,
			sessionID, userID, ch.Username, createdAt, pendingExpiry,
		); err != nil {
			respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Internal error"})
			return
		}
		recordLoginSuccess(clientIP)
		log.Printf("SCRAM AUTH PENDING TOTP: %q from %s", ch.Username, clientIP)
		respondJSON(w, http.StatusOK, map[string]any{
			"success":       true,
			"requires_totp": true,
			"pending_token": sessionID,
			"server_proof":  scram.EncodeBase64(serverProof),
		})
		return
	}

	scramSessionStatus := "active"
	if mustChange == 1 {
		scramSessionStatus = "must_change_password"
	}
	expiresAt := time.Now().Add(24 * time.Hour).Unix()
	_, err = h.db.Exec(
		`INSERT INTO sessions (session_id, user_id, username, ip_address, user_agent, created_at, expires_at, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		sessionID, userID, ch.Username, clientIP, r.Header.Get("User-Agent"), createdAt, expiresAt, scramSessionStatus,
	)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "Internal error"})
		return
	}

	recordLoginSuccess(clientIP)
	log.Printf("SCRAM AUTH OK: %q from %s", ch.Username, clientIP)
	audit.LogAction("scram_login", ch.Username, "SCRAM-SHA-512 authentication successful", true, 0)

	respondJSON(w, http.StatusOK, map[string]any{
		"success":              true,
		"token":                sessionID,
		"server_proof":         scram.EncodeBase64(serverProof),
		"must_change_password": mustChange == 1,
	})
}
