package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  WEBHOOK ALERTING
//
//  Generic outbound webhook for system events. Covers Slack, Teams, Discord,
//  PagerDuty, Opsgenie, and any HTTP endpoint that accepts a JSON POST.
//
//  Events (string constants used by callers):
//    EventPoolDegraded      "pool.degraded"
//    EventPoolCritical      "pool.critical"
//    EventPoolScrubError    "pool.scrub_error"
//    EventCapacityWarning   "capacity.warning"
//    EventCapacityCritical  "capacity.critical"
//    EventCapacityEmergency "capacity.emergency"
//    EventDiskSmartFail     "disk.smart_fail"
//    EventAuthFailedBurst   "auth.failed_burst"
//    EventShareFailed       "share.failed"
//    EventUpgradeApplied    "upgrade.applied"
// ═══════════════════════════════════════════════════════════════════════════════

// Webhook event name constants - used by callers of SendWebhookAlert.
const (
	EventPoolDegraded      = "pool.degraded"
	EventPoolCritical      = "pool.critical"
	EventPoolScrubError    = "pool.scrub_error"
	EventCapacityWarning   = "capacity.warning"
	EventCapacityCritical  = "capacity.critical"
	EventCapacityEmergency = "capacity.emergency"
	EventDiskSmartFail     = "disk.smart_fail"
	EventAuthFailedBurst   = "auth.failed_burst"
	EventShareFailed       = "share.failed"
	EventUpgradeApplied    = "upgrade.applied"
)

// WebhookHandler manages webhook configuration CRUD.
type WebhookHandler struct {
	db *sql.DB
}

func NewWebhookHandler(db *sql.DB, version string) *WebhookHandler {
	SetDaemonVersion(version)
	return &WebhookHandler{db: db}
}

// webhookConfig mirrors the webhook_configs table row.
type webhookConfig struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	SecretHeader string `json:"secret_header"`
	SecretValue  string `json:"secret_value,omitempty"` // omitted on list responses
	ContentType  string `json:"content_type"`           // default: application/json
	BodyTemplate string `json:"body_template"`          // optional; supports {{event}}, {{pool}}, {{message}}, {{timestamp}}, {{hostname}}
	Enabled      int    `json:"enabled"`
	Events       string `json:"events"` // JSON array string, e.g. '["pool.degraded","capacity.critical"]'
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// webhookPayload is the default JSON body sent when no body_template is set.
type webhookPayload struct {
	Event     string                 `json:"event"`
	Hostname  string                 `json:"hostname"`
	Severity  string                 `json:"severity"`
	Message   string                 `json:"message"`
	Timestamp string                 `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// ── CRUD ──────────────────────────────────────────────────────────────────────

// ListWebhooks returns all configured webhooks.
// GET /api/alerts/webhooks
func (h *WebhookHandler) ListWebhooks(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT id, name, url, secret_header, content_type, body_template, enabled, events, created_at, updated_at
		FROM webhook_configs
		ORDER BY id ASC
	`)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to query webhooks", err)
		return
	}
	defer rows.Close()

	var configs []webhookConfig
	for rows.Next() {
		var c webhookConfig
		if err := rows.Scan(
			&c.ID, &c.Name, &c.URL, &c.SecretHeader,
			&c.ContentType, &c.BodyTemplate,
			&c.Enabled, &c.Events, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			continue
		}
		// Never return the secret value in list responses.
		configs = append(configs, c)
	}

	respondOK(w, map[string]interface{}{
		"success":  true,
		"webhooks": configs,
		"count":    len(configs),
	})
}

