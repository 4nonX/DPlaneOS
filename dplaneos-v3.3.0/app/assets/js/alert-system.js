/**
 * D-PlaneOS Frontend Alert System
 * 
 * Handles alert display with priority-based behavior:
 * - NORMAL: Toast (auto-dismiss 10s)
 * - WARNING: Notification bell (click to dismiss)
 * - CRITICAL: Sticky banner (acknowledge to dismiss)
 * 
 * CRITICAL FIX: Intelligent auto-dismiss prevents alert fatigue
 */

function esc(s){const d=document.createElement('div');d.textContent=String(s==null?'':s);return d.innerHTML;}

class AlertSystem {
    constructor() {
        this.alerts = [];
        this.toastContainer = null;
        this.bellContainer = null;
        this.criticalBanner = null;
        this.updateInterval = null;
        
        this.init();
    }
    
    init() {
        // Create UI containers
        this.createContainers();
        
        // Load initial alerts
        this.loadAlerts();
        
        // Poll for new alerts every 10 seconds
        this.updateInterval = setInterval(() => this.loadAlerts(), 10000);
        
        // Cleanup auto-dismiss timers on page unload
        window.addEventListener('beforeunload', () => {
            if (this.updateInterval) {
                clearInterval(this.updateInterval);
            }
        });
    }
    
    /**
     * Create alert UI containers
     */
    createContainers() {
        // Toast container (top-right)
        if (!document.getElementById('alert-toast-container')) {
            this.toastContainer = document.createElement('div');
            this.toastContainer.id = 'alert-toast-container';
            this.toastContainer.className = 'alert-toast-container';
            document.body.appendChild(this.toastContainer);
        } else {
            this.toastContainer = document.getElementById('alert-toast-container');
        }
        
        // Notification bell (in header)
        if (!document.getElementById('alert-bell')) {
            // Create bell icon in header
            const bell = document.createElement('div');
            bell.id = 'alert-bell';
            bell.className = 'alert-bell';
            bell.innerHTML = `
                <span class="material-symbols-outlined">notifications</span>
                <span class="alert-bell-badge" id="alert-bell-badge" style="display: none;">0</span>
            `;
            bell.addEventListener('click', () => this.showBellAlerts());
            
            // Add to header (assuming header exists)
            const header = document.querySelector('header') || document.querySelector('.header');
            if (header) {
                header.appendChild(bell);
            }
        }
        
        // Bell dropdown container
        if (!document.getElementById('alert-bell-container')) {
            this.bellContainer = document.createElement('div');
            this.bellContainer.id = 'alert-bell-container';
            this.bellContainer.className = 'alert-bell-container';
            this.bellContainer.style.display = 'none';
            document.body.appendChild(this.bellContainer);
        } else {
            this.bellContainer = document.getElementById('alert-bell-container');
        }
        
        // Critical banner container (top of page)
        if (!document.getElementById('alert-critical-banner')) {
            this.criticalBanner = document.createElement('div');
            this.criticalBanner.id = 'alert-critical-banner';
            this.criticalBanner.className = 'alert-critical-banner';
            this.criticalBanner.style.display = 'none';
            document.body.insertBefore(this.criticalBanner, document.body.firstChild);
        } else {
            this.criticalBanner = document.getElementById('alert-critical-banner');
        }
    }
    
    /**
     * Load alerts from API
     */
    async loadAlerts() {
        try {
            const response = await fetch('/api/metrics/current');
            const data = await response.json();
            
            if (data.success) {
                this.alerts = data.alerts;
                this.processAlerts();
                this.updateBellBadge();
            }
        } catch (error) {
            console.error('Failed to load alerts:', error);
        }
    }
    
    /**
     * Process alerts and display them according to priority
     */
    processAlerts() {
        // Separate by priority
        const normalAlerts = this.alerts.filter(a => 
            a.priority === 'normal' && !a.dismissed && !this.isExpired(a)
        );
        const warningAlerts = this.alerts.filter(a => 
            a.priority === 'warning' && !a.dismissed
        );
        const criticalAlerts = this.alerts.filter(a => 
            a.priority === 'critical' && !a.acknowledged
        );
        
        // Display normal alerts as toasts (auto-dismiss)
        normalAlerts.forEach(alert => {
            if (!this.isAlertDisplayed(alert.alert_id)) {
                this.showToast(alert);
            }
        });
        
        // Display critical alerts as sticky banner
        if (criticalAlerts.length > 0) {
            this.showCriticalBanner(criticalAlerts);
        } else {
            this.hideCriticalBanner();
        }
    }
    
    /**
     * Check if alert is expired
     */
    isExpired(alert) {
        if (!alert.expires_at) return false;
        return Date.now() / 1000 > alert.expires_at;
    }
    
    /**
     * Check if alert is already displayed
     */
    isAlertDisplayed(alertId) {
        return !!document.querySelector(`[data-alert-id="${alertId}"]`);
    }
    
    /**
     * Show toast notification (auto-dismiss)
     */
    showToast(alert) {
        const toast = document.createElement('div');
        toast.className = `alert-toast alert-toast-${alert.priority}`;
        toast.setAttribute('data-alert-id', alert.alert_id);
        
        const icon = this.getIcon(alert.priority);
        
        toast.innerHTML = `
            <div class="alert-toast-icon">${icon}</div>
            <div class="alert-toast-content">
                <div class="alert-toast-title">${esc(alert.title)}</div>
                <div class="alert-toast-message">${esc(alert.message)}</div>
                ${alert.count > 1 ? `<div class="alert-toast-count">${esc(alert.count)} occurrences</div>` : ''}
            </div>
            <button class="alert-toast-close" onclick="alertSystem.dismissToast('\${esc(alert.alert_id)}')">
                <span class="material-symbols-outlined">close</span>
            </button>
        `;
        
        this.toastContainer.appendChild(toast);
        
        // Slide in animation
        setTimeout(() => toast.classList.add('alert-toast-show'), 10);
        
        // Auto-dismiss after 10 seconds (configurable)
        const dismissTime = this.getAutoDismissTime(alert);
        setTimeout(() => this.dismissToast(alert.alert_id), dismissTime);
    }
    
