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
	"sync"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/secrets"
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
	totpIssuer     = "DPlaneOS"
	totpDigits     = 6
	totpPeriod     = 30 // seconds
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
	_, ok := matchTOTPStep(secret, code, time.Now())
	return ok
}

// matchTOTPStep returns the time step (counter) the code belongs to.
func matchTOTPStep(secret, code string, now time.Time) (int64, bool) {
	for _, offset := range []int{-1, 0, 1} {
		t := now.Add(time.Duration(offset) * time.Duration(totpPeriod) * time.Second)
		expected, err := computeTOTP(secret, t)
		if err != nil {
			continue
		}
		if hmac.Equal([]byte(expected), []byte(code)) {
			return t.Unix() / totpPeriod, true
		}
	}
	return 0, false
}

// totpGuard limits second-factor guessing per user (a new pending token
// only takes the password, so a per-token limit would not hold) and
// refuses a code whose time step was already used (replay).
type totpGuard struct {
	mu       sync.Mutex
	fails    map[int][]time.Time
	lastStep map[int]int64
}

const (
	totpMaxFailures = 5
	totpFailWindow  = 15 * time.Minute
)

var totpAttempts = &totpGuard{fails: map[int][]time.Time{}, lastStep: map[int]int64{}}

// locked reports whether the user has too many recent failures.
func (g *totpGuard) locked(userID int, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	recent := g.fails[userID][:0]
	for _, t := range g.fails[userID] {
		if now.Sub(t) < totpFailWindow {
			recent = append(recent, t)
		}
	}
	g.fails[userID] = recent
	return len(recent) >= totpMaxFailures
}

func (g *totpGuard) fail(userID int, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fails[userID] = append(g.fails[userID], now)
}

// useStep records a successful code; false if its step (or a later one)
// was used already.
func (g *totpGuard) useStep(userID int, step int64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if step <= g.lastStep[userID] {
		return false
	}
	g.lastStep[userID] = step
	delete(g.fails, userID)
	return true
}

func (g *totpGuard) succeeded(userID int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, userID)
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

