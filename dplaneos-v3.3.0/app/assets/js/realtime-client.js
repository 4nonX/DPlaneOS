/**
 * D-PlaneOS Real-time WebSocket Client
 * Connects to event daemon for live system updates
 */

(function() {
  'use strict';
  
  class RealtimeClient {
    constructor() {
      this.ws = null;
      this.reconnectDelay = 1000;
      this.maxReconnectDelay = 30000;
      this.reconnectAttempts = 0;
      this.listeners = {
        stateUpdate: [],
        hardwareEvent: [],
        resilverUpdate: [],
        diskAlert: [],
        connected: [],
        disconnected: []
      };
      this.lastState = null;
      this.connecting = false;
    }
    
    connect() {
      if (this.connecting || (this.ws && this.ws.readyState === WebSocket.OPEN)) {
        return;
      }
      
      this.connecting = true;
      
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const host = window.location.hostname;
      const port = 8081;
      
      console.log(`Connecting to WebSocket: ${protocol}//${host}:${port}`);
      
      this.ws = new WebSocket(`${protocol}//${host}:${port}`);
      
      this.ws.onopen = () => this.onOpen();
      this.ws.onmessage = (event) => this.onMessage(event);
      this.ws.onclose = () => this.onClose();
      this.ws.onerror = (error) => this.onError(error);
    }
    
    onOpen() {
      console.log('✓ WebSocket connected');
      this.connecting = false;
      this.reconnectAttempts = 0;
      this.reconnectDelay = 1000;
      
      // Send session authentication
      const sessionId = this.getSessionId();
      if (sessionId) {
        this.send({
          type: 'auth',
          session_id: sessionId
        });
      } else {
        console.error('No session ID found - cannot authenticate');
        this.ws.close();
        return;
      }
      
      // Update connection status in UI
      this.updateConnectionStatus(true);
      this.trigger('connected');
      
      // Start keepalive
      this.startKeepalive();
    }
    
    getSessionId() {
      // Get PHP session ID from cookie
      const match = document.cookie.match(/PHPSESSID=([^;]+)/);
      return match ? match[1] : null;
    }
    
    onMessage(event) {
      try {
        const message = JSON.parse(event.data);
        
        switch (message.type) {
          case 'initial_state':
          case 'state_update':
            this.handleStateUpdate(message.data);
            break;
          
          case 'hardware_event':
            this.handleHardwareEvent(message);
            break;
          
          case 'resilver_started':
          case 'resilver_completed':
          case 'scrub_started':
          case 'scrub_completed':
            this.handleZFSEvent(message);
            break;
          
          case 'pool_health_change':
            this.handlePoolHealthChange(message);
            break;
          
          case 'disk_temperature_warning':
            this.handleDiskAlert(message);
            break;
          
          case 'disk_added':
          case 'disk_removed':
            this.handleDiskChange(message);
            break;
          
          case 'pong':
            // Keepalive response
            break;
        }
      } catch (error) {
        console.error('Error handling message:', error);
      }
    }
    
    onClose() {
      console.log('✗ WebSocket disconnected');
      this.connecting = false;
      
      this.updateConnectionStatus(false);
      this.trigger('disconnected');
      
      this.stopKeepalive();
      this.scheduleReconnect();
    }
    
    onError(error) {
      console.error('WebSocket error:', error);
    }
    
    scheduleReconnect() {
      this.reconnectAttempts++;
      console.log(`Reconnecting in ${this.reconnectDelay/1000}s (attempt ${this.reconnectAttempts})...`);
      
      setTimeout(() => {
        this.connect();
      }, this.reconnectDelay);
      
      // Exponential backoff
      this.reconnectDelay = Math.min(
        this.reconnectDelay * 2,
        this.maxReconnectDelay
      );
    }
    
    startKeepalive() {
      this.keepaliveInterval = setInterval(() => {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
          this.send({ type: 'ping' });
        }
      }, 30000); // Ping every 30 seconds
    }
    
    stopKeepalive() {
      if (this.keepaliveInterval) {
        clearInterval(this.keepaliveInterval);
        this.keepaliveInterval = null;
      }
    }
    
    send(data) {
      if (this.ws && this.ws.readyState === WebSocket.OPEN) {
        this.ws.send(JSON.stringify(data));
      }
    }
    
    handleStateUpdate(state) {
      this.lastState = state;
      this.trigger('stateUpdate', state);
      
      // Update dashboard if on index page
      if (window.location.pathname.includes('index.html') || window.location.pathname === '/') {
        this.updateDashboard(state);
      }
    }
    
    handleHardwareEvent(event) {
      const action = event.action === 'add' ? 'Angeschlossen' : 'Entfernt';
      const message = `Festplatte ${action}: ${event.device}`;
      
      if (window.EnhancedUI) {
        const type = event.action === 'add' ? 'success' : 'warning';
        EnhancedUI.toast(message, type, 8000);
      }
      
      this.trigger('hardwareEvent', event);
    }
    
    handleZFSEvent(event) {
      let message = '';
      
      switch (event.type) {
        case 'resilver_started':
          message = `ZFS Resilver gestartet auf Pool "${event.pool}"`;
          if (window.EnhancedUI) {
            EnhancedUI.toast(message, 'info', 10000);
          }
          break;
        
        case 'resilver_completed':
          message = `✓ ZFS Resilver abgeschlossen auf Pool "${event.pool}"`;
          if (window.EnhancedUI) {
            EnhancedUI.toast(message, 'success', 10000);
          }
          break;
        
        case 'scrub_started':
          message = `ZFS Scrub gestartet auf Pool "${event.pool}"`;
          break;
        
        case 'scrub_completed':
          message = `✓ ZFS Scrub abgeschlossen auf Pool "${event.pool}"`;
          if (window.EnhancedUI) {
            EnhancedUI.toast(message, 'success', 8000);
          }
          break;
      }
      
      this.trigger('resilverUpdate', event);
    }
    
    handlePoolHealthChange(event) {
      const message = `⚠ Pool "${event.pool}": ${event.old_health} → ${event.new_health}`;
      
      if (window.EnhancedUI) {
        const type = event.new_health === 'ONLINE' ? 'success' : 'error';
        EnhancedUI.toast(message, type, 15000);
      }
    }
    
    handleDiskAlert(event) {
      const message = `⚠ Festplattentemperatur: ${event.disk} = ${event.temperature}°C`;
      
      if (window.EnhancedUI) {
        EnhancedUI.toast(message, 'warning', 10000);
      }
      
      this.trigger('diskAlert', event);
    }
    
    handleDiskChange(event) {
      if (event.type === 'disk_added') {
        const message = `Neue Festplatte erkannt: ${event.disk} (${event.model}, ${event.size})`;
        if (window.EnhancedUI) {
          EnhancedUI.toast(message, 'success', 10000);
        }
      } else if (event.type === 'disk_removed') {
        const message = `Festplatte entfernt: ${event.disk}`;
        if (window.EnhancedUI) {
          EnhancedUI.toast(message, 'warning', 8000);
        }
      }
    }
    
    updateConnectionStatus(connected) {
      const statusEl = document.getElementById('navConnectionStatus');
      if (statusEl) {
        if (connected) {
          statusEl.classList.remove('offline');
          statusEl.classList.add('online');
          statusEl.innerHTML = '<span class="status-dot"></span><span>Live</span>';
        } else {
          statusEl.classList.remove('online');
          statusEl.classList.add('offline');
          statusEl.innerHTML = '<span class="status-dot"></span><span>Verbindet...</span>';
        }
      }
    }
    
    updateDashboard(state) {
      // Update CPU
      const cpuEl = document.getElementById('cpuUsage');
      if (cpuEl) {
        cpuEl.textContent = `${state.cpu.usage}%`;
      }
      
      const cpuTempEl = document.getElementById('cpuTemp');
      if (cpuTempEl && state.cpu.temp) {
        cpuTempEl.textContent = `${state.cpu.temp}°C`;
      }
      
      // Update Memory
      const memEl = document.getElementById('memoryUsage');
      if (memEl) {
        memEl.textContent = `${state.memory.used} / ${state.memory.total} GB (${state.memory.percent}%)`;
      }
      
      const memProgressEl = document.getElementById('memoryProgress');
      if (memProgressEl) {
        memProgressEl.style.width = `${state.memory.percent}%`;
      }
      
      // Update Docker
      const dockerEl = document.getElementById('dockerStats');
      if (dockerEl && state.docker) {
        dockerEl.textContent = `${state.docker.running} / ${state.docker.containers} laufend`;
      }
      
      // Update ZFS pools with resilver progress
      if (state.zfs_pools) {
        for (const [poolName, poolInfo] of Object.entries(state.zfs_pools)) {
          const poolEl = document.getElementById(`pool-${poolName}`);
          if (poolEl) {
            // Update health
            const healthEl = poolEl.querySelector('.pool-health');
            if (healthEl) {
              healthEl.textContent = poolInfo.health;
              healthEl.className = `pool-health ${poolInfo.health === 'ONLINE' ? 'healthy' : 'degraded'}`;
            }
            
            // Show resilver progress if active
            if (poolInfo.resilver) {
              this.showResilverProgress(poolName, poolInfo.resilver);
            }
            
            // Show scrub progress if active
            if (poolInfo.scrub) {
              this.showScrubProgress(poolName, poolInfo.scrub);
            }
          }
        }
      }
    }
    
    showResilverProgress(pool, resilverInfo) {
      let progressEl = document.getElementById(`resilver-${pool}`);
      
      if (!progressEl) {
        // Create progress card
        const container = document.querySelector('.zfs-progress-container') || document.querySelector('.main');
        if (!container) return;
        
        progressEl = document.createElement('div');
        progressEl.id = `resilver-${pool}`;
        progressEl.className = 'card resilver-card';
        container.insertBefore(progressEl, container.firstChild);
      }
      
      const percent = resilverInfo.progress || 0;
      const remaining = resilverInfo.time_remaining || 'Berechne...';
      
      progressEl.innerHTML = `
        <div class="card-header">
          <div class="card-title">
            <span class="material-symbols-rounded">sync</span>
            ZFS Resilver - ${pool}
          </div>
        </div>
        <div class="progress-bar-container">
          <div class="progress-bar" style="width: ${percent}%"></div>
        </div>
        <div class="progress-info">
          <span>${percent.toFixed(1)}% abgeschlossen</span>
          <span>${remaining} verbleibend</span>
        </div>
      `;
    }
    
    showScrubProgress(pool, scrubInfo) {
      let progressEl = document.getElementById(`scrub-${pool}`);
      
      if (!progressEl) {
        const container = document.querySelector('.zfs-progress-container') || document.querySelector('.main');
        if (!container) return;
        
        progressEl = document.createElement('div');
        progressEl.id = `scrub-${pool}`;
        progressEl.className = 'card scrub-card';
        container.insertBefore(progressEl, container.firstChild);
      }
      
      const percent = scrubInfo.progress || 0;
      const remaining = scrubInfo.time_remaining || 'Berechne...';
      
      progressEl.innerHTML = `
        <div class="card-header">
          <div class="card-title">
            <span class="material-symbols-rounded">cleaning_services</span>
            ZFS Scrub - ${pool}
          </div>
        </div>
        <div class="progress-bar-container">
          <div class="progress-bar scrub" style="width: ${percent}%"></div>
        </div>
        <div class="progress-info">
          <span>${percent.toFixed(1)}% abgeschlossen</span>
          <span>${remaining} verbleibend</span>
        </div>
      `;
    }
    
    on(event, callback) {
      if (this.listeners[event]) {
        this.listeners[event].push(callback);
      }
    }
    
    off(event, callback) {
      if (this.listeners[event]) {
        this.listeners[event] = this.listeners[event].filter(cb => cb !== callback);
      }
    }
    
    trigger(event, data) {
      if (this.listeners[event]) {
        this.listeners[event].forEach(callback => {
          try {
            callback(data);
          } catch (error) {
            console.error(`Error in ${event} listener:`, error);
          }
        });
      }
    }
    
    getState() {
      return this.lastState;
    }
    
    disconnect() {
      if (this.ws) {
        this.ws.close();
      }
      this.stopKeepalive();
    }
  }
  
  // Create global instance
  window.DPlaneRT = new RealtimeClient();
  
  // Auto-connect on page load
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => {
      window.DPlaneRT.connect();
    });
  } else {
    window.DPlaneRT.connect();
  }
  
  // Reconnect on page visibility change
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && (!window.DPlaneRT.ws || window.DPlaneRT.ws.readyState !== WebSocket.OPEN)) {
      window.DPlaneRT.connect();
    }
  });
  
})();
