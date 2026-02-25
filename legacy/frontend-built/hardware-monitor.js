/**
 * D-PlaneOS v1.14.0 - Frontend Hardware Monitor
 * Real-time hardware event handling with Server-Sent Events
 */

class HardwareMonitorClient {
    constructor() {
        this.eventSource = null;
        this.reconnectInterval = 5000; // 5 seconds
        this.reconnectAttempts = 0;
        this.maxReconnectAttempts = 10;
        this.listeners = {};
        this.connected = false;
    }

    /**
     * Connect to hardware monitoring stream
     */
    connect() {
        if (this.eventSource) {
            this.disconnect();
        }

        console.log('🔌 Connecting to hardware monitor...');

        try {
            this.eventSource = new EventSource('/api/events.php');

            // Connection opened
            this.eventSource.addEventListener('open', (e) => {
                console.log('✅ Hardware monitor connected');
                this.connected = true;
                this.reconnectAttempts = 0;
                toast.success('Hardware monitoring active');
            });

            // Connection event
            this.eventSource.addEventListener('connected', (e) => {
                const data = JSON.parse(e.data);
                console.log('📡 Monitor ready:', data.message);
            });

            // Disk added event
            this.eventSource.addEventListener('disk_added', (e) => {
                const data = JSON.parse(e.data);
                console.log('➕ Disk added:', data.disk);
                
                toast.success(data.message, 8000);
                
                // Trigger callback
                this.trigger('disk_added', data);
                
                // Update disk lists if visible
                this.refreshDiskLists();
            });

            // Disk removed event
            this.eventSource.addEventListener('disk_removed', (e) => {
                const data = JSON.parse(e.data);
                console.log('➖ Disk removed:', data.disk);
                
                if (data.critical) {
                    toast.error('⚠️ ' + data.message + ' - Pool may be degraded!', 15000);
                } else {
                    toast.warning(data.message, 8000);
                }
                
                // Trigger callback
                this.trigger('disk_removed', data);
                
                // Update disk lists
                this.refreshDiskLists();
            });

            // Disk SMART status changed
            this.eventSource.addEventListener('disk_smart_changed', (e) => {
                const data = JSON.parse(e.data);
                console.log('⚠️ SMART changed:', data);
                
                if (data.critical) {
                    toast.error('❌ ' + data.message, 0); // Don't auto-dismiss
                    
                    // Show critical alert banner
                    this.showCriticalAlert(data.message, 'Replace disk immediately!');
                } else {
                    toast.warning(data.message, 10000);
                }
                
                this.trigger('disk_smart_changed', data);
            });

            // Disk usage changed (added/removed from pool)
            this.eventSource.addEventListener('disk_usage_changed', (e) => {
                const data = JSON.parse(e.data);
                console.log('📊 Disk usage changed:', data);
                
                toast.info(data.message, 6000);
                this.trigger('disk_usage_changed', data);
                
                // Update pool and disk lists
                this.refreshDiskLists();
                this.refreshPoolLists();
            });

            // Pool health critical
            this.eventSource.addEventListener('pool_health_critical', (e) => {
                const data = JSON.parse(e.data);
                console.error('🚨 POOL CRITICAL:', data);
                
                toast.error('🚨 ' + data.message, 0); // Don't auto-dismiss
                
                // Show critical alert banner
                this.showCriticalAlert(
                    `Pool "${data.pool}" is ${data.health}`,
                    'Immediate action required! Check pool status and replace failed disks.'
                );
                
                this.trigger('pool_health_critical', data);
                
                // Update dashboard health
                this.updateSystemHealth();
            });

            // Pool health warning
            this.eventSource.addEventListener('pool_health_warning', (e) => {
                const data = JSON.parse(e.data);
                console.warn('⚠️ POOL WARNING:', data);
                
                toast.warning('⚠️ ' + data.message, 12000);
                this.trigger('pool_health_warning', data);
                
                // Update dashboard
                this.updateSystemHealth();
            });

            // ZFS events
            this.eventSource.addEventListener('zfs_event', (e) => {
                const data = JSON.parse(e.data);
                console.log('📋 ZFS Event:', data);
                
                if (data.critical) {
                    toast.error(data.message, 10000);
                } else if (data.severity === 'error') {
                    toast.error(data.message, 8000);
                } else if (data.severity === 'warning') {
                    toast.warning(data.message, 6000);
                } else {
                    // Don't show info-level ZFS events as toasts (too noisy)
                    console.log('ZFS info:', data.message);
                }
                
                this.trigger('zfs_event', data);
            });

            // Error event
            this.eventSource.addEventListener('error', (e) => {
                console.error('❌ Hardware monitor error:', e);
                this.connected = false;
                
                if (this.eventSource.readyState === EventSource.CLOSED) {
                    this.handleDisconnect();
                }
            });

        } catch (error) {
            console.error('Failed to connect to hardware monitor:', error);
            this.handleDisconnect();
        }
    }

