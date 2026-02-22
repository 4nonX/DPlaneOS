/**
 * D-PlaneOS Real-time Monitoring WebSocket Client
 * Receives live system monitoring events from Go daemon
 */

class MonitorWebSocket {
  constructor() {
    this.ws = null;
    this.reconnectDelay = 1000;
    this.maxReconnectDelay = 30000;
    this.listeners = new Map();
    this.connect();
  }

  connect() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${protocol}//${window.location.host}/ws/monitor`;

    try {
      this.ws = new WebSocket(wsUrl);
      
      this.ws.onopen = () => {
        console.log('[Monitor WS] Connected');
        this.reconnectDelay = 1000; // Reset delay on successful connection
        this.trigger('connected');
      };

      this.ws.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data);
          this.handleEvent(data);
        } catch (error) {
          console.error('[Monitor WS] Failed to parse message:', error);
        }
      };

      this.ws.onerror = (error) => {
        console.error('[Monitor WS] Error:', error);
      };

      this.ws.onclose = () => {
        console.log('[Monitor WS] Disconnected, reconnecting...');
        this.trigger('disconnected');
        this.reconnect();
      };

    } catch (error) {
      console.error('[Monitor WS] Failed to create WebSocket:', error);
      this.reconnect();
    }
  }

  reconnect() {
    setTimeout(() => {
      console.log(`[Monitor WS] Reconnecting in ${this.reconnectDelay}ms...`);
      this.connect();
      
      // Exponential backoff
      this.reconnectDelay = Math.min(this.reconnectDelay * 2, this.maxReconnectDelay);
    }, this.reconnectDelay);
  }

  handleEvent(event) {
    const { type, data, level, timestamp } = event;
    
    console.log(`[Monitor WS] Event: ${type} (${level})`, data);
    
    // Trigger type-specific listeners
    this.trigger(type, { data, level, timestamp });
    
    // Trigger level-specific listeners (for global alert handlers)
    if (level === 'warning' || level === 'critical') {
      this.trigger(`level:${level}`, { type, data, timestamp });
    }
  }

  on(eventType, callback) {
    if (!this.listeners.has(eventType)) {
      this.listeners.set(eventType, []);
    }
    this.listeners.get(eventType).push(callback);
  }

  off(eventType, callback) {
    if (!this.listeners.has(eventType)) return;
    
    const callbacks = this.listeners.get(eventType);
    const index = callbacks.indexOf(callback);
    if (index > -1) {
      callbacks.splice(index, 1);
    }
  }

  trigger(eventType, data = null) {
    if (!this.listeners.has(eventType)) return;
    
    this.listeners.get(eventType).forEach(callback => {
      try {
        callback(data);
      } catch (error) {
        console.error(`[Monitor WS] Listener error for ${eventType}:`, error);
      }
    });
  }

  close() {
    if (this.ws) {
      this.ws.close();
    }
  }
}

// Global singleton instance
window.monitorWS = new MonitorWebSocket();
