/**
 * D-PlaneOS Dashboard Health Widget
 * 
 * Displays critical health information for large storage systems:
 * - ZFS scrub status (last scrub, next scrub, progress)
 * - Disk temperatures (SMART data with warnings)
 * - Pool health summary
 * 
 * This addresses the "silent dashboard" problem where everything
 * looks static even though critical maintenance is happening.
 */

class HealthWidget {
    constructor(containerId) {
        this.container = document.getElementById(containerId);
        this.updateInterval = null;
        this.init();
    }
    
    init() {
        if (!this.container) {
            console.error('Health widget container not found');
            return;
        }
        
        // Initial load
        this.update();
        
        // Update every 30 seconds
        this.updateInterval = setInterval(() => this.update(), 30000);
    }
    
    async update() {
        try {
            // Fetch health data
            const [scrubData, smartData] = await Promise.all([
                this.fetchScrubStatus(),
                this.fetchSmartData()
            ]);
            
            // Render widget
            this.render(scrubData, smartData);
        } catch (error) {
            console.error('Failed to update health widget:', error);
            this.renderError();
        }
    }
    
    async fetchScrubStatus() {
        const response = await fetch('/api/zfs/pools');
        const data = await response.json();
        return data.success ? data.scrub_status : null;
    }
    
    async fetchSmartData() {
        const response = await fetch('/api/metrics/current');
        const data = await response.json();
        return data.success ? data.temperatures : [];
    }
    
    render(scrubData, smartData) {
        this.container.innerHTML = `
            <div class="health-widget">
                <div class="health-widget-header">
                    <h3>System Health</h3>
                    <span class="health-widget-updated">Updated: ${new Date().toLocaleTimeString()}</span>
                </div>
                
                <div class="health-widget-content">
                    ${this.renderScrubStatus(scrubData)}
                    ${this.renderTemperatures(smartData)}
                </div>
            </div>
        `;
    }
    
    renderScrubStatus(scrubData) {
        if (!scrubData) {
            return `
                <div class="health-section">
                    <div class="health-section-title">
                        <span class="material-symbols-outlined">sync</span>
                        ZFS Scrub Status
                    </div>
                    <div class="health-section-content">
                        <p class="health-info">No scrub data available</p>
                    </div>
                </div>
            `;
        }
        
        const {
            pool,
            status,
            last_scrub,
            next_scrub,
            progress,
            errors_found,
            scan_rate
        } = scrubData;
        
        // Determine status color
        let statusClass = 'health-status-ok';
        let statusIcon = 'check_circle';
        let statusText = 'Healthy';
        
        if (status === 'in_progress') {
            statusClass = 'health-status-warning';
            statusIcon = 'sync';
            statusText = 'Scrubbing...';
        } else if (errors_found > 0) {
            statusClass = 'health-status-error';
            statusIcon = 'error';
            statusText = 'Errors Found!';
        }
        
        // Format dates
        const lastScrubDate = last_scrub ? new Date(last_scrub * 1000).toLocaleDateString() : 'Never';
        const nextScrubDate = next_scrub ? new Date(next_scrub * 1000).toLocaleDateString() : 'Not scheduled';
        
        return `
            <div class="health-section">
                <div class="health-section-title">
                    <span class="material-symbols-outlined">sync</span>
                    ZFS Scrub Status - ${pool}
                </div>
                <div class="health-section-content">
                    <div class="health-status ${statusClass}">
                        <span class="material-symbols-outlined">${statusIcon}</span>
                        <span>${statusText}</span>
                    </div>
                    
                    <div class="health-details">
                        <div class="health-detail-row">
                            <span class="health-detail-label">Last Scrub:</span>
                            <span class="health-detail-value">${lastScrubDate}</span>
                        </div>
                        <div class="health-detail-row">
                            <span class="health-detail-label">Next Scrub:</span>
                            <span class="health-detail-value">${nextScrubDate}</span>
                        </div>
                        ${status === 'in_progress' ? `
                            <div class="health-detail-row">
                                <span class="health-detail-label">Progress:</span>
                                <span class="health-detail-value">${progress}%</span>
                            </div>
                            <div class="health-progress-bar">
                                <div class="health-progress-fill" style="width: ${progress}%"></div>
                            </div>
                            <div class="health-detail-row">
                                <span class="health-detail-label">Scan Rate:</span>
                                <span class="health-detail-value">${scan_rate}</span>
                            </div>
                        ` : ''}
                        ${errors_found > 0 ? `
                            <div class="health-detail-row health-error">
                                <span class="health-detail-label">Errors Found:</span>
                                <span class="health-detail-value">${errors_found}</span>
                            </div>
                        ` : ''}
                    </div>
                </div>
            </div>
        `;
    }
    
    renderTemperatures(smartData) {
        if (!smartData || smartData.length === 0) {
            return `
                <div class="health-section">
                    <div class="health-section-title">
                        <span class="material-symbols-outlined">thermostat</span>
                        Disk Temperatures
                    </div>
                    <div class="health-section-content">
                        <p class="health-info">No temperature data available</p>
                    </div>
                </div>
            `;
        }
        
        return `
            <div class="health-section">
                <div class="health-section-title">
                    <span class="material-symbols-outlined">thermostat</span>
                    Disk Temperatures
                </div>
                <div class="health-section-content">
                    <div class="health-temp-grid">
                        ${smartData.map(disk => this.renderDiskTemp(disk)).join('')}
                    </div>
                </div>
            </div>
        `;
    }
    
    renderDiskTemp(disk) {
        const { device, model, temperature } = disk;
        
        // Determine temperature status
        let tempClass = 'health-temp-ok';
        let tempIcon = 'check_circle';
        let tempWarning = '';
        
        if (temperature >= 55) {
            tempClass = 'health-temp-critical';
            tempIcon = 'warning';
            tempWarning = 'CRITICAL!';
        } else if (temperature >= 50) {
            tempClass = 'health-temp-warning';
            tempIcon = 'error';
            tempWarning = 'High!';
        } else if (temperature >= 45) {
            tempClass = 'health-temp-elevated';
            tempIcon = 'info';
            tempWarning = 'Elevated';
        }
        
        return `
            <div class="health-temp-card ${tempClass}">
                <div class="health-temp-header">
                    <span class="health-temp-device">${device}</span>
                    <span class="material-symbols-outlined">${tempIcon}</span>
                </div>
                <div class="health-temp-body">
                    <div class="health-temp-value">${temperature}°C</div>
                    ${tempWarning ? `<div class="health-temp-warning">${tempWarning}</div>` : ''}
                </div>
                <div class="health-temp-footer">
                    <span class="health-temp-model">${model}</span>
                </div>
            </div>
        `;
    }
    
    renderError() {
        this.container.innerHTML = `
            <div class="health-widget health-widget-error">
                <div class="health-error-icon">
                    <span class="material-symbols-outlined">error</span>
                </div>
                <div class="health-error-message">
                    Failed to load health data
                </div>
                <button class="health-retry-btn" onclick="healthWidget.update()">
                    Retry
                </button>
            </div>
        `;
    }
    
    destroy() {
        if (this.updateInterval) {
            clearInterval(this.updateInterval);
        }
    }
}

// Initialize on page load
let healthWidget;
document.addEventListener('DOMContentLoaded', () => {
    healthWidget = new HealthWidget('health-widget-container');
});
