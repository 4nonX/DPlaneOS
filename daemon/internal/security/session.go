package security

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const dbQueryTimeout = 5 * time.Second

// db is declared in rbac.go via SetDatabase()

// InitDatabase initializes the PostgreSQL connection
func InitDatabase(dbDSN string) error {
	var err error
	db, err = sql.Open("pgx", dbDSN)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	// Set connection pool settings
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	return nil
}

// CloseDatabase closes the database connection
func CloseDatabase() error {
	if db != nil {
		return db.Close()
	}
	return nil
}

// ValidateSession checks if a session is valid in the database
func ValidateSession(sessionID, username string) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("database not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()

	// Session idle timeout: 30 minutes of inactivity = expired
	idleTimeout := int64(30 * 60) // 30 minutes in seconds
	now := time.Now().Unix()

	// Check if session exists, not expired, and not idle
	var count int
	query := `
		SELECT COUNT(*) 
		FROM sessions 
		WHERE session_id = $1 
		AND username = $2
		AND (expires_at IS NULL OR expires_at > $3)
		AND (last_activity = 0 OR ($4 - last_activity) < $5)
		AND COALESCE(status, 'active') = 'active'
	`

	err := db.QueryRowContext(ctx, query, sessionID, username, now, now, idleTimeout).Scan(&count)
	if err != nil {
		// FAIL-CLOSED: Reject on ANY error (no fallback!)
		return false, fmt.Errorf("session validation failed: %w", err)
	}

	if count > 0 {
		// Update last_activity timestamp (touch session)
		db.ExecContext(ctx, "UPDATE sessions SET last_activity = $1 WHERE session_id = $2", now, sessionID)
		return true, nil
	}

	return false, nil
}

// GetUserFromSession retrieves the username associated with a session
func GetUserFromSession(sessionID string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("database not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()

	var username string
	query := `
		SELECT username 
		FROM sessions 
		WHERE session_id = $1
		AND (expires_at IS NULL OR expires_at > $2)
		LIMIT 1
	`

	err := db.QueryRowContext(ctx, query, sessionID, time.Now().Unix()).Scan(&username)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("session not found")
		}
		return "", fmt.Errorf("failed to get user: %w", err)
	}

	return username, nil
}

// ValidateUser checks if a user exists and is active
func ValidateUser(username string) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("database not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()

	var count int
	query := `
		SELECT COUNT(*) 
		FROM users 
		WHERE username = $1
		AND active = 1
	`

	err := db.QueryRowContext(ctx, query, username).Scan(&count)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("user validation failed: %w", err)
	}

	return count > 0, nil
}

// SessionUser represents basic user info returned from session validation
type SessionUser struct {
	ID       int
	Username string
	Email    string
}

// ValidateSessionAndGetUser validates a session token and returns the associated user
func ValidateSessionAndGetUser(sessionToken string) (*SessionUser, error) {
	if db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()

	var user SessionUser
	query := `
		SELECT u.id, u.username, COALESCE(u.email, '')
		FROM sessions s
		JOIN users u ON s.username = u.username
		WHERE s.session_id = $1
		AND (s.expires_at IS NULL OR s.expires_at > $2)
		AND u.active = 1
		AND COALESCE(s.status, 'active') = 'active'
		LIMIT 1
	`

	err := db.QueryRowContext(ctx, query, sessionToken, time.Now().Unix()).Scan(
		&user.ID, &user.Username, &user.Email,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("invalid or expired session")
		}
		return nil, fmt.Errorf("session validation failed: %w", err)
	}

	return &user, nil
}

// HashToken hashes a raw token for lookup (mirrored from handlers/api_tokens.go)
func HashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// ValidateAPITokenAndGetUser validates a bearer token and returns the associated user
func ValidateAPITokenAndGetUser(token string) (*SessionUser, error) {
	if db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()

	// Token must start with dpl_
	if !strings.HasPrefix(token, "dpl_") {
		return nil, fmt.Errorf("invalid token format")
	}

	hash := HashToken(token)

	var user SessionUser
	var expiresAt *string
	query := `
		SELECT u.id, u.username, COALESCE(u.email, ''), at.expires_at
		FROM api_tokens at
		JOIN users u ON u.id = at.user_id
		WHERE at.token_hash = $1 AND u.active = 1
		LIMIT 1
	`

	err := db.QueryRowContext(ctx, query, hash).Scan(
		&user.ID, &user.Username, &user.Email, &expiresAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("invalid token")
		}
		return nil, fmt.Errorf("token validation failed: %w", err)
	}

	// Check expiry
	if expiresAt != nil && *expiresAt != "" {
		t, err := time.Parse("2006-01-02 15:04:05", *expiresAt)
		if err == nil && time.Now().After(t) {
			return nil, fmt.Errorf("token expired")
		}
	}

	// Update last_used asynchronously
	go func() {
		db.Exec(`UPDATE api_tokens SET last_used = NOW() WHERE token_hash = $1`, hash)
	}()

	return &user, nil
}

// SessionInfo represents a session with metadata
type SessionInfo struct {
	SessionID    string `json:"session_id"`
	IPAddress    string `json:"ip_address"`
	UserAgent    string `json:"user_agent"`
	CreatedAt    int64  `json:"created_at"`
	LastActivity int64  `json:"last_activity"`
	Status       string `json:"status"`
}

// GetUserSessions retrieves all active sessions for a user
func GetUserSessions(username string) ([]SessionInfo, error) {
	if db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	rows, err := db.Query(`
		SELECT session_id, ip_address, user_agent, created_at, last_activity, status
		FROM sessions
		WHERE username = $1 AND status = 'active' AND (expires_at IS NULL OR expires_at > $2)
		ORDER BY last_activity DESC`,
		username, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []SessionInfo
	for rows.Next() {
		var s SessionInfo
		if err := rows.Scan(&s.SessionID, &s.IPAddress, &s.UserAgent, &s.CreatedAt, &s.LastActivity, &s.Status); err == nil {
			sessions = append(sessions, s)
		}
	}
	return sessions, nil
}

// RevokeSession marks a session as revoked
func RevokeSession(sessionID string) error {
	if db == nil {
		return fmt.Errorf("database not initialized")
	}

	_, err := db.Exec("UPDATE sessions SET status = 'revoked' WHERE session_id = $1", sessionID)
	return err
}