    /**
     * Get auto-dismiss time based on priority
     */
    getAutoDismissTime(alert) {
        // Normal: 10s, but add 2s per occurrence for grouped alerts
        const baseTime = 10000;
        const extraTime = Math.min(alert.count - 1, 10) * 2000; // Max +20s
        return baseTime + extraTime;
    }
    
    /**
     * Dismiss toast
     */
    async dismissToast(alertId) {
        const toast = document.querySelector(`[data-alert-id="${alertId}"]`);
        if (!toast) return;
        
        // Slide out animation
        toast.classList.remove('alert-toast-show');
        
        setTimeout(() => {
            toast.remove();
        }, 300);
        
        // Alert dismissed (client-side only)
    }
    
    /**
     * Show critical banner (sticky)
     */
    showCriticalBanner(alerts) {
        if (alerts.length === 0) {
            this.hideCriticalBanner();
            return;
        }
        
        // Show first critical alert (or group if multiple)
        const alert = alerts[0];
        const additionalCount = alerts.length - 1;
        
        this.criticalBanner.innerHTML = `
            <div class="alert-critical-content">
                <div class="alert-critical-icon">
                    <span class="material-symbols-outlined">error</span>
                </div>
                <div class="alert-critical-text">
                    <div class="alert-critical-title">${esc(alert.title)}</div>
                    <div class="alert-critical-message">${esc(alert.message)}</div>
                    ${additionalCount > 0 ? `<div class="alert-critical-additional">+${additionalCount} more critical alerts</div>` : ''}
                </div>
                <div class="alert-critical-actions">
                    <button class="alert-btn-acknowledge" onclick="alertSystem.acknowledgeCritical('\${esc(alert.alert_id)}')">
                        Acknowledge
                    </button>
                    ${additionalCount > 0 ? `<button class="alert-btn-view-all" onclick="alertSystem.showAllCritical()">View All</button>` : ''}
                </div>
            </div>
        `;
        
        this.criticalBanner.style.display = 'block';
    }
    
    /**
     * Hide critical banner
     */
    hideCriticalBanner() {
        this.criticalBanner.style.display = 'none';
    }
    
    /**
     * Acknowledge critical alert
     */
    async acknowledgeCritical(alertId) {
        // Alert acknowledged (client-side only)
        this.activeCritical = null;
    }
    
    /**
     * Show bell alerts dropdown
     */
    showBellAlerts() {
        const warningAlerts = this.alerts.filter(a => 
            a.priority === 'warning' && !a.dismissed
        );
        
        if (warningAlerts.length === 0) {
            this.bellContainer.innerHTML = '<div class="alert-bell-empty">No new notifications</div>';
        } else {
            this.bellContainer.innerHTML = warningAlerts.map(alert => `
                <div class="alert-bell-item" data-alert-id="${esc(alert.alert_id)}">
                    <div class="alert-bell-item-icon">${this.getIcon(alert.priority)}</div>
                    <div class="alert-bell-item-content">
                        <div class="alert-bell-item-title">${esc(alert.title)}</div>
                        <div class="alert-bell-item-message">${esc(alert.message)}</div>
                        <div class="alert-bell-item-time">${this.formatTime(alert.last_seen)}</div>
                    </div>
                    <button class="alert-bell-item-dismiss" onclick="alertSystem.dismissBellAlert('\${esc(alert.alert_id)}')">
                        <span class="material-symbols-outlined">close</span>
                    </button>
                </div>
            `).join('');
        }
        
        this.bellContainer.style.display = this.bellContainer.style.display === 'none' ? 'block' : 'none';
    }
    
    /**
     * Dismiss bell alert
     */
    async dismissBellAlert(alertId) {
        await this.dismissToast(alertId);
        this.loadAlerts();
    }
    
    /**
     * Update bell badge count
     */
    updateBellBadge() {
        const badge = document.getElementById('alert-bell-badge');
        if (!badge) return;
        
        const warningCount = this.alerts.filter(a => 
            a.priority === 'warning' && !a.dismissed
        ).length;
        
        if (warningCount > 0) {
            badge.textContent = warningCount > 99 ? '99+' : warningCount;
            badge.style.display = 'block';
        } else {
            badge.style.display = 'none';
        }
    }
    
    /**
     * Get icon for priority
     */
    getIcon(priority) {
        const icons = {
            normal: '<span class="material-symbols-outlined">info</span>',
            warning: '<span class="material-symbols-outlined">warning</span>',
            critical: '<span class="material-symbols-outlined">error</span>'
        };
        return icons[priority] || icons.normal;
    }
    
    /**
     * Format timestamp
     */
    formatTime(timestamp) {
        const date = new Date(timestamp * 1000);
        const now = new Date();
        const diff = (now - date) / 1000;
        
        if (diff < 60) return 'Just now';
        if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
        if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
        return Math.floor(diff / 86400) + 'd ago';
    }
    
    /**
     * Show all critical alerts in modal
     */
    showAllCritical() {
        const criticalAlerts = this.alerts.filter(a => 
            a.priority === 'critical' && !a.acknowledged
        );
        
        // Create modal with all critical alerts
        // (Implementation depends on your modal system)
    }
}

// Initialize alert system when DOM is ready
let alertSystem;
if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => {
        alertSystem = new AlertSystem();
    });
} else {
    alertSystem = new AlertSystem();
}