    /**
     * Handle disconnection and attempt reconnect
     */
    handleDisconnect() {
        console.warn('⚠️ Hardware monitor disconnected');
        this.connected = false;
        
        if (this.reconnectAttempts < this.maxReconnectAttempts) {
            this.reconnectAttempts++;
            console.log(`🔄 Reconnecting... (attempt ${this.reconnectAttempts}/${this.maxReconnectAttempts})`);
            
            setTimeout(() => {
                this.connect();
            }, this.reconnectInterval);
        } else {
            console.error('❌ Max reconnect attempts reached');
            toast.error('Hardware monitoring disconnected. Refresh page to reconnect.', 0);
        }
    }

    /**
     * Disconnect from event stream
     */
    disconnect() {
        if (this.eventSource) {
            this.eventSource.close();
            this.eventSource = null;
            this.connected = false;
            console.log('🔌 Hardware monitor disconnected');
        }
    }

    /**
     * Register event listener
     */
    on(event, callback) {
        if (!this.listeners[event]) {
            this.listeners[event] = [];
        }
        this.listeners[event].push(callback);
    }

    /**
     * Trigger event callbacks
     */
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

    /**
     * Refresh disk lists in UI
     */
    refreshDiskLists() {
        // Refresh wizard disk selection if visible
        const wizardDiskList = document.getElementById('wizardDiskList');
        if (wizardDiskList && wizardDiskList.offsetParent !== null) {
            console.log('🔄 Refreshing wizard disk list...');
            if (typeof loadWizardDisks === 'function') {
                loadWizardDisks();
            }
        }

        // Refresh storage page disk list if visible
        const disksList = document.getElementById('disksList');
        if (disksList && disksList.offsetParent !== null) {
            console.log('🔄 Refreshing storage disk list...');
            if (typeof loadDisks === 'function') {
                loadDisks();
            }
        }
    }

    /**
     * Refresh pool lists in UI
     */
    refreshPoolLists() {
        // Refresh pool list if visible
        const poolsList = document.getElementById('poolsList');
        if (poolsList && poolsList.offsetParent !== null) {
            console.log('🔄 Refreshing pools list...');
            if (typeof loadPools === 'function') {
                loadPools();
            }
        }

        // Refresh dashboard pools
        if (typeof loadDashboardPools === 'function') {
            loadDashboardPools();
        }
    }

    /**
     * Update system health on dashboard
     */
    updateSystemHealth() {
        if (typeof refreshDashboard === 'function') {
            refreshDashboard();
        }
    }

    /**
     * Show critical alert banner
     */
    showCriticalAlert(title, message) {
        // Remove existing alert if present
        const existingAlert = document.getElementById('criticalAlert');
        if (existingAlert) {
            existingAlert.remove();
        }

        // Create alert banner
        const alert = document.createElement('div');
        alert.id = 'criticalAlert';
        alert.style.cssText = `
            position: fixed;
            top: 70px;
            left: 0;
            right: 0;
            background: linear-gradient(135deg, #dc2626 0%, #991b1b 100%);
            color: white;
            padding: 16px 24px;
            box-shadow: 0 8px 24px rgba(220, 38, 38, 0.4);
            z-index: 9998;
            animation: slideDown 0.3s ease;
            border-bottom: 3px solid #7f1d1d;
        `;

        alert.innerHTML = `
            <div style="max-width: 1400px; margin: 0 auto; display: flex; align-items: center; justify-content: space-between; gap: 16px;">
                <div style="display: flex; align-items: center; gap: 16px;">
                    <div style="font-size: 32px; animation: pulse 2s infinite;">🚨</div>
                    <div>
                        <div style="font-size: 18px; font-weight: 700; margin-bottom: 4px;">${title}</div>
                        <div style="font-size: 14px; opacity: 0.9;">${message}</div>
                    </div>
                </div>
                <button onclick="this.parentElement.parentElement.remove()" 
                        style="background: rgba(255,255,255,0.2); border: none; color: white; padding: 8px 16px; border-radius: 6px; cursor: pointer; font-weight: 600; transition: all 0.2s;">
                    Dismiss
                </button>
            </div>
        `;

        document.body.appendChild(alert);
    }

    /**
     * Check connection status
     */
    isConnected() {
        return this.connected;
    }
}

// Create global hardware monitor instance
const hardwareMonitor = new HardwareMonitorClient();

// Auto-connect when page loads
document.addEventListener('DOMContentLoaded', function() {
    // Connect to hardware monitor
    hardwareMonitor.connect();
    
    console.log('🔧 Hardware monitoring system initialized');
});

// Cleanup on page unload
window.addEventListener('beforeunload', function() {
    hardwareMonitor.disconnect();
});

// Export to window
window.hardwareMonitor = hardwareMonitor;
