package handlers

// alert_dispatch.go - Central alert dispatch hub
//
// All subsystems (pool monitor, ZED events, SMART monitor, capacity guardian,
// replication, snapshots, ...) call DispatchAlert() rather than individual
// alert functions directly. Wired up from main.go via SetAlertDispatchers().

import (
	"sync"
	"time"
)

// Package-level alert function references - set from main.go.
var (
	alertFnMu        sync.RWMutex
	webhookAlertFn   func(level, event, resource, message string)
	smtpAlertFn      func(subject, body string)
	telegramAlertFn  func(level, title, message string)
	webSocketAlertFn func(event string, data any, level string)
)

// SetAlertDispatchers wires up the outbound alert channels.
func SetAlertDispatchers(
	webhook func(level, event, resource, message string),
	smtp func(subject, body string),
	telegram func(level, title, message string),
	websocket func(event string, data any, level string),
) {
	alertFnMu.Lock()
	defer alertFnMu.Unlock()
	webhookAlertFn = webhook
	smtpAlertFn = smtp
	telegramAlertFn = telegram
	webSocketAlertFn = websocket
}

// AlertRepeatWindow: the same alert (level, event, resource) is sent again
// only after this long. ZED reports each damaged block: one scrub of a
// damaged pool gave 65 "data" events, which were 65 mails.
var AlertRepeatWindow = 30 * time.Minute

var (
	alertSentMu sync.Mutex
	alertSent   = map[string]time.Time{}
)

// alertDue reports whether an alert with this key may be sent now.
func alertDue(key string, now time.Time) bool {
	alertSentMu.Lock()
	defer alertSentMu.Unlock()
	if t, ok := alertSent[key]; ok && now.Sub(t) < AlertRepeatWindow {
		return false
	}
	alertSent[key] = now
	if len(alertSent) > 5000 { // forget old keys
		for k, t := range alertSent {
			if now.Sub(t) > AlertRepeatWindow {
				delete(alertSent, k)
			}
		}
	}
	return true
}

// DispatchAlert routes an alert to all configured channels based on severity level.
//
// level:    "critical" | "warning" | "info"
// event:    event constant, e.g. EventPoolDegraded
// resource: pool name, device name, etc.
// message:  human-readable description
//
// Routing rules:
//   - UI (WebSocket): all levels, always
//   - Webhook: all levels (filtered per webhook by its subscription list)
//   - SMTP:    warning + critical
//   - Telegram: warning + critical
//
// Repeats of the same alert within AlertRepeatWindow go to the UI only.
func DispatchAlert(level, event, resource, message string) {
	alertFnMu.RLock()
	webhookFn, smtpFn, telegramFn, wsFn := webhookAlertFn, smtpAlertFn, telegramAlertFn, webSocketAlertFn
	alertFnMu.RUnlock()
	if wsFn != nil {
		// Additive and non-blocking broadcast to UI
		go wsFn(event, map[string]any{
			"resource": resource,
			"message":  message,
		}, level)
	}
	if !alertDue(level+"|"+event+"|"+resource, time.Now()) {
		return
	}
	if webhookFn != nil {
		webhookFn(level, event, resource, message)
	}
	if smtpFn != nil && (level == "critical" || level == "warning") {
		// sendSMTPAlert adds the "[DPlaneOS]" prefix.
		smtpFn(event+": "+resource, message)
	}
	if telegramFn != nil && (level == "critical" || level == "warning") {
		telegramFn(level, event+" - "+resource, message)
	}
}
