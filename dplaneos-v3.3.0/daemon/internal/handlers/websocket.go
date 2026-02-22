package handlers

import (
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Allow same-origin connections
		return true
	},
}

type WebSocketHandler struct {
	hub interface {
		Register(*websocket.Conn)
		Unregister(*websocket.Conn)
	}
}

func NewWebSocketHandler(hub interface{ Register(*websocket.Conn); Unregister(*websocket.Conn) }) *WebSocketHandler {
	return &WebSocketHandler{hub: hub}
}

// HandleMonitor upgrades HTTP connection to WebSocket for monitoring
func (h *WebSocketHandler) HandleMonitor(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	// Register client
	h.hub.Register(conn)

	// Handle disconnection
	go func() {
		defer h.hub.Unregister(conn)
		
		// Read messages (ping/pong)
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("WebSocket error: %v", err)
				}
				break
			}
		}
	}()
}
