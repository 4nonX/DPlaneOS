package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// TelegramConfig holds Telegram bot configuration
type TelegramConfig struct {
	BotToken string
	ChatID   string
	Enabled  bool
}

// TelegramAlert represents an alert message
type TelegramAlert struct {
	Level   string // INFO, WARNING, CRITICAL
	Title   string
	Message string
	Details map[string]string
}

var globalConfig *TelegramConfig

// InitTelegram initializes Telegram bot configuration
func InitTelegram(botToken, chatID string) {
	if botToken == "" || chatID == "" {
		log.Println("Telegram not configured (optional)")
		return
	}
	
	globalConfig = &TelegramConfig{
		BotToken: botToken,
		ChatID:   chatID,
		Enabled:  true,
	}
	
	log.Println("Telegram alerts enabled")
}

// SendAlert sends an alert to Telegram
func SendAlert(alert TelegramAlert) error {
	if globalConfig == nil || !globalConfig.Enabled {
		return nil // Silently skip if not configured
	}
	
	// Build message with Markdown formatting
	var message string
	
	// Emoji based on level
	emoji := "ℹ️"
	if alert.Level == "WARNING" {
		emoji = "⚠️"
	} else if alert.Level == "CRITICAL" {
		emoji = "🚨"
	}
	
	message = fmt.Sprintf("%s *%s*\n\n*%s*\n\n%s", emoji, alert.Level, alert.Title, alert.Message)
	
	// Add details if present
	if len(alert.Details) > 0 {
		message += "\n\n*Details:*"
		for key, value := range alert.Details {
			message += fmt.Sprintf("\n• %s: `%s`", key, value)
		}
	}
	
	return sendTelegramMessage(message)
}

// sendTelegramMessage sends a message via Telegram Bot API
func sendTelegramMessage(text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", globalConfig.BotToken)
	
	payload := map[string]interface{}{
		"chat_id":    globalConfig.ChatID,
		"text":       text,
		"parse_mode": "Markdown",
	}
	
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}
	
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to send telegram message: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram API error: %s", string(body))
	}
	
	return nil
}
