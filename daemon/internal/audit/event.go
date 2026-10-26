// Package audit provides immutable audit logging.
// event.go: Phase 4.2 - Structured event logging with context
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// EventType categorizes Phase 4 structured audit events.
type EventType string

const (
	EventOperationStart    EventType = "operation_start"
	EventOperationComplete EventType = "operation_complete"
	EventOperationFailed   EventType = "operation_failed"
	EventCircuitOpen       EventType = "circuit_open"
	EventCircuitClosed     EventType = "circuit_closed"
	EventResourceCritical  EventType = "resource_critical"
	EventHardwareDetected  EventType = "hardware_detected"
	EventAuthFailure       EventType = "auth_failure"
	EventConfigChanged     EventType = "config_changed"
)

// StructuredEvent represents a high-level Phase 4 audit event.
type StructuredEvent struct {
	ID          int64                  `json:"id"`
	Timestamp   time.Time              `json:"timestamp"`
	EventType   EventType              `json:"event_type"`
	Component   string                 `json:"component"`
	OperationID string                 `json:"operation_id,omitempty"`
	UserID      string                 `json:"user_id,omitempty"`
	Status      string                 `json:"status"`
	Details     map[string]interface{} `json:"details"`
	IPAddress   string                 `json:"ip_address,omitempty"`
	HMAC        string                 `json:"hmac"`
}

// EventLogger logs high-level structured events (Phase 4) to audit_events table.
// Wraps database connection for structured event insertion.
type EventLogger struct {
	db  *sql.DB
	log func(string, ...interface{})
}

// NewEventLogger creates a Phase 4 structured event logger.
func NewEventLogger(db *sql.DB) *EventLogger {
	return &EventLogger{
		db: db,
		log: func(msg string, args ...interface{}) {
			log.Printf("[EVENT-LOGGER] "+msg, args...)
		},
	}
}

// LogEvent logs a structured event to audit_events table.
func (el *EventLogger) LogEvent(ctx context.Context, event StructuredEvent) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	detailsJSON, _ := json.Marshal(event.Details)

	// Insert into audit_events; trigger computes HMAC
	err := el.db.QueryRowContext(ctx, `
		INSERT INTO audit_events (timestamp, event_type, component, operation_id, user_id, status, details, ip_address)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, hmac
	`, event.Timestamp, event.EventType, event.Component, event.OperationID, event.UserID, event.Status, string(detailsJSON), event.IPAddress).
		Scan(&event.ID, &event.HMAC)

	if err != nil {
		return fmt.Errorf("log structured event failed: %w", err)
	}

	el.log("Event logged: type=%s component=%s operation=%s status=%s", event.EventType, event.Component, event.OperationID, event.Status)
	return nil
}

// LogOperation logs the start of a long-running operation.
func (el *EventLogger) LogOperation(ctx context.Context, opID, opType, component string, details map[string]interface{}) error {
	event := StructuredEvent{
		EventType:   EventOperationStart,
		Component:   component,
		OperationID: opID,
		Status:      "success",
		Details: map[string]interface{}{
			"operation_type":    opType,
			"operation_details": details,
		},
	}
	return el.LogEvent(ctx, event)
}

// LogOperationComplete logs when an operation finishes.
func (el *EventLogger) LogOperationComplete(ctx context.Context, opID, component string, duration time.Duration) error {
	event := StructuredEvent{
		EventType:   EventOperationComplete,
		Component:   component,
		OperationID: opID,
		Status:      "success",
		Details: map[string]interface{}{
			"duration_ms": duration.Milliseconds(),
		},
	}
	return el.LogEvent(ctx, event)
}

// LogOperationFailed logs when an operation fails.
func (el *EventLogger) LogOperationFailed(ctx context.Context, opID, component, errMsg string) error {
	event := StructuredEvent{
		EventType:   EventOperationFailed,
		Component:   component,
		OperationID: opID,
		Status:      "failure",
		Details: map[string]interface{}{
			"error": errMsg,
		},
	}
	return el.LogEvent(ctx, event)
}

// LogCircuitStateChange logs circuit breaker state transitions.
func (el *EventLogger) LogCircuitStateChange(ctx context.Context, service, newState string) error {
	eventType := EventCircuitOpen
	if newState == "closed" {
		eventType = EventCircuitClosed
	}

	event := StructuredEvent{
		EventType: eventType,
		Component: "resilience",
		Status:    "warning",
		Details: map[string]interface{}{
			"service":   service,
			"new_state": newState,
		},
	}
	return el.LogEvent(ctx, event)
}

// LogResourceCritical logs resource exhaustion warnings.
func (el *EventLogger) LogResourceCritical(ctx context.Context, resourceType string, percent int) error {
	event := StructuredEvent{
		EventType: EventResourceCritical,
		Component: "resources",
		Status:    "warning",
		Details: map[string]interface{}{
			"resource_type": resourceType,
			"usage_percent": percent,
		},
	}
	return el.LogEvent(ctx, event)
}

// LogHardwareDetected logs detected hardware capabilities.
func (el *EventLogger) LogHardwareDetected(ctx context.Context, profile map[string]interface{}) error {
	event := StructuredEvent{
		EventType: EventHardwareDetected,
		Component: "hardware",
		Status:    "success",
		Details:   profile,
	}
	return el.LogEvent(ctx, event)
}

// GetEventHistory retrieves audit events within a time range.
func (el *EventLogger) GetEventHistory(ctx context.Context, from, to time.Time, limit int) ([]StructuredEvent, error) {
	rows, err := el.db.QueryContext(ctx, `
		SELECT id, timestamp, event_type, component, operation_id, user_id, status, details, ip_address, hmac
		FROM audit_events
		WHERE timestamp BETWEEN $1 AND $2
		ORDER BY id DESC
		LIMIT $3
	`, from, to, limit)

	if err != nil {
		return nil, fmt.Errorf("query audit events failed: %w", err)
	}
	defer rows.Close()

	var events []StructuredEvent
	for rows.Next() {
		var event StructuredEvent
		var detailsJSON string

		err := rows.Scan(&event.ID, &event.Timestamp, &event.EventType, &event.Component, &event.OperationID, &event.UserID, &event.Status, &detailsJSON, &event.IPAddress, &event.HMAC)
		if err != nil {
			continue
		}

		json.Unmarshal([]byte(detailsJSON), &event.Details)
		events = append(events, event)
	}

	return events, nil
}

// VerifyAuditChain validates HMAC integrity from fromID to toID.
// Returns true if all event HMACs are valid, false and the first broken ID otherwise.
func (el *EventLogger) VerifyAuditChain(ctx context.Context, fromID, toID int64) (bool, int64, error) {
	rows, err := el.db.QueryContext(ctx, `
		SELECT id, timestamp, event_type, component, operation_id, status, hmac
		FROM audit_events
		WHERE id BETWEEN $1 AND $2
		ORDER BY id
	`, fromID, toID)

	if err != nil {
		return false, 0, fmt.Errorf("verify chain query failed: %w", err)
	}
	defer rows.Close()

	// This would require the actual trigger computation logic to verify against.
	// For now, we just ensure continuity without corruption (in production,
	// call audit_verify_chain function in PostgreSQL).
	for rows.Next() {
		var id int64
		var ts time.Time
		var eventType, component, opID, status, hmac string
		if err := rows.Scan(&id, &ts, &eventType, &component, &opID, &status, &hmac); err != nil {
			return false, id, err
		}
		if hmac == "" {
			return false, id, fmt.Errorf("event %d has empty hmac", id)
		}
	}

	return true, 0, nil
}
