package handlers

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dplaned/internal/audit"
	"golang.org/x/crypto/bcrypt"
)

// TOTPHandler manages TOTP-based two-factor authentication
type TOTPHandler struct {
	db *sql.DB
}

func NewTOTPHandler(db *sql.DB) *TOTPHandler {
	return &TOTPHandler{db: db}
}

const (
	totpIssuer   = "D-PlaneOS"
	totpDigits   = 6
	totpPeriod   = 30  // seconds
	numBackupCodes = 8
)

// generateTOTPSecret creates a random 20-byte base32-encoded secret
func generateTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// computeTOTP computes the TOTP code for a given secret and time
func computeTOTP(secret string, t time.Time) (string, error) {
	// Decode base32 secret
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(
		strings.ToUpper(secret),
	)
	if err != nil {
		return "", fmt.Errorf("invalid secret: %w", err)
	}

	// Counter = floor(unix / period)
	counter := uint64(t.Unix()) / uint64(totpPeriod)

	// HMAC-SHA1
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	h := mac.Sum(nil)

	// Dynamic truncation
	offset := h[len(h)-1] & 0x0f
	code := (uint32(h[offset])&0x7f)<<24 |
		uint32(h[offset+1])<<16 |
		uint32(h[offset+2])<<8 |
		uint32(h[offset+3])

	// 6-digit code
	otp := code % uint32(math.Pow10(totpDigits))
	return fmt.Sprintf("%0*d", totpDigits, otp), nil
}

// validateTOTP checks the current window ±1 step for clock drift tolerance
func validateTOTP(secret, code string) bool {
	now := time.Now()
	for _, offset := range []int{-1, 0, 1} {
		t := now.Add(time.Duration(offset) * time.Duration(totpPeriod) * time.Second)
		expected, err := computeTOTP(secret, t)
		if err != nil {
			continue
		}
		if hmac.Equal([]byte(expected), []byte(code)) {
			return true
		}
	}
	return false
}

// generateBackupCodes creates 8 single-use 8-char backup codes
func generateBackupCodes() ([]string, string, error) {
	codes := make([]string, numBackupCodes)
	hashes := make([]string, numBackupCodes)
	for i := range codes {
		raw := make([]byte, 5)
		rand.Read(raw)
		code := fmt.Sprintf("%X", raw)[:8]
		codes[i] = code
		// Hash each for storage
		h, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.MinCost)
		if err != nil {
			return nil, "", err
		}
		hashes[i] = string(h)
	}
	return codes, strings.Join(hashes, ","), nil
}

// --- HTTP Handlers ---

// HandleTOTPSetup — GET: get setup info (secret + QR URI), POST: verify & enable
func (h *TOTPHandler) HandleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	if user == "" {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var userID int
	if err := h.db.QueryRow(`SELECT id FROM users WHERE username = ?`, user).Scan(&userID); err != nil {
		respondErrorSimple(w, "User not found", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getTOTPSetup(w, userID, user)
	case http.MethodPost:
		h.verifyAndEnable(w, r, userID, user)
	case http.MethodDelete:
		h.disableTOTP(w, r, userID, user)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// getTOTPSetup: returns current 2FA status and, if not yet enabled, a new setup secret
func (h *TOTPHandler) getTOTPSetup(w http.ResponseWriter, userID int, username string) {
	var secret string
	var enabled int
	err := h.db.QueryRow(`SELECT secret, enabled FROM totp_secrets WHERE user_id = ?`, userID).
		Scan(&secret, &enabled)

	if err == sql.ErrNoRows || (err == nil && enabled == 0) {
		// Generate or reuse pending secret
		if err == sql.ErrNoRows || secret == "" {
			var genErr error
			secret, genErr = generateTOTPSecret()
			if genErr != nil {
				respondErrorSimple(w, "Failed to generate secret", http.StatusInternalServerError)
				return
			}
			h.db.Exec(`INSERT OR REPLACE INTO totp_secrets (user_id, secret, enabled) VALUES (?, ?, 0)`,
				userID, secret)
		}
		// Build otpauth:// URI for QR code
		otpauthURI := buildOTPAuthURI(username, secret)
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success":       true,
			"enabled":       false,
			"secret":        secret,
			"otpauth_uri":   otpauthURI,
		})
		return
	}

	if err != nil {
		respondErrorSimple(w, "Failed to check 2FA status", http.StatusInternalServerError)
		return
	}

	// 2FA already enabled
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"enabled": enabled == 1,
	})
}

func buildOTPAuthURI(username, secret string) string {
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&digits=%d&period=%d",
		url.QueryEscape(totpIssuer),
		url.QueryEscape(username),
		secret,
		url.QueryEscape(totpIssuer),
		totpDigits,
		totpPeriod,
	)
}