// HandleTOTPSetup - GET: get setup info (secret + QR URI), POST: verify & enable
func (h *TOTPHandler) HandleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	if user == "" {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var userID int
	if err := h.db.QueryRow(`SELECT id FROM users WHERE username = $1`, user).Scan(&userID); err != nil {
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
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// getTOTPSetup: returns current 2FA status and, if not yet enabled, a new setup secret
func (h *TOTPHandler) getTOTPSetup(w http.ResponseWriter, userID int, username string) {
	var secret string
	var enabled int
	err := h.db.QueryRow(`SELECT secret, enabled FROM totp_secrets WHERE user_id = $1`, userID).
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
			sealedSecret, sealErr := secrets.Seal(secret)
			if sealErr != nil {
				respondErrorSimple(w, "Failed to encrypt TOTP secret", http.StatusInternalServerError)
				return
			}
			if _, err := h.db.Exec(`INSERT INTO totp_secrets (user_id, secret, enabled) VALUES ($1, $2, 0)
				ON CONFLICT(user_id) DO UPDATE SET secret=EXCLUDED.secret, enabled=EXCLUDED.enabled`,
				userID, sealedSecret); err != nil {
				log.Printf("TOTP SECRET INSERT ERROR: %v", err)
				respondErrorSimple(w, "Failed to store TOTP secret", http.StatusInternalServerError)
				return
			}
		} else {
			// Decrypt the stored secret so we can build the QR URI.
			plain, openErr := secrets.Open(secret)
			if openErr != nil {
				respondErrorSimple(w, "Failed to decrypt TOTP secret", http.StatusInternalServerError)
				return
			}
			secret = plain
		}
		// Build otpauth:// URI for QR code
		otpauthURI := buildOTPAuthURI(username, secret)
		respondJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"enabled":     false,
			"secret":      secret,
			"otpauth_uri": otpauthURI,
		})
		return
	}

	if err != nil {
		respondErrorSimple(w, "Failed to check 2FA status", http.StatusInternalServerError)
		return
	}

	// 2FA already enabled
	respondJSON(w, http.StatusOK, map[string]any{
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

	var sealedSecret string
	var enabled int
	if err := h.db.QueryRow(`SELECT secret, enabled FROM totp_secrets WHERE user_id = $1`, userID).
		Scan(&sealedSecret, &enabled); err != nil {
		respondErrorSimple(w, "No 2FA setup in progress - request setup first", http.StatusBadRequest)
		return
	}
	if enabled == 1 {
		respondErrorSimple(w, "2FA is already enabled", http.StatusConflict)
		return
	}
	secret, openErr := secrets.Open(sealedSecret)
	if openErr != nil {
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if !validateTOTP(secret, req.Code) {
		respondErrorSimple(w, "Invalid code - check your authenticator app's time sync", http.StatusBadRequest)
		return
	}

	// Generate backup codes
	plainCodes, hashedCodes, err := generateBackupCodes()
	if err != nil {
		respondErrorSimple(w, "Failed to generate backup codes", http.StatusInternalServerError)
		return
	}

	_, err = h.db.Exec(`
		UPDATE totp_secrets SET enabled = 1, backup_codes = $1, verified_at = NOW()
		WHERE user_id = $2
	`, hashedCodes, userID)
	if err != nil {
		respondErrorSimple(w, "Failed to enable 2FA", http.StatusInternalServerError)
		log.Printf("TOTP ENABLE ERROR: %v", err)
		return
	}

	// Also mark user as having 2FA
	if _, err := h.db.Exec(`UPDATE users SET totp_enabled = 1 WHERE id = $1`, userID); err != nil {
		log.Printf("TOTP USER UPDATE ERROR: %v", err)
	}

	audit.LogAction("2fa", username, "Two-factor authentication enabled", true, 0)
	respondJSON(w, http.StatusOK, map[string]any{
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
	if err := h.db.QueryRow(`SELECT password_hash FROM users WHERE id = $1`, userID).Scan(&hash); err != nil {
		log.Printf("TOTP PASSWORD LOOKUP ERROR: %v", err)
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		respondErrorSimple(w, "Incorrect password", http.StatusUnauthorized)
		return
	}

	// Validate TOTP or backup code
	var sealedTOTPSecret, backupCodes string
	var enabled int
	if err := h.db.QueryRow(`SELECT secret, enabled, backup_codes FROM totp_secrets WHERE user_id = $1`, userID).
		Scan(&sealedTOTPSecret, &enabled, &backupCodes); err != nil {
		log.Printf("TOTP SECRET LOOKUP ERROR: %v", err)
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if enabled == 0 {
		respondErrorSimple(w, "2FA is not enabled", http.StatusBadRequest)
		return
	}

	totpSecret, openErr := secrets.Open(sealedTOTPSecret)
	if openErr != nil {
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if !validateTOTP(totpSecret, req.Code) {
		respondErrorSimple(w, "Invalid authenticator code", http.StatusUnauthorized)
		return
	}

	if _, err := h.db.Exec(`DELETE FROM totp_secrets WHERE user_id = $1`, userID); err != nil {
		log.Printf("TOTP DELETE ERROR: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE users SET totp_enabled = 0 WHERE id = $1`, userID); err != nil {
		log.Printf("TOTP DISABLE USER ERROR: %v", err)
	}

	audit.LogAction("2fa", username, "Two-factor authentication disabled", true, 0)
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Two-factor authentication has been disabled",
	})
}

// HandleTOTPVerify - called during login step 2
// POST /api/auth/totp-verify
func (h *TOTPHandler) HandleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
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
		WHERE s.session_id = $1 AND s.status = 'pending_totp'
		AND (s.expires_at IS NULL OR s.expires_at > $2)
	`, req.PendingToken, time.Now().Unix()).Scan(&userID, &username)
	if err != nil {
		respondErrorSimple(w, "Invalid or expired pending session", http.StatusUnauthorized)
		return
	}

	// Get TOTP secret
	var sealedVerifySecret, backupCodes string
	if err := h.db.QueryRow(`SELECT secret, backup_codes FROM totp_secrets WHERE user_id = $1 AND enabled = 1`, userID).
		Scan(&sealedVerifySecret, &backupCodes); err != nil {
		log.Printf("TOTP VERIFY LOOKUP ERROR: %v", err)
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}
	plainVerifySecret, openErr := secrets.Open(sealedVerifySecret)
	if openErr != nil {
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	if totpAttempts.locked(userID, now) {
		// The pending login is spent; a new one needs the password again.
		h.db.Exec(`DELETE FROM sessions WHERE session_id = $1`, req.PendingToken) //nolint:errcheck
		audit.LogAction("auth", username, "2FA locked: too many failed codes", false, 0)
		respondErrorSimple(w, "Too many failed codes - try again in 15 minutes", http.StatusTooManyRequests)
		return
	}

	valid := false
	if step, ok := matchTOTPStep(plainVerifySecret, req.Code, now); ok {
		valid = totpAttempts.useStep(userID, step) // a replayed code counts as a failure
	} else if len(req.Code) == 8 {
		// Check backup codes if TOTP failed
		valid = h.validateAndConsumeBackupCode(userID, req.Code, backupCodes)
		if valid {
			totpAttempts.succeeded(userID)
		}
	}

	if !valid {
		totpAttempts.fail(userID, now)
		respondErrorSimple(w, "Invalid authentication code", http.StatusUnauthorized)
		return
	}

	// Upgrade pending session to full active session at AAL2 (password + TOTP).
	// Exactly one row: a pending token used twice (double submit) must not
	// hand out a second, non-existent session id.
	sessionID, err := generateSessionID()
	if err != nil {
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}
	res, err := h.db.Exec(
		`UPDATE sessions SET session_id = $1, status = 'active', aal = 2 WHERE session_id = $2 AND status = 'pending_totp'`,
		sessionID, req.PendingToken)
	if err != nil {
		log.Printf("TOTP SESSION UPGRADE ERROR: %v", err)
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		respondErrorSimple(w, "Invalid or expired pending session", http.StatusUnauthorized)
		return
	}

	// Return the new full session
	if _, err := h.db.Exec(`UPDATE users SET last_login = NOW() WHERE id = $1`, userID); err != nil {
		log.Printf("TOTP LAST LOGIN UPDATE ERROR: %v", err)
	}
	audit.LogAction("auth", username, "2FA verification successful - logged in", true, 0)

	// Get session expiry
	var expiresAt int64
	if err := h.db.QueryRow(`SELECT COALESCE(expires_at, 0) FROM sessions WHERE session_id = $1`, sessionID).Scan(&expiresAt); err != nil {
		log.Printf("TOTP SESSION EXPIRY ERROR: %v", err)
	}

	respondJSON(w, http.StatusOK, map[string]any{
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
			// Consume: replace this hash with empty string. Compare-and-swap
			// on the old list, so two concurrent requests with the same code
			// cannot both succeed; a code that cannot be consumed is refused.
			hashes[i] = ""
			res, err := h.db.Exec(`UPDATE totp_secrets SET backup_codes = $1 WHERE user_id = $2 AND backup_codes = $3`,
				strings.Join(hashes, ","), userID, storedHashes)
			if err != nil {
				log.Printf("TOTP BACKUP CONSUME ERROR: %v", err)
				return false
			}
			n, _ := res.RowsAffected()
			return n == 1
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
