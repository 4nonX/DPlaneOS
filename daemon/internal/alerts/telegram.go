// Package alerts sends alert messages to Telegram.
package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

// TelegramConfig is the bot and chat alerts go to.
type TelegramConfig struct {
	BotToken string
	ChatID   string
	Enabled  bool
}

// TelegramAlert is one message.
type TelegramAlert struct {
	Level   string // INFO, WARNING, CRITICAL
	Title   string
	Message string
	Details map[string]string
}

var (
	mu           sync.RWMutex
	globalConfig *TelegramConfig
	// apiBase is the Bot API endpoint (a test points it at a fake server).
	apiBase = "https://api.telegram.org"
)

// InitTelegram sets the bot and chat. It is called at start and whenever
// the settings are saved; before, saving them changed nothing until the
// daemon restarted (the test button sent directly and worked).
func InitTelegram(botToken, chatID string) {
	mu.Lock()
	defer mu.Unlock()
	if botToken == "" || chatID == "" {
		globalConfig = nil
		log.Println("Telegram not configured (optional)")
		return
	}
	globalConfig = &TelegramConfig{BotToken: botToken, ChatID: chatID, Enabled: true}
	log.Println("Telegram alerts enabled")
}

// DisableTelegram stops Telegram alerts (switched off in the settings).
func DisableTelegram() {
	mu.Lock()
	globalConfig = nil
	mu.Unlock()
}

// maxMessage is Telegram's limit for one message (characters).
const maxMessage = 4096

// FormatAlert renders an alert as plain text. Markdown (as before) made the
// API refuse every message with an odd "_" or "*" in it, e.g. a pool or an
// event named auto_update_failed, and the alert was lost.
func FormatAlert(alert TelegramAlert) string {
	emoji := "ℹ️"
	switch alert.Level {
	case "WARNING":
		emoji = "⚠️"
	case "CRITICAL":
		emoji = "🚨"
	}
	msg := fmt.Sprintf("%s %s: %s\n\n%s", emoji, alert.Level, alert.Title, alert.Message)
	if len(alert.Details) > 0 {
		keys := make([]string, 0, len(alert.Details))
		for k := range alert.Details {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		msg += "\n"
		for _, k := range keys {
			msg += fmt.Sprintf("\n• %s: %s", k, alert.Details[k])
		}
	}
	if utf8.RuneCountInString(msg) > maxMessage {
		r := []rune(msg)
		msg = string(r[:maxMessage-1]) + "…"
	}
	return msg
}

// SendAlert sends an alert if Telegram is configured.
func SendAlert(alert TelegramAlert) error {
	mu.RLock()
	cfg := globalConfig
	mu.RUnlock()
	if cfg == nil || !cfg.Enabled {
		return nil // Silently skip if not configured
	}
	return SendMessage(cfg.BotToken, cfg.ChatID, FormatAlert(alert))
}

// SendMessage sends a plain-text message.
func SendMessage(botToken, chatID, text string) error {
	payload, err := json.Marshal(map[string]any{"chat_id": chatID, "text": text})
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(apiBase+"/bot"+botToken+"/sendMessage", "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to send telegram message: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram API error: %s", string(body))
	}
	return nil
}
