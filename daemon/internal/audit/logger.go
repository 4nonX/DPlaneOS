package audit

import (
	"fmt"
	"strings"
	"time"
)

type LogLevel string

const (
	LevelInfo     LogLevel = "INFO"
	LevelWarning  LogLevel = "WARNING"
	LevelWarn     LogLevel = "WARNING" // alias for LevelWarning
	LevelError    LogLevel = "ERROR"
	LevelSecurity LogLevel = "SECURITY"
)

type AuditLog struct {
	Timestamp time.Time      `json:"timestamp"`
	Level     LogLevel       `json:"level"`
	User      string         `json:"user,omitempty"`
	Command   string         `json:"command"`
	Args      []string       `json:"args,omitempty"`
	Success   bool           `json:"success"`
	Error     string         `json:"error,omitempty"`
	Duration  int64          `json:"duration_ms"`
	SourceIP  string         `json:"source_ip,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

var globalBufferedLogger interface {
	Log(AuditEvent) error
}

// SetGlobalBufferedLogger sets the database-backed logger
func SetGlobalBufferedLogger(bl interface {
	Log(AuditEvent) error
}) {
	globalBufferedLogger = bl
}

// Log writes an entry to the audit log (table audit_logs, HMAC-chained).
// The command's arguments are recorded as the resource it acted on.
func Log(entry AuditLog) error {
	if globalBufferedLogger == nil {
		return fmt.Errorf("audit logger not initialized")
	}
	return globalBufferedLogger.Log(AuditEvent{
		Timestamp: time.Now().Unix(),
		Actor:     entry.User,
		Action:    entry.Command,
		Resource:  strings.Join(entry.Args, " "),
		Details:   entry.Error,
		IPAddress: entry.SourceIP,
		Success:   entry.Success,
	})
}

func LogCommand(level LogLevel, user, command string, args []string, success bool, duration time.Duration, err error) error {
	entry := AuditLog{
		Level:    level,
		User:     user,
		Command:  command,
		Args:     args,
		Success:  success,
		Duration: duration.Milliseconds(),
	}

	if err != nil {
		entry.Error = err.Error()
	}

	return Log(entry)
}

func LogSecurityEvent(message, user, sourceIP string) error {
	return Log(AuditLog{
		Level:    LevelSecurity,
		Command:  "SECURITY_EVENT",
		User:     user,
		SourceIP: sourceIP,
		Success:  false,
		Error:    message,
	})
}

// LogAction is a convenience function for handler-level audit logging.
// Used by system_extended.go and similar handlers.
// Call pattern: audit.LogAction("snapshot_run", user, "Created snapshot", true, duration)
func LogAction(action, user, message string, success bool, duration time.Duration) {
	Log(AuditLog{
		Level:    LevelInfo,
		Command:  action,
		User:     user,
		Success:  success,
		Error:    message,
		Duration: duration.Milliseconds(),
	})
}

// LogActivity is a convenience function for file/replication audit logging.
// Used by files.go and replication.go.
// Call pattern: audit.LogActivity(user, "directory_create", map[string]any{...})
func LogActivity(user, action string, details map[string]any) {
	// A caller that reports the outcome in details ("success": false) must
	// not be recorded as a success.
	success := true
	if v, ok := details["success"].(bool); ok {
		success = v
	}
	msg := fmt.Sprintf("%v", details)
	Log(AuditLog{
		Level:   LevelInfo,
		Command: action,
		User:    user,
		Success: success,
		Error:   msg,
	})
}
