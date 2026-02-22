// D-PlaneOS - Connection Status Monitor

class ConnectionMonitor {
  constructor() {
    this.isOnline = navigator.onLine;
    this.indicator = null;
    this.checkInterval = null;
    this.init();
  }
  
  init() {
    // Create status indicator
    this.indicator = document.createElement('div');
    this.indicator.className = 'connection-status';
    this.indicator.style.cssText = `
      position: fixed;
      bottom: 24px;
      left: 24px;
      background: rgba(30,30,30,0.95);
      backdrop-filter: blur(12px);
      padding: 12px 16px;
      border-radius: 8px;
      border: 1px solid rgba(255,255,255,0.1);
      display: none;
      align-items: center;
      gap: 8px;
      z-index: 9999;
      font-size: 13px;
      animation: slideIn 0.3s;
    `;
    document.body.appendChild(this.indicator);
    
    // Listen to online/offline events
    window.addEventListener('online', () => this.setStatus(true));
    window.addEventListener('offline', () => this.setStatus(false));
    
    // Periodic API health check
    this.startHealthCheck();
  }
  
  setStatus(online) {
    this.isOnline = online;
    
    if (online) {
      this.indicator.innerHTML = `
        <span class="status-dot status-online"></span>
        <span>Connected</span>
      `;
      this.indicator.style.display = 'flex';
      setTimeout(() => {
        this.indicator.style.display = 'none';
      }, 3000);
    } else {
      this.indicator.innerHTML = `
        <span class="status-dot status-offline"></span>
        <span>Connection lost. Retrying...</span>
      `;
      this.indicator.style.display = 'flex';
    }
  }
  
  async startHealthCheck() {
    // Check every 30 seconds
    this.checkInterval = setInterval(async () => {
      try {
        const response = await fetch('/api/auth/check', {
          method: 'GET',
          cache: 'no-cache'
        });
        
        if (response.ok && !this.isOnline) {
          this.setStatus(true);
        }
      } catch (error) {
        if (this.isOnline) {
          this.setStatus(false);
        }
      }
    }, 30000);
  }
  
  stop() {
    if (this.checkInterval) {
      clearInterval(this.checkInterval);
    }
  }
}

// Initialize connection monitor
if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', () => {
    window.connectionMonitor = new ConnectionMonitor();
  });
} else {
  window.connectionMonitor = new ConnectionMonitor();
}
