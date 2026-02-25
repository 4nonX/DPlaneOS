package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type LogLevel string

const (
	LevelInfo    LogLevel = "INFO"
	LevelWarning LogLevel = "WARNING"
	LevelWarn    LogLevel = "WARNING" // alias for LevelWarning
	LevelError   LogLevel = "ERROR"
	LevelSecurity LogLevel = "SECURITY"
)

type AuditLog struct {
	Timestamp   time.Time              `json:"timestamp"`
	Level       LogLevel               `json:"level"`
	User        string                 `json:"user,omitempty"`
	Command     string                 `json:"command"`
	Args        []string               `json:"args,omitempty"`
	Success     bool                   `json:"success"`
	Error       string                 `json:"error,omitempty"`
	Duration    int64                  `json:"duration_ms"`
	SourceIP    string                 `json:"source_ip,omitempty"`
	SessionID   string                 `json:"session_id,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

type Logger struct {
	file *os.File
	mu   sync.Mutex
}

var (
	defaultLogger *Logger
	once          sync.Once
)

// InitLogger initializes the audit logger
func InitLogger(logPath string) error {
	var err error
	once.Do(func() {
		defaultLogger, err = NewLogger(logPath)
	})
	return err
}

// NewLogger creates a new audit logger
func NewLogger(logPath string) (*Logger, error) {
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open audit log: %w", err)
	}

	return &Logger{
		file: file,
	}, nil
}

// Log writes an audit log entry
func (l *Logger) Log(entry AuditLog) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry.Timestamp = time.Now()

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	_, err = l.file.Write(append(data, '\n'))
	if err != nil {
		return err
	}

	// Also log to stderr for systemd journal
	fmt.Fprintf(os.Stderr, "%s\n", string(data))

	return l.file.Sync()
}

// Close closes the audit log file
func (l *Logger) Close() error {
	return l.file.Close()
}

// Convenience functions using default logger
func Log(entry AuditLog) error {
	if defaultLogger == nil {
		return fmt.Errorf("audit logger not initialized")
	}
	return defaultLogger.Log(entry)
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

func Close() error {
	if defaultLogger == nil {
		return nil
	}
	return defaultLogger.Close()
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
// Call pattern: audit.LogActivity(user, "directory_create", map[string]interface{}{...})
func LogActivity(user, action string, details map[string]interface{}) {
	msg := fmt.Sprintf("%v", details)
	Log(AuditLog{
		Level:   LevelInfo,
		Command: action,
		User:    user,
		Success: true,
		Error:   msg,
	})
}