// SaveWebhook creates or updates a webhook configuration.
// POST /api/alerts/webhooks
// Body: { name, url, secret_header, secret_value, content_type, body_template, enabled, events }
func (h *WebhookHandler) SaveWebhook(w http.ResponseWriter, r *http.Request) {
	var req webhookConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.URL) == "" {
		respondErrorSimple(w, "name and url are required", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		respondErrorSimple(w, "url must start with http:// or https://", http.StatusBadRequest)
		return
	}

	// Default content type
	if strings.TrimSpace(req.ContentType) == "" {
		req.ContentType = "application/json"
	}

	// Validate events JSON array
	var eventList []string
	if req.Events != "" {
		if err := json.Unmarshal([]byte(req.Events), &eventList); err != nil {
			respondErrorSimple(w, "events must be a JSON array of strings", http.StatusBadRequest)
			return
		}
	} else {
		req.Events = "[]"
	}

	if req.ID == 0 {
		// Create
		var id int64
		err := h.db.QueryRow(`
			INSERT INTO webhook_configs (name, url, secret_header, secret_value, content_type, body_template, enabled, events)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			req.Name, req.URL, req.SecretHeader, req.SecretValue,
			req.ContentType, req.BodyTemplate,
			req.Enabled, req.Events,
		).Scan(&id)
		if err != nil {
			if strings.Contains(err.Error(), "unique constraint") || strings.Contains(err.Error(), "duplicate key") {
				respondErrorSimple(w, "A webhook with that name already exists", http.StatusConflict)
				return
			}
			respondError(w, http.StatusInternalServerError, "Failed to create webhook", err)
			return
		}
		respondOK(w, map[string]interface{}{"success": true, "id": id, "message": "Webhook created"})
	} else {
		// Update - only replace secret_value if provided, keep existing otherwise
		var args []interface{}
		var query string
		if req.SecretValue != "" {
			query = `UPDATE webhook_configs
				SET name=$1, url=$2, secret_header=$3, secret_value=$4, content_type=$5, body_template=$6,
				    enabled=$7, events=$8, updated_at=NOW()
				WHERE id=$9`
			args = []interface{}{
				req.Name, req.URL, req.SecretHeader, req.SecretValue,
				req.ContentType, req.BodyTemplate,
				req.Enabled, req.Events, req.ID,
			}
		} else {
			query = `UPDATE webhook_configs
				SET name=$1, url=$2, secret_header=$3, content_type=$4, body_template=$5,
				    enabled=$6, events=$7, updated_at=NOW()
				WHERE id=$8`
			args = []interface{}{
				req.Name, req.URL, req.SecretHeader,
				req.ContentType, req.BodyTemplate,
				req.Enabled, req.Events, req.ID,
			}
		}
		if _, err := h.db.Exec(query, args...); err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to update webhook", err)
			return
		}
		respondOK(w, map[string]interface{}{"success": true, "message": "Webhook updated"})
	}
}

// DeleteWebhook removes a webhook configuration.
// DELETE /api/alerts/webhooks/{id}
func (h *WebhookHandler) DeleteWebhook(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]
	if _, err := h.db.Exec("DELETE FROM webhook_configs WHERE id = $1", idStr); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to delete webhook", err)
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "message": "Webhook deleted"})
}

// TestWebhook fires a test payload to a specific webhook.
// POST /api/alerts/webhooks/{id}/test
func (h *WebhookHandler) TestWebhook(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]

	var cfg webhookConfig
	err := h.db.QueryRow(`
		SELECT id, name, url, secret_header, secret_value, content_type, body_template, enabled, events
		FROM webhook_configs WHERE id = $1`, idStr,
	).Scan(
		&cfg.ID, &cfg.Name, &cfg.URL, &cfg.SecretHeader, &cfg.SecretValue,
		&cfg.ContentType, &cfg.BodyTemplate, &cfg.Enabled, &cfg.Events,
	)
	if err != nil {
		respondError(w, http.StatusNotFound, "Webhook not found", err)
		return
	}

	payload := webhookPayload{
		Event:     "webhook.test",
		Hostname:  safeHostname(),
		Severity:  "info",
		Message:   fmt.Sprintf("Test alert from D-PlaneOS webhook '%s'", cfg.Name),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data:      map[string]interface{}{"webhook_id": cfg.ID, "webhook_name": cfg.Name},
	}

	if err := dispatchWebhook(cfg, payload); err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "message": "Test payload delivered"})
}

// ── Internal dispatch ──────────────────────────────────────────────────────────

// SendWebhookAlert is called internally by other handlers to emit a system event.
// Mirrors SendSMTPAlert in alerting_smtp.go - fire and forget, async, with retry.
//
// Example callers:
//
//	handlers.SendWebhookAlert(db, EventPoolDegraded, "critical", "Pool 'tank' degraded", map[string]interface{}{"pool": "tank"})
func SendWebhookAlert(db *sql.DB, event, severity, message string, data map[string]interface{}) {
	if db == nil {
		return
	}

	rows, err := db.Query(`
		SELECT id, name, url, secret_header, secret_value, content_type, body_template, enabled, events
		FROM webhook_configs
		WHERE enabled = 1
	`)
	if err != nil {
		log.Printf("webhook alert: db query error: %v", err)
		return
	}
	defer rows.Close()

	hostname := safeHostname()
	payload := webhookPayload{
		Event:     event,
		Hostname:  hostname,
		Severity:  severity,
		Message:   message,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data:      data,
	}

	for rows.Next() {
		var cfg webhookConfig
		if err := rows.Scan(
			&cfg.ID, &cfg.Name, &cfg.URL, &cfg.SecretHeader, &cfg.SecretValue,
			&cfg.ContentType, &cfg.BodyTemplate, &cfg.Enabled, &cfg.Events,
		); err != nil {
			continue
		}
		// Check if this config is subscribed to this event
		if !webhookSubscribed(cfg.Events, event) {
			continue
		}
		// Fire async - never block the caller
		go func(c webhookConfig, p webhookPayload) {
			if err := dispatchWithRetry(c, p); err != nil {
				log.Printf("webhook alert FAILED name=%s event=%s err=%v", c.Name, p.Event, err)
			}
		}(cfg, payload)
	}
}

// webhookSubscribed returns true if the events JSON array contains the given event,
// or if the array is empty/null (subscribe to all).
func webhookSubscribed(eventsJSON, event string) bool {
	if eventsJSON == "" || eventsJSON == "[]" || eventsJSON == "null" {
		return true // empty = subscribe to everything
	}
	var events []string
	if err := json.Unmarshal([]byte(eventsJSON), &events); err != nil {
		return false
	}
	for _, e := range events {
		if e == event {
			return true
		}
	}
	return false
}

// dispatchWebhook sends a single webhook payload synchronously.
// Used by TestWebhook (needs the error returned to the caller).
//
// Body rendering:
//   - If cfg.BodyTemplate is non-empty, the template string is rendered with
//     simple token substitution and sent verbatim with cfg.ContentType.
//   - If cfg.BodyTemplate is empty, the default JSON webhookPayload is sent.
//
// daemonVersion is set at startup via SetDaemonVersion - populated from main.Version.
var daemonVersion = "dev"

// SetDaemonVersion allows main to inject the build version into this package.
func SetDaemonVersion(v string) { daemonVersion = v }

func dispatchWebhook(cfg webhookConfig, payload webhookPayload) error {
	var bodyBytes []byte
	contentType := "application/json"

	if strings.TrimSpace(cfg.BodyTemplate) != "" {
		// Render the body template with token substitution.
		hostname := payload.Hostname
		if hostname == "" {
			hostname = safeHostname()
		}
		rendered := strings.NewReplacer(
			"{{event}}", payload.Event,
			"{{pool}}", func() string {
				// pool name may be in Data["pool"] or in the message
				if payload.Data != nil {
					if p, ok := payload.Data["pool"].(string); ok {
						return p
					}
					if p, ok := payload.Data["resource"].(string); ok {
						return p
					}
				}
				return ""
			}(),
			"{{message}}", payload.Message,
			"{{timestamp}}", time.Now().UTC().Format(time.RFC3339),
			"{{hostname}}", hostname,
			"{{severity}}", payload.Severity,
		).Replace(cfg.BodyTemplate)
		bodyBytes = []byte(rendered)

		if strings.TrimSpace(cfg.ContentType) != "" {
			contentType = cfg.ContentType
		}
	} else {
		// Default: marshal the standard JSON payload.
		var err error
		bodyBytes, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		if strings.TrimSpace(cfg.ContentType) != "" {
			contentType = cfg.ContentType
		}
	}

	req, err := http.NewRequest("POST", cfg.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "D-PlaneOS/"+daemonVersion)
	if cfg.SecretHeader != "" && cfg.SecretValue != "" {
		req.Header.Set(cfg.SecretHeader, cfg.SecretValue)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return nil
}

// dispatchWithRetry retries on transient failures with exponential backoff.
// Attempts: immediate → 5s → 25s (3 total).
func dispatchWithRetry(cfg webhookConfig, payload webhookPayload) error {
	delays := []time.Duration{0, 5 * time.Second, 25 * time.Second}
	var lastErr error
	for i, delay := range delays {
		if delay > 0 {
			time.Sleep(delay)
		}
		if err := dispatchWebhook(cfg, payload); err != nil {
			lastErr = err
			log.Printf("webhook retry %d/%d name=%s err=%v", i+1, len(delays), cfg.Name, err)
			continue
		}
		return nil // success
	}
	return fmt.Errorf("all %d attempts failed, last error: %w", len(delays), lastErr)
}

// safeHostname returns the system hostname or "unknown" on error.
func safeHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

