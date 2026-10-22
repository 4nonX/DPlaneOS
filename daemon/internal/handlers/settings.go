package handlers

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"

	"dplaned/internal/alerts"
	"dplaned/internal/secrets"
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

// GetTelegramConfig retrieves Telegram configuration.
// The bot token is never returned; only a boolean indicating whether one is stored.
func (h *SettingsHandler) GetTelegramConfig(w http.ResponseWriter, r *http.Request) {
	var sealedToken, chatID string
	var enabledInt int

	err := h.db.QueryRow(`
		SELECT COALESCE(bot_token, ''), COALESCE(chat_id, ''), enabled
		FROM telegram_config
		WHERE id = 1
	`).Scan(&sealedToken, &chatID, &enabledInt)

	w.Header().Set("Content-Type", "application/json")
	if err == sql.ErrNoRows {
		json.NewEncoder(w).Encode(map[string]any{
			"has_token": false, "chat_id": "", "enabled": false,
		})
		return
	}
	if err != nil {
		respondErrorSimple(w, "Failed to get config", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"has_token": sealedToken != "",
		"chat_id":   chatID,
		"enabled":   enabledInt == 1,
	})
}

// SaveTelegramConfig saves Telegram configuration.
// If bot_token is empty the existing stored token is preserved.
func (h *SettingsHandler) SaveTelegramConfig(w http.ResponseWriter, r *http.Request) {
	var config TelegramConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	tokenToStore := ""
	if config.BotToken != "" {
		sealed, err := secrets.Seal(config.BotToken)
		if err != nil {
			log.Printf("TELEGRAM: failed to seal bot token: %v", err)
			respondErrorSimple(w, "Failed to encrypt token", http.StatusInternalServerError)
			return
		}
		tokenToStore = sealed
	} else {
		// Keep the existing sealed token.
		if err := h.db.QueryRow("SELECT COALESCE(bot_token, '') FROM telegram_config WHERE id=1").Scan(&tokenToStore); err != nil {
			tokenToStore = ""
		}
	}

	_, err := h.db.Exec(`
		INSERT INTO telegram_config (id, bot_token, chat_id, enabled, updated_at)
		VALUES (1, $1, $2, $3, NOW())
		ON CONFLICT(id) DO UPDATE SET
			bot_token  = excluded.bot_token,
			chat_id    = excluded.chat_id,
			enabled    = excluded.enabled,
			updated_at = NOW()
	`, tokenToStore, config.ChatID, boolToInt(config.Enabled))

	if err != nil {
		respondErrorSimple(w, "Failed to save config", http.StatusInternalServerError)
		return
	}
	// Apply now: alerts used the settings loaded at start until a restart.
	applyTelegramConfig(tokenToStore, config.ChatID, config.Enabled)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// applyTelegramConfig makes the alert sender use the saved settings.
func applyTelegramConfig(sealedToken, chatID string, enabled bool) {
	if !enabled || sealedToken == "" || chatID == "" {
		alerts.DisableTelegram()
		return
	}
	token, err := secrets.Open(sealedToken)
	if err != nil {
		log.Printf("TELEGRAM: cannot open the bot token: %v", err)
		alerts.DisableTelegram()
		return
	}
	alerts.InitTelegram(token, chatID)
}

// TestTelegramConfig tests Telegram connectivity.
// If bot_token is omitted in the request the stored (decrypted) token is used.
func (h *SettingsHandler) TestTelegramConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BotToken string `json:"bot_token"`
		ChatID   string `json:"chat_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	tokenToTest := req.BotToken
	chatIDToTest := req.ChatID

	if tokenToTest == "" {
		var sealedToken, storedChatID string
		if err := h.db.QueryRow("SELECT COALESCE(bot_token,''), COALESCE(chat_id,'') FROM telegram_config WHERE id=1").Scan(&sealedToken, &storedChatID); err != nil {
			respondErrorSimple(w, "No saved configuration found", http.StatusBadRequest)
			return
		}
		plain, err := secrets.Open(sealedToken)
		if err != nil || plain == "" {
			respondErrorSimple(w, "No bot token configured", http.StatusBadRequest)
			return
		}
		tokenToTest = plain
		if chatIDToTest == "" {
			chatIDToTest = storedChatID
		}
	}

	if tokenToTest == "" || chatIDToTest == "" {
		respondErrorSimple(w, "Bot token and chat ID are required", http.StatusBadRequest)
		return
	}

	if err := sendTelegramTest(tokenToTest, chatIDToTest); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"message": "Test failed: " + err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "Test message sent successfully.",
	})
}

// sendTelegramTest sends a test message directly with the provided credentials.
// It goes through the same sender as the alerts.
func sendTelegramTest(botToken, chatID string) error {
	return alerts.SendMessage(botToken, chatID, alerts.FormatAlert(alerts.TelegramAlert{
		Level: "INFO", Title: "DPlaneOS Telegram test", Message: "Your alert configuration is working.",
	}))
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
