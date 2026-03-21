package handlers

// alert_dispatch.go - Central alert dispatch hub
//
// All subsystems (heartbeat, SMART monitor, capacity guardian, scrub monitor)
// call DispatchAlert() rather than individual alert functions directly.
// Wire up from main.go after all alert subsystems are initialized via
// SetAlertDispatchers().

// Package-level alert function references - set from main.go.
var (
	webhookAlertFn   func(event, resource, message string)
	smtpAlertFn      func(subject, body string)
	telegramAlertFn  func(message string)
	webSocketAlertFn func(event string, data interface{}, level string)
)

// SetAlertDispatchers wires up the three outbound alert channels.
// Call this from main.go after Telegram/SMTP/Webhook subsystems are ready.
//
//	handlers.SetAlertDispatchers(
//	    func(event, resource, msg string) { handlers.SendWebhookAlert(db, event, "critical", msg, nil) },
//	    handlers.SendSMTPAlert,
//	    func(msg string) { alerts.SendAlert(alerts.TelegramAlert{...}) },
//	)
func SetAlertDispatchers(
	webhook func(event, resource, message string),
	smtp func(subject, body string),
	telegram func(message string),
	websocket func(event string, data interface{}, level string),
) {
	webhookAlertFn = webhook
	smtpAlertFn = smtp
	telegramAlertFn = telegram
	webSocketAlertFn = websocket
}

// DispatchAlert routes an alert to all configured channels based on severity level.
//
// level:    "critical" | "warning" | "info"
// event:    event constant, e.g. EventPoolDegraded
// resource: pool name, device name, etc.
// message:  human-readable description
//
// Routing rules:
//   - Webhook: all levels (filtered per-webhook by subscription list)
//   - SMTP:    warning + critical
//   - Telegram: critical only
func DispatchAlert(level, event, resource, message string) {
	if webSocketAlertFn != nil {
		// Additive and non-blocking broadcast to UI
		go webSocketAlertFn(event, map[string]interface{}{
			"resource": resource,
			"message":  message,
		}, level)
	}
	if webhookAlertFn != nil {
		webhookAlertFn(event, resource, message)
	}
	if smtpAlertFn != nil && (level == "critical" || level == "warning") {
		smtpAlertFn("[D-PlaneOS] "+event+": "+resource, message)
	}
	if telegramAlertFn != nil && level == "critical" {
		telegramAlertFn("🚨 " + event + " - " + resource + "\n" + message)
	}
}

