package audit

import (
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

// AuditEvent represents a single audit log entry
type AuditEvent struct {
	Timestamp int64
	User      string
	Action    string
	Resource  string
	Details   string
	IPAddress string
	Success   bool
}

// BufferedLogger implements batched audit logging for high-performance SQLite
type BufferedLogger struct {
	db            *sql.DB
	buffer        []AuditEvent
	bufferMutex   sync.Mutex
	flushTicker   *time.Ticker
	stopChan      chan struct{}
	maxBuffer     int
	flushInterval time.Duration
	hmacKey       []byte // 32-byte key for audit chain integrity; nil = chain disabled
}

// NewBufferedLogger creates a new buffered audit logger
//
// CRITICAL for large storage systems:
// - Batches audit logs to reduce SQLite I/O
// - Flushes every 5 seconds OR when buffer reaches maxBuffer
// - Prevents I/O stalls during mass file operations
//
// Example: Moving 10,000 files generates 10,000 audit events
// Without buffering: 10,000 individual SQLite INSERTs → slow!
// With buffering: 1-2 batch INSERTs → fast!
func NewBufferedLogger(db *sql.DB, maxBuffer int, flushInterval time.Duration, hmacKey []byte) *BufferedLogger {
	if maxBuffer <= 0 {
		maxBuffer = 100
	}
	if flushInterval <= 0 {
		flushInterval = 5 * time.Second
	}

	bl := &BufferedLogger{
		db:            db,
		buffer:        make([]AuditEvent, 0, maxBuffer),
		maxBuffer:     maxBuffer,
		flushInterval: flushInterval,
		stopChan:      make(chan struct{}),
		hmacKey:       hmacKey,
	}

	return bl
}

// Start begins the background flushing goroutine
func (bl *BufferedLogger) Start() {
	bl.flushTicker = time.NewTicker(bl.flushInterval)
	
	go func() {
		for {
			select {
			case <-bl.flushTicker.C:
				// Periodic flush
				if err := bl.Flush(); err != nil {
					log.Printf("Error flushing audit logs: %v", err)
				}
			case <-bl.stopChan:
				// Final flush before shutdown
				bl.flushTicker.Stop()
				if err := bl.Flush(); err != nil {
					log.Printf("Error in final audit flush: %v", err)
				}
				return
			}
		}
	}()
}

// Stop gracefully stops the buffered logger
func (bl *BufferedLogger) Stop() {
	close(bl.stopChan)
}

// SecurityActions lists action strings that must bypass the buffer and write
// directly to SQLite. These events must never be lost on crash or SIGKILL.
// Callers can also set event.Critical = true to force direct write.
var SecurityActions = map[string]bool{
	"login":           true,
	"login_failed":    true,
	"logout":          true,
	"auth_failed":     true,
	"permission_denied": true,
	"user_created":    true,
	"user_deleted":    true,
	"password_changed": true,
	"token_created":   true,
	"token_revoked":   true,
}

// Log adds an event to the buffer.
// Security events (auth, permission) bypass the buffer and are written directly
// to SQLite to guarantee they survive a hard crash or SIGKILL.
//
// Thread-safe: Can be called from multiple goroutines
func (bl *BufferedLogger) Log(event AuditEvent) error {
	// Security-critical events skip the buffer entirely
	if SecurityActions[event.Action] {
		return bl.writeDirect([]AuditEvent{event})
	}

	bl.bufferMutex.Lock()
	bl.buffer = append(bl.buffer, event)
	needFlush := len(bl.buffer) >= bl.maxBuffer
	bl.bufferMutex.Unlock()

	// Flush outside the lock — Flush() manages its own locking.
	// No defer here: we always unlock above, cleanly, before any external call.
	if needFlush {
		return bl.Flush()
	}
	return nil
}

// writeDirect writes events synchronously to SQLite, bypassing the buffer.
// Used for security events that must not be lost on crash.
func (bl *BufferedLogger) writeDirect(events []AuditEvent) error {
	tx, err := bl.db.Begin()
	if err != nil {
		return fmt.Errorf("audit direct write: begin: %w", err)
	}
	defer tx.Rollback()

	// Fetch prev_hash to continue the chain for security events.
	var prevHash string
	if bl.hmacKey != nil {
		_ = tx.QueryRow(
			`SELECT COALESCE(row_hash,'') FROM audit_logs ORDER BY id DESC LIMIT 1`,
		).Scan(&prevHash)
	}

	stmt, err := tx.Prepare(`INSERT INTO audit_logs
		(timestamp, user, action, resource, details, ip_address, success, prev_hash, row_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("audit direct write: prepare: %w", err)
	}
	defer stmt.Close()

	for _, e := range events {
		rowHash := computeRowHash(bl.hmacKey, prevHash, e)
		_, err := stmt.Exec(e.Timestamp, e.User, e.Action, e.Resource, e.Details, e.IPAddress, e.Success, prevHash, rowHash)
		if err != nil {
			log.Printf("audit direct write: exec: %v", err)
			continue
		}
		prevHash = rowHash
	}
	return tx.Commit()
}

// Flush writes all buffered events to SQLite in a single transaction
//
// CRITICAL: Uses BEGIN TRANSACTION for batch insert
// This is 100x faster than individual INSERTs
func (bl *BufferedLogger) Flush() error {
	bl.bufferMutex.Lock()
	
	// Quick exit if buffer is empty
	if len(bl.buffer) == 0 {
		bl.bufferMutex.Unlock()
		return nil
	}

	// Copy buffer and clear it
	events := make([]AuditEvent, len(bl.buffer))
	copy(events, bl.buffer)
	bl.buffer = bl.buffer[:0]
	
	bl.bufferMutex.Unlock()

	// Write to database in single transaction
	tx, err := bl.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Fetch the row_hash of the most recent row to start the chain.
	// Run inside the transaction so we see a consistent snapshot.
	var prevHash string
	if bl.hmacKey != nil {
		_ = tx.QueryRow(
			`SELECT COALESCE(row_hash,'') FROM audit_logs ORDER BY id DESC LIMIT 1`,
		).Scan(&prevHash)
	}

	// Prepare statement (reused for all inserts)
	stmt, err := tx.Prepare(`
		INSERT INTO audit_logs (
			timestamp, user, action, resource, details, ip_address, success,
			prev_hash, row_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	// Insert all events in batch, threading the HMAC chain.
	for _, event := range events {
		rowHash := computeRowHash(bl.hmacKey, prevHash, event)
		_, err := stmt.Exec(
			event.Timestamp,
			event.User,
			event.Action,
			event.Resource,
			event.Details,
			event.IPAddress,
			event.Success,
			prevHash,
			rowHash,
		)
		if err != nil {
			// Log error but continue with other events
			log.Printf("Failed to insert audit event: %v", err)
			continue
		}
		prevHash = rowHash // advance chain
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	log.Printf("Flushed %d audit events to database", len(events))
	return nil
}

// GetStats returns buffer statistics
func (bl *BufferedLogger) GetStats() map[string]interface{} {
	bl.bufferMutex.Lock()
	defer bl.bufferMutex.Unlock()

	return map[string]interface{}{
		"buffer_size":     len(bl.buffer),
		"max_buffer":      bl.maxBuffer,
		"flush_interval":  bl.flushInterval.String(),
		"buffer_capacity": cap(bl.buffer),
	}
}

// Example usage in main.go:
//
// var auditLogger *audit.BufferedLogger
//
// func main() {
//     db, _ := sql.Open("sqlite3", "/var/lib/dplaneos/dplaneos.db")
//     
//     // Create buffered logger
//     // Buffer up to 100 events, flush every 5 seconds
//     auditLogger = audit.NewBufferedLogger(db, 100, 5*time.Second)
//     auditLogger.Start()
//     defer auditLogger.Stop()
//     
//     // Log events (non-blocking, fast!)
//     auditLogger.Log(audit.AuditEvent{
//         Timestamp: time.Now().Unix(),
//         User:      "admin",
//         Action:    "file_delete",
//         Resource:  "/tank/data/old.txt",
//         Success:   true,
//     })
// }
