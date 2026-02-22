/**
 * D-PlaneOS UPS Dashboard Widget
 * 
 * Displays UPS status including:
 * - Battery charge level
 * - Runtime remaining
 * - Input/output voltage
 * - Load percentage
 * - Status (online/on battery/charging)
 * 
 * Updates every 10 seconds
 */

class UPSWidget {
    constructor(containerId) {
        this.container = document.getElementById(containerId);
        this.updateInterval = null;
        this.init();
    }
    
    init() {
        if (!this.container) {
            console.error('UPS widget container not found');
            return;
        }
        
        // Initial load
        this.update();
        
        // Update every 10 seconds
        this.updateInterval = setInterval(() => this.update(), 10000);
    }
    
    async update() {
        try {
            const data = await this.fetchUPSData();
            
            if (data.available) {
                this.render(data);
            } else {
                this.renderNotAvailable();
            }
        } catch (error) {
            console.error('Failed to update UPS widget:', error);
            this.renderError();
        }
    }
    
    async fetchUPSData() {
        const response = await fetch('/api/system/ups');
        const data = await response.json();
        return data.success ? data.ups : { available: false };
    }
    
    render(data) {
        const {
            status,
            battery_charge,
            battery_runtime,
            input_voltage,
            output_voltage,
            load_percent,
            model,
            manufacturer
        } = data;
        
        // Determine status color and icon
        let statusClass = 'ups-status-online';
        let statusIcon = 'power';
        let statusText = 'Online';
        
        if (status === 'OB') {
            statusClass = 'ups-status-battery';
            statusIcon = 'battery_alert';
            statusText = 'On Battery!';
        } else if (status === 'LB') {
            statusClass = 'ups-status-critical';
            statusIcon = 'battery_0_bar';
            statusText = 'Low Battery!';
        } else if (status === 'CHRG') {
            statusClass = 'ups-status-charging';
            statusIcon = 'battery_charging_full';
            statusText = 'Charging';
        }
        
        // Battery charge color
        let chargeClass = 'ups-battery-ok';
        if (battery_charge < 20) {
            chargeClass = 'ups-battery-critical';
        } else if (battery_charge < 50) {
            chargeClass = 'ups-battery-low';
        }
        
        this.container.innerHTML = `
            <div class="ups-widget">
                <div class="ups-widget-header">
                    <h3>UPS Status</h3>
                    <div class="ups-status ${statusClass}">
                        <span class="material-symbols-outlined">${statusIcon}</span>
                        <span>${statusText}</span>
                    </div>
                </div>
                
                <div class="ups-widget-content">
                    <!-- Battery Info -->
                    <div class="ups-section">
                        <div class="ups-section-title">Battery</div>
                        <div class="ups-battery-display ${chargeClass}">
                            <div class="ups-battery-icon">
                                <div class="ups-battery-fill" style="width: ${battery_charge}%"></div>
                                <span class="ups-battery-percent">${battery_charge}%</span>
                            </div>
                            <div class="ups-battery-runtime">
                                <span class="material-symbols-outlined">schedule</span>
                                <span>${this.formatRuntime(battery_runtime)}</span>
                            </div>
                        </div>
                    </div>
                    
                    <!-- Power Info -->
                    <div class="ups-section">
                        <div class="ups-section-title">Power</div>
                        <div class="ups-power-grid">
                            <div class="ups-power-card">
                                <div class="ups-power-label">Input</div>
                                <div class="ups-power-value">${input_voltage}V</div>
                            </div>
                            <div class="ups-power-arrow">→</div>
                            <div class="ups-power-card">
                                <div class="ups-power-label">Output</div>
                                <div class="ups-power-value">${output_voltage}V</div>
                            </div>
                        </div>
                    </div>
                    
                    <!-- Load Info -->
                    <div class="ups-section">
                        <div class="ups-section-title">Load</div>
                        <div class="ups-load-display">
                            <div class="ups-load-bar">
                                <div class="ups-load-fill" style="width: ${load_percent}%"></div>
                            </div>
                            <div class="ups-load-text">${load_percent}%</div>
                        </div>
                    </div>
                    
                    <!-- Device Info -->
                    <div class="ups-device-info">
                        <div class="ups-device-model">${manufacturer} ${model}</div>
                        <div class="ups-device-updated">Updated: ${new Date().toLocaleTimeString()}</div>
                    </div>
                </div>
            </div>
        `;
    }
    
    renderNotAvailable() {
        this.container.innerHTML = `
            <div class="ups-widget ups-widget-not-available">
                <div class="ups-widget-header">
                    <h3>UPS Status</h3>
                </div>
                <div class="ups-not-available-content">
                    <div class="ups-not-available-icon">
                        <span class="material-symbols-outlined">power_off</span>
                    </div>
                    <div class="ups-not-available-text">
                        <h4>No UPS Detected</h4>
                        <p>Connect a UPS via USB to enable monitoring</p>
                    </div>
                    <button class="ups-setup-btn" onclick="window.location.href='/pages/settings.html#ups'">
                        Setup UPS
                    </button>
                </div>
            </div>
        `;
    }
    
    renderError() {
        this.container.innerHTML = `
            <div class="ups-widget ups-widget-error">
                <div class="ups-error-icon">
                    <span class="material-symbols-outlined">error</span>
                </div>
                <div class="ups-error-message">
                    Failed to load UPS data
                </div>
                <button class="ups-retry-btn" onclick="upsWidget.update()">
                    Retry
                </button>
            </div>
        `;
    }
    
    formatRuntime(seconds) {
        if (!seconds || seconds < 0) return 'Unknown';
        
        const hours = Math.floor(seconds / 3600);
        const minutes = Math.floor((seconds % 3600) / 60);
        
        if (hours > 0) {
            return `${hours}h ${minutes}m`;
        } else {
            return `${minutes}m`;
        }
    }
    
    destroy() {
        if (this.updateInterval) {
            clearInterval(this.updateInterval);
        }
    }
}

// Initialize on page load
let upsWidget;
document.addEventListener('DOMContentLoaded', () => {
    upsWidget = new UPSWidget('ups-widget-container');
});
