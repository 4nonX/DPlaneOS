package websocket

import (
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// MonitorEvent represents a monitoring event to send to clients
type MonitorEvent struct {
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
	Level     string      `json:"level"` // info, warning, critical
}

// MonitorHub manages WebSocket connections for monitoring
type MonitorHub struct {
	clients    map[*websocket.Conn]bool
	broadcast  chan MonitorEvent
	register   chan *websocket.Conn
	unregister chan *websocket.Conn
	mutex      sync.RWMutex
}

// NewMonitorHub creates a new monitoring WebSocket hub
func NewMonitorHub() *MonitorHub {
	return &MonitorHub{
		clients:    make(map[*websocket.Conn]bool),
		broadcast:  make(chan MonitorEvent, 256),
		register:   make(chan *websocket.Conn),
		unregister: make(chan *websocket.Conn),
	}
}

// Run starts the hub's event loop
func (h *MonitorHub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mutex.Lock()
			h.clients[client] = true
			h.mutex.Unlock()
			log.Printf("Monitor client connected, total: %d", len(h.clients))

		case client := <-h.unregister:
			h.mutex.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.Close()
			}
			h.mutex.Unlock()
			log.Printf("Monitor client disconnected, total: %d", len(h.clients))

		case event := <-h.broadcast:
			// Use Lock (not RLock): we may delete failed clients from the map.
			h.mutex.Lock()
			for client := range h.clients {
				err := client.WriteJSON(event)
				if err != nil {
					log.Printf("WebSocket write error: %v", err)
					client.Close()
					delete(h.clients, client)
				}
			}
			h.mutex.Unlock()
		}
	}
}

// Register adds a new client connection
func (h *MonitorHub) Register(conn *websocket.Conn) {
	h.register <- conn
}

// Unregister removes a client connection
func (h *MonitorHub) Unregister(conn *websocket.Conn) {
	h.unregister <- conn
}

// Broadcast sends an event to all connected clients
func (h *MonitorHub) Broadcast(eventType string, data interface{}, level string) {
	event := MonitorEvent{
		Type:      eventType,
		Timestamp: time.Now(),
		Data:      data,
		Level:     level,
	}
	
	// Non-blocking send
	select {
	case h.broadcast <- event:
	default:
		log.Printf("Warning: Monitor broadcast channel full, event dropped")
	}
}