// verifyAndEnable: user provides their authenticator code to confirm setup
func (h *TOTPHandler) verifyAndEnable(w http.ResponseWriter, r *http.Request, userID int, username string) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Code) != 6 {
		respondErrorSimple(w, "A 6-digit code is required", http.StatusBadRequest)
		return
	}

	var secret string
	var enabled int
	if err := h.db.QueryRow(`SELECT secret, enabled FROM totp_secrets WHERE user_id = ?`, userID).
		Scan(&secret, &enabled); err != nil {
		respondErrorSimple(w, "No 2FA setup in progress — request setup first", http.StatusBadRequest)
		return
	}
	if enabled == 1 {
		respondErrorSimple(w, "2FA is already enabled", http.StatusConflict)
		return
	}

	if !validateTOTP(secret, req.Code) {
		respondErrorSimple(w, "Invalid code — check your authenticator app's time sync", http.StatusBadRequest)
		return
	}

	// Generate backup codes
	plainCodes, hashedCodes, err := generateBackupCodes()
	if err != nil {
		respondErrorSimple(w, "Failed to generate backup codes", http.StatusInternalServerError)
		return
	}

	_, err = h.db.Exec(`
		UPDATE totp_secrets SET enabled = 1, backup_codes = ?, verified_at = CURRENT_TIMESTAMP
		WHERE user_id = ?
	`, hashedCodes, userID)
	if err != nil {
		respondErrorSimple(w, "Failed to enable 2FA", http.StatusInternalServerError)
		log.Printf("TOTP ENABLE ERROR: %v", err)
		return
	}

	// Also mark user as having 2FA
	h.db.Exec(`UPDATE users SET totp_enabled = 1 WHERE id = ?`, userID)

	audit.LogAction("2fa", username, "Two-factor authentication enabled", true, 0)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"message":      "Two-factor authentication enabled",
		"backup_codes": plainCodes, // shown ONCE only
	})
}

// disableTOTP requires current TOTP code to disable
func (h *TOTPHandler) disableTOTP(w http.ResponseWriter, r *http.Request, userID int, username string) {
	var req struct {
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Require current password for extra security
	var hash string
	h.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash)
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		respondErrorSimple(w, "Incorrect password", http.StatusUnauthorized)
		return
	}

	// Validate TOTP or backup code
	var secret, backupCodes string
	var enabled int
	h.db.QueryRow(`SELECT secret, enabled, backup_codes FROM totp_secrets WHERE user_id = ?`, userID).
		Scan(&secret, &enabled, &backupCodes)

	if enabled == 0 {
		respondErrorSimple(w, "2FA is not enabled", http.StatusBadRequest)
		return
	}

	if !validateTOTP(secret, req.Code) {
		respondErrorSimple(w, "Invalid authenticator code", http.StatusUnauthorized)
		return
	}

	h.db.Exec(`DELETE FROM totp_secrets WHERE user_id = ?`, userID)
	h.db.Exec(`UPDATE users SET totp_enabled = 0 WHERE id = ?`, userID)

	audit.LogAction("2fa", username, "Two-factor authentication disabled", true, 0)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Two-factor authentication has been disabled",
	})
}

// HandleTOTPVerify — called during login step 2
// POST /api/auth/totp-verify
func (h *TOTPHandler) HandleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		PendingToken string `json:"pending_token"`
		Code         string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.PendingToken == "" || req.Code == "" {
		respondErrorSimple(w, "pending_token and code are required", http.StatusBadRequest)
		return
	}

	// Look up the pending token (stored in sessions with status='pending_totp')
	var userID int
	var username string
	err := h.db.QueryRow(`
		SELECT u.id, u.username FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.session_id = ? AND s.status = 'pending_totp'
		AND (s.expires_at IS NULL OR s.expires_at > ?)
	`, req.PendingToken, time.Now().Unix()).Scan(&userID, &username)
	if err != nil {
		respondErrorSimple(w, "Invalid or expired pending session", http.StatusUnauthorized)
		return
	}

	// Get TOTP secret
	var secret, backupCodes string
	h.db.QueryRow(`SELECT secret, backup_codes FROM totp_secrets WHERE user_id = ? AND enabled = 1`, userID).
		Scan(&secret, &backupCodes)

	valid := validateTOTP(secret, req.Code)

	// Check backup codes if TOTP failed
	if !valid && len(req.Code) == 8 {
		valid = h.validateAndConsumeBackupCode(userID, req.Code, backupCodes)
	}

	if !valid {
		respondErrorSimple(w, "Invalid authentication code", http.StatusUnauthorized)
		return
	}

	// Upgrade pending session to full session
	sessionID, _ := generateSessionID()
	h.db.Exec(`UPDATE sessions SET session_id = ?, status = 'active' WHERE session_id = ?`,
		sessionID, req.PendingToken)

	// Return the new full session
	h.db.Exec(`UPDATE users SET last_login = CURRENT_TIMESTAMP WHERE id = ?`, userID)
	audit.LogAction("auth", username, "2FA verification successful — logged in", true, 0)

	// Get session expiry
	var expiresAt int64
	h.db.QueryRow(`SELECT COALESCE(expires_at, 0) FROM sessions WHERE session_id = ?`, sessionID).Scan(&expiresAt)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"session_id": sessionID,
		"username":   username,
		"expires_at": expiresAt,
	})
}

func (h *TOTPHandler) validateAndConsumeBackupCode(userID int, code, storedHashes string) bool {
	if storedHashes == "" {
		return false
	}
	hashes := strings.Split(storedHashes, ",")
	for i, hash := range hashes {
		if hash == "" {
			continue
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(code)); err == nil {
			// Consume: replace this hash with empty string
			hashes[i] = ""
			h.db.Exec(`UPDATE totp_secrets SET backup_codes = ? WHERE user_id = ?`,
				strings.Join(hashes, ","), userID)
			return true
		}
	}
	return false
}

// generateSessionID creates a new secure session ID
func generateSessionID() (string, error) {
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	return fmt.Sprintf("%x", raw), err
}
