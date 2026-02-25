package security

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// db is declared in rbac.go via SetDatabase()

// InitDatabase initializes the SQLite connection
func InitDatabase(dbPath string) error {
	var err error
	// Use same high-concurrency settings as main daemon
	// - WAL mode: concurrent reads during writes
	// - busy_timeout: wait 30s during WAL checkpoints
	// - cache_size: 64MB in-memory cache
	db, err = sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=30000&cache=shared&_cache_size=-65536&_synchronous=FULL")
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

	// Session idle timeout: 30 minutes of inactivity = expired
	idleTimeout := int64(30 * 60) // 30 minutes in seconds
	now := time.Now().Unix()

	// Check if session exists, not expired, and not idle
	var count int
	query := `
		SELECT COUNT(*) 
		FROM sessions 
		WHERE session_id = ? 
		AND username = ?
		AND (expires_at IS NULL OR expires_at > ?)
		AND (last_activity = 0 OR (? - last_activity) < ?)
	`

	err := db.QueryRow(query, sessionID, username, now, now, idleTimeout).Scan(&count)
	if err != nil {
		// FAIL-CLOSED: Reject on ANY error (no fallback!)
		return false, fmt.Errorf("session validation failed: %w", err)
	}

	if count > 0 {
		// Update last_activity timestamp (touch session)
		db.Exec("UPDATE sessions SET last_activity = ? WHERE session_id = ?", now, sessionID)
		return true, nil
	}

	return false, nil
}

// GetUserFromSession retrieves the username associated with a session
func GetUserFromSession(sessionID string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("database not initialized")
	}

	var username string
	query := `
		SELECT username 
		FROM sessions 
		WHERE session_id = ?
		AND (expires_at IS NULL OR expires_at > ?)
		LIMIT 1
	`

	err := db.QueryRow(query, sessionID, time.Now().Unix()).Scan(&username)
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

	var count int
	query := `
		SELECT COUNT(*) 
		FROM users 
		WHERE username = ?
		AND active = 1
	`

	err := db.QueryRow(query, username).Scan(&count)
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

	var user SessionUser
	query := `
		SELECT u.id, u.username, COALESCE(u.email, '')
		FROM sessions s
		JOIN users u ON s.username = u.username
		WHERE s.session_id = ?
		AND (s.expires_at IS NULL OR s.expires_at > ?)
		AND u.active = 1
		AND COALESCE(s.status, 'active') = 'active'
		LIMIT 1
	`

	err := db.QueryRow(query, sessionToken, time.Now().Unix()).Scan(
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
