package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type SettingsHandler struct {
	db *sql.DB
}

func NewSettingsHandler(db *sql.DB) *SettingsHandler {
	return &SettingsHandler{db: db}
}

type TelegramConfig struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
	Enabled  bool   `json:"enabled"`
}

// GetTelegramConfig retrieves Telegram configuration
func (h *SettingsHandler) GetTelegramConfig(w http.ResponseWriter, r *http.Request) {
	var config TelegramConfig
	
	err := h.db.QueryRow(`
		SELECT COALESCE(bot_token, ''), COALESCE(chat_id, ''), enabled 
		FROM telegram_config 
		WHERE id = 1
	`).Scan(&config.BotToken, &config.ChatID, &config.Enabled)
	
	if err == sql.ErrNoRows {
		// No config yet, return empty
		config = TelegramConfig{Enabled: false}
	} else if err != nil {
		respondErrorSimple(w, "Failed to get config", http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(config)
}

// SaveTelegramConfig saves Telegram configuration
func (h *SettingsHandler) SaveTelegramConfig(w http.ResponseWriter, r *http.Request) {
	var config TelegramConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	
	// Insert or update
	_, err := h.db.Exec(`
		INSERT INTO telegram_config (id, bot_token, chat_id, enabled, updated_at)
		VALUES (1, $1, $2, $3, NOW())
		ON CONFLICT(id) DO UPDATE SET
			bot_token = excluded.bot_token,
			chat_id = excluded.chat_id,
			enabled = excluded.enabled,
			updated_at = NOW()
	`, config.BotToken, config.ChatID, boolToInt(config.Enabled))
	
	if err != nil {
		respondErrorSimple(w, "Failed to save config", http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// TestTelegramConfig tests Telegram connection
func (h *SettingsHandler) TestTelegramConfig(w http.ResponseWriter, r *http.Request) {
	var config TelegramConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if config.BotToken == "" || config.ChatID == "" {
		respondErrorSimple(w, "Bot token and chat ID are required", http.StatusBadRequest)
		return
	}

	// Actually send a test message via the Telegram Bot API
	if err := sendTelegramTest(config.BotToken, config.ChatID); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "Test failed: " + err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Test message sent successfully.",
	})
}

// sendTelegramTest sends a test message directly with the provided credentials
// without touching the global alerts config.
func sendTelegramTest(botToken, chatID string) error {
	url := "https://api.telegram.org/bot" + botToken + "/sendMessage"
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"text":       "✅ *D-PlaneOS Telegram Test*\n\nYour alert configuration is working correctly.",
		"parse_mode": "Markdown",
	}
	body, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Telegram API error %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
