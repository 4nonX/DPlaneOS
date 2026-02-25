package handlers

import (
	"database/sql"
	"encoding/json"
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
		http.Error(w, "Failed to get config", http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(config)
}

// SaveTelegramConfig saves Telegram configuration
func (h *SettingsHandler) SaveTelegramConfig(w http.ResponseWriter, r *http.Request) {
	var config TelegramConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	
	// Insert or update
	_, err := h.db.Exec(`
		INSERT INTO telegram_config (id, bot_token, chat_id, enabled, updated_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			bot_token = excluded.bot_token,
			chat_id = excluded.chat_id,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`, config.BotToken, config.ChatID, boolToInt(config.Enabled), time.Now().Unix())
	
	if err != nil {
		http.Error(w, "Failed to save config", http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// TestTelegramConfig tests Telegram connection
func (h *SettingsHandler) TestTelegramConfig(w http.ResponseWriter, r *http.Request) {
	var config TelegramConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	
	// TODO: Actually test the connection by sending a test message
	// For now, just validate that token and chat ID are not empty
	
	if config.BotToken == "" || config.ChatID == "" {
		http.Error(w, "Bot token and chat ID are required", http.StatusBadRequest)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Configuration looks valid. Save to enable alerts.",
	})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
