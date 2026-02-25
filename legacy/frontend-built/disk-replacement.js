/**
 * D-PlaneOS Disk Replacement Wizard
 * Frontend JavaScript - v1.13.0
 */

class DiskReplacementWizard {
    constructor() {
        this.currentStep = 1;
        this.totalSteps = 5;
        this.selectedPool = null;
        this.selectedFailedDisk = null;
        this.selectedNewDisk = null;
        this.replacementId = null;
        this.resilverCheckInterval = null;
        this.init();
    }
    
    init() {
        this.checkPoolStatus();
        this.setupEventListeners();
    }
    
    setupEventListeners() {
        document.getElementById('start-wizard-btn')?.addEventListener('click', () => this.startWizard());
        document.getElementById('wizard-next')?.addEventListener('click', () => this.nextStep());
        document.getElementById('wizard-prev')?.addEventListener('click', () => this.prevStep());
        document.getElementById('wizard-cancel')?.addEventListener('click', () => this.cancelWizard());
    }
    
    async checkPoolStatus() {
        try {
            const response = await fetch('/api/disk-replacement.php?action=status');
            const data = await response.json();
            
            this.renderPoolStatus(data.pools);
            
        } catch (error) {
            console.error('Failed to check pool status:', error);
        }
    }
    
    renderPoolStatus(pools) {
        const container = document.getElementById('pool-status-cards');
        if (!container) return;
        
        container.innerHTML = pools.map(pool => {
            const hasFailed = pool.devices.some(d => d.is_failed);
            const statusClass = pool.status === 'ONLINE' ? 'success' : 
                               pool.status === 'DEGRADED' ? 'warning' : 'danger';
            
            return `
                <div class="card pool-card" data-pool="${pool.name}">
                    <div class="card-header">
                        Pool: ${pool.name}
                        <span class="badge badge-${statusClass}">${pool.status}</span>
                    </div>
                    <div class="card-body">
                        <div class="disk-list">
                            ${pool.devices.map(device => `
                                <div class="disk-item ${device.is_failed ? 'failed' : ''}">
                                    <span class="disk-icon">${device.is_failed ? '❌' : '✓'}</span>
                                    <span class="disk-name">${device.name}</span>
                                    <span class="badge badge-${device.status === 'ONLINE' ? 'success' : 'danger'}">
                                        ${device.status}
                                    </span>
                                </div>
                            `).join('')}
                        </div>
                        ${hasFailed ? `
                            <button class="btn btn-primary" style="margin-top: 16px;" 
                                    onclick="diskWizard.startReplacement('${pool.name}')">
                                🔧 Start Disk Replacement
                            </button>
                        ` : ''}
                    </div>
                </div>
            `;
        }).join('');
    }
    
    startReplacement(poolName) {
        this.selectedPool = poolName;
        this.showWizardModal();
        this.loadStep1();
    }
    
    showWizardModal() {
        const modal = document.createElement('div');
        modal.id = 'disk-wizard-modal';
        modal.className = 'modal active';
        modal.innerHTML = `
            <div class="modal-content" style="max-width: 900px;">
                <div class="modal-header">
                    <h3>🔧 Disk Replacement Wizard</h3>
                    <button class="modal-close" onclick="diskWizard.cancelWizard()">&times;</button>
                </div>
                <div class="modal-body">
                    <!-- Progress Steps -->
                    <div class="wizard-steps" id="wizard-steps">
                        ${this.renderSteps()}
                    </div>
                    
                    <!-- Step Content -->
                    <div id="wizard-content" style="min-height: 300px; margin-top: 32px;">
                        <!-- Dynamic content -->
                    </div>
                    
                    <!-- Navigation -->
                    <div style="display: flex; justify-content: space-between; margin-top: 32px; padding-top: 24px; border-top: 1px solid #334155;">
                        <button id="wizard-prev" class="btn btn-secondary" style="display: none;">
                            ← Previous
                        </button>
                        <button id="wizard-cancel" class="btn btn-secondary">
                            Cancel
                        </button>
                        <button id="wizard-next" class="btn btn-primary">
                            Next →
                        </button>
                    </div>
                </div>
            </div>
        `;
        document.body.appendChild(modal);
        
        // Re-attach event listeners
        document.getElementById('wizard-next').addEventListener('click', () => this.nextStep());
        document.getElementById('wizard-prev').addEventListener('click', () => this.prevStep());
        document.getElementById('wizard-cancel').addEventListener('click', () => this.cancelWizard());
    }
    
    renderSteps() {
        const steps = [
            'Identify Failed Disk',
            'Offline Disk',
            'Install New Disk',
            'Replace & Resilver',
            'Complete'
        ];
        
        return `
            <div style="display: flex; justify-content: space-between; align-items: center;">
                ${steps.map((step, index) => `
                    <div class="wizard-step ${index + 1 === this.currentStep ? 'active' : ''} 
                                            ${index + 1 < this.currentStep ? 'completed' : ''}">
                        <div class="wizard-step-number">${index + 1}</div>
                        <div class="wizard-step-label">${step}</div>
                    </div>
                    ${index < steps.length - 1 ? '<div class="wizard-connector"></div>' : ''}
                `).join('')}
            </div>
        `;
    }
    
    async loadStep1() {
        this.updateStepDisplay();
        
        try {
            const response = await fetch(`/api/disk-replacement.php?action=identify-failed&pool=${this.selectedPool}`);
            const data = await response.json();
            
            const content = document.getElementById('wizard-content');
            content.innerHTML = `
                <h3>Step 1: Identify Failed Disk</h3>
                <p style="color: #94a3b8; margin-bottom: 24px;">
                    The following disk(s) have failed in pool <strong>${this.selectedPool}</strong>:
                </p>
                
                ${data.failed_disks.map(disk => `
                    <div class="card" style="margin-bottom: 16px; border: 2px solid #f87171;">
                        <div style="display: flex; justify-content: space-between; align-items: start;">
                            <div>
                                <h4 style="margin-bottom: 8px;">❌ ${disk.device}</h4>
                                <div style="font-size: 14px; color: #94a3b8;">
                                    <strong>Status:</strong> ${disk.status}<br>
                                    ${disk.smart ? `
                                        <strong>Serial:</strong> ${disk.smart.serial || 'Unknown'}<br>
                                        <strong>Model:</strong> ${disk.smart.model || 'Unknown'}<br>
                                        <strong>Capacity:</strong> ${disk.smart.capacity || 'Unknown'}
                                    ` : ''}
                                </div>
                                <div style="margin-top: 12px; padding: 12px; background: rgba(248, 113, 113, 0.1); border-radius: 6px;">
                                    <strong>Errors:</strong>
                                    Read: ${disk.errors.read} | 
                                    Write: ${disk.errors.write} | 
                                    Checksum: ${disk.errors.checksum}
                                </div>
                            </div>
                            <button class="btn btn-primary" 
                                    onclick="diskWizard.selectFailedDisk('${disk.device}')">
                                Select This Disk
                            </button>
                        </div>
                    </div>
                `).join('')}
            `;
            
        } catch (error) {
            this.showNotification('error', `Failed to identify disks: ${error.message}`);
        }
    }
    
    selectFailedDisk(device) {
        this.selectedFailedDisk = device;
        this.showNotification('success', `Selected: ${device}`);
        this.nextStep();
    }
    
    async loadStep2() {
        this.updateStepDisplay();
        
        const content = document.getElementById('wizard-content');
        content.innerHTML = `
            <h3>Step 2: Take Disk Offline</h3>
            
            <div class="alert alert-warning">
                <strong>⚠️ Important:</strong> Taking this disk offline will reduce redundancy.
                The pool will run in DEGRADED mode until replacement is complete.
            </div>
            
            <div class="card" style="margin: 24px 0;">
                <h4>Disk to offline:</h4>
                <div style="padding: 16px; background: rgba(248, 113, 113, 0.1); border-radius: 6px; margin-top: 12px;">
                    <code>${this.selectedFailedDisk}</code>
                </div>
                
                <div style="margin-top: 20px;">
                    <strong>Command that will be executed:</strong>
                    <pre style="background: #0f3460; padding: 12px; border-radius: 6px; margin-top: 8px;">zpool offline ${this.selectedPool} ${this.selectedFailedDisk}</pre>
                </div>
            </div>
            
            <button class="btn btn-primary" style="width: 100%; padding: 14px; font-size: 16px;" 
                    onclick="diskWizard.offlineDisk()">
                Take Disk Offline
            </button>
        `;
    }
    
    async offlineDisk() {
        try {
            const response = await fetch('/api/disk-replacement.php?action=offline', {
                method: 'POST',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
                body: new URLSearchParams({
                    pool: this.selectedPool,
                    device: this.selectedFailedDisk
                })
            });
            
            const data = await response.json();
            
            if (!response.ok) {
                throw new Error(data.error);
            }
            
            this.showNotification('success', 'Disk taken offline successfully');
            this.nextStep();
            
        } catch (error) {
            this.showNotification('error', `Failed to offline disk: ${error.message}`);
        }
    }
    
    async loadStep3() {
        this.updateStepDisplay();
        
        const content = document.getElementById('wizard-content');
        content.innerHTML = `
            <h3>Step 3: Install New Disk</h3>
            
            <div class="alert alert-info">
                <strong>Physical Installation Required:</strong><br>
                1. Safely remove the failed disk from your server<br>
                2. Install the new replacement disk<br>
                3. Power on and wait for the system to detect it
            </div>
            
            <div style="margin: 24px 0;">
                <button class="btn btn-primary" onclick="diskWizard.scanForNewDisks()">
                    🔍 Scan for New Disks
                </button>
            </div>
            
            <div id="available-disks-list">
                <p style="color: #94a3b8;">Click "Scan for New Disks" after installation...</p>
            </div>
        `;
    }
    
    async scanForNewDisks() {
        this.showNotification('info', 'Scanning for new disks...');
        
        try {
            const response = await fetch('/api/disk-replacement.php?action=scan-new');
            const data = await response.json();
            
            const container = document.getElementById('available-disks-list');
            
            if (data.available_disks.length === 0) {
                container.innerHTML = `
                    <div class="alert alert-warning">
                        No new disks detected. Please check:
                        <ul style="margin-top: 8px;">
                            <li>Disk is properly installed and connected</li>
                            <li>Server has power to the disk</li>
                            <li>Disk is not already in use</li>
                        </ul>
                    </div>
                `;
                return;
            }
            
            container.innerHTML = `
                <h4 style="margin-bottom: 16px;">Available Disks:</h4>
                ${data.available_disks.map(disk => `
                    <div class="card" style="margin-bottom: 12px; cursor: pointer; transition: all 0.2s;" 
                         onclick="diskWizard.selectNewDisk('${disk.device}', '${disk.size}')">
                        <div style="display: flex; justify-content: space-between; align-items: center;">
                            <div>
                                <h4>💾 ${disk.device}</h4>
                                <p style="color: #94a3b8; margin-top: 4px;">
                                    Size: ${disk.size} | Serial: ${disk.serial}
                                </p>
                            </div>
                            <button class="btn btn-primary">Select</button>
                        </div>
                    </div>
                `).join('')}
            `;
            
        } catch (error) {
            this.showNotification('error', `Scan failed: ${error.message}`);
        }
    }
    
    selectNewDisk(device, size) {
        this.selectedNewDisk = device;
        this.showNotification('success', `Selected new disk: ${device} (${size})`);
        this.nextStep();
    }
    
    async loadStep4() {
        this.updateStepDisplay();
        
        const content = document.getElementById('wizard-content');
        content.innerHTML = `
            <h3>Step 4: Replace Disk & Start Resilver</h3>
            
            <div class="card" style="margin: 24px 0;">
                <table style="width: 100%;">
                    <tr>
                        <td style="padding: 12px;"><strong>Pool:</strong></td>
                        <td style="padding: 12px;">${this.selectedPool}</td>
                    </tr>
                    <tr>
                        <td style="padding: 12px;"><strong>Failed Disk:</strong></td>
                        <td style="padding: 12px;"><code>${this.selectedFailedDisk}</code></td>
                    </tr>
                    <tr>
                        <td style="padding: 12px;"><strong>New Disk:</strong></td>
                        <td style="padding: 12px;"><code>${this.selectedNewDisk}</code></td>
                    </tr>
                </table>
                
                <div style="margin-top: 20px; padding: 12px; background: rgba(102, 126, 234, 0.1); border-radius: 6px;">
                    <strong>Command:</strong>
                    <pre style="margin-top: 8px;">zpool replace ${this.selectedPool} ${this.selectedFailedDisk} ${this.selectedNewDisk}</pre>
                </div>
            </div>
            
            <button class="btn btn-primary" style="width: 100%; padding: 14px; font-size: 16px;" 
                    onclick="diskWizard.startReplacement()">
                🚀 Start Replacement
            </button>
            
            <div id="resilver-progress" style="margin-top: 24px; display: none;">
                <!-- Progress will appear here -->
            </div>
        `;
    }
    
    async startReplacementProcess() {
        try {
            const response = await fetch('/api/disk-replacement.php?action=replace', {
                method: 'POST',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
                body: new URLSearchParams({
                    pool: this.selectedPool,
                    old_device: this.selectedFailedDisk,
                    new_device: this.selectedNewDisk
                })
            });
            
            const data = await response.json();
            
            if (!response.ok) {
                throw new Error(data.error);
            }
            
            this.replacementId = data.replacement_id;
            this.showNotification('success', 'Resilver started!');
            
            // Show progress section
            document.getElementById('resilver-progress').style.display = 'block';
            
            // Start monitoring
            this.startResilverMonitoring();
            
        } catch (error) {
            this.showNotification('error', `Replacement failed: ${error.message}`);
        }
    }
    
    startResilverMonitoring() {
        this.resilverCheckInterval = setInterval(() => this.checkResilverProgress(), 5000);
        this.checkResilverProgress(); // Initial check
    }
    
    async checkResilverProgress() {
        try {
            const response = await fetch(`/api/disk-replacement.php?action=resilver-progress&pool=${this.selectedPool}`);
            const data = await response.json();
            
            const progressDiv = document.getElementById('resilver-progress');
            
            if (data.is_resilvering) {
                progressDiv.innerHTML = `
                    <div class="card">
                        <h4>🔄 Resilvering in Progress</h4>
                        <div style="margin: 20px 0;">
                            <div style="display: flex; justify-content: space-between; margin-bottom: 8px;">
                                <span>${data.percent.toFixed(2)}% complete</span>
                                <span>${data.time_remaining} remaining</span>
                            </div>
                            <div class="progress" style="height: 20px;">
                                <div class="progress-bar" style="width: ${data.percent}%"></div>
                            </div>
                        </div>
                        <div style="display: grid; grid-template-columns: repeat(3, 1fr); gap: 16px; margin-top: 16px;">
                            <div>
                                <p style="color: #94a3b8; font-size: 12px;">Scanned</p>
                                <p style="font-size: 18px; font-weight: 600;">${data.scanned}</p>
                            </div>
                            <div>
                                <p style="color: #94a3b8; font-size: 12px;">To Scan</p>
                                <p style="font-size: 18px; font-weight: 600;">${data.to_scan}</p>
                            </div>
                            <div>
                                <p style="color: #94a3b8; font-size: 12px;">Speed</p>
                                <p style="font-size: 18px; font-weight: 600;">${data.speed}/s</p>
                            </div>
                        </div>
                    </div>
                `;
            } else if (data.percent === 100) {
                clearInterval(this.resilverCheckInterval);
                this.showNotification('success', 'Resilver completed!');
                this.nextStep();
            }
            
        } catch (error) {
            console.error('Failed to check resilver progress:', error);
        }
    }
    
    async loadStep5() {
        this.updateStepDisplay();
        clearInterval(this.resilverCheckInterval);
        
        // Check for auto-expand opportunity
        const expandCheck = await this.checkAutoExpand();
        
        const content = document.getElementById('wizard-content');
        content.innerHTML = `
            <div style="text-align: center; padding: 40px 20px;">
                <div style="font-size: 64px; margin-bottom: 24px;">✅</div>
                <h3 style="margin-bottom: 16px;">Disk Replacement Complete!</h3>
                <p style="color: #94a3b8; margin-bottom: 32px;">
                    The new disk has been successfully integrated into pool <strong>${this.selectedPool}</strong>.
                    The pool is now back to normal redundancy.
                </p>
                
                ${expandCheck.can_expand ? `
                    <div class="alert alert-info" style="text-align: left; margin-bottom: 24px;">
                        <h4>🎉 Pool Expansion Available!</h4>
                        <p>All disks in this pool are now the same size. You can expand the pool to use the additional capacity.</p>
                        <p><strong>Current Pool Size:</strong> ${expandCheck.current_size}</p>
                        ${!expandCheck.autoexpand_enabled ? `
                            <p style="color: #f59e0b;">⚠️ Auto-expand is currently disabled.</p>
                        ` : ''}
                        <button class="btn btn-primary" style="margin-top: 12px;" onclick="diskWizard.expandPool()">
                            Expand Pool Now
                        </button>
                    </div>
                ` : ''}
                
                <div class="card" style="text-align: left; max-width: 600px; margin: 0 auto 32px;">
                    <h4>Summary:</h4>
                    <table style="width: 100%; margin-top: 16px;">
                        <tr>
                            <td>Pool:</td>
                            <td><strong>${this.selectedPool}</strong></td>
                        </tr>
                        <tr>
                            <td>Removed:</td>
                            <td><code>${this.selectedFailedDisk}</code></td>
                        </tr>
                        <tr>
                            <td>Added:</td>
                            <td><code>${this.selectedNewDisk}</code></td>
                        </tr>
                        <tr>
                            <td>Status:</td>
                            <td><span class="badge badge-success">RESILVER COMPLETE</span></td>
                        </tr>
                    </table>
                </div>
                
                <button class="btn btn-primary" style="padding: 14px 32px; font-size: 16px;" 
                        onclick="diskWizard.finishWizard()">
                    Finish
                </button>
            </div>
        `;
        
        // Hide next button, show only finish
        document.getElementById('wizard-next').style.display = 'none';
    }
    
    async checkAutoExpand() {
        try {
            const response = await fetch(`/api/disk-replacement.php?action=complete`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
                body: new URLSearchParams({
                    replacement_id: this.replacementId
                })
            });
            
            const data = await response.json();
            return data.expand_info || { can_expand: false };
        } catch (error) {
            console.error('Failed to check auto-expand:', error);
            return { can_expand: false };
        }
    }
    
    async expandPool() {
        if (!confirm(`Expand pool ${this.selectedPool} to use all available disk space?`)) {
            return;
        }
        
        this.showNotification('info', 'Expanding pool...');
        
        try {
            const response = await fetch(`/api/disk-replacement.php?action=expand-pool`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
                body: new URLSearchParams({
                    pool: this.selectedPool
                })
            });
            
            const data = await response.json();
            
            if (data.success) {
                this.showNotification('success', `Pool expanded to ${data.new_size}!`);
                // Reload completion screen to show new size
                this.loadStep5();
            } else {
                this.showNotification('error', data.error || 'Expansion failed');
            }
        } catch (error) {
            this.showNotification('error', `Failed to expand pool: ${error.message}`);
        }
    }
    
    updateStepDisplay() {
        const stepsContainer = document.getElementById('wizard-steps');
        if (stepsContainer) {
            stepsContainer.innerHTML = this.renderSteps();
        }
        
        // Update navigation buttons
        const prevBtn = document.getElementById('wizard-prev');
        const nextBtn = document.getElementById('wizard-next');
        
        if (prevBtn) {
            prevBtn.style.display = this.currentStep > 1 && this.currentStep < 5 ? 'block' : 'none';
        }
        
        if (nextBtn) {
            nextBtn.disabled = true; // Will be enabled when step action completes
        }
    }
    
    nextStep() {
        if (this.currentStep < this.totalSteps) {
            this.currentStep++;
            this[`loadStep${this.currentStep}`]();
        }
    }
    
    prevStep() {
        if (this.currentStep > 1) {
            this.currentStep--;
            this[`loadStep${this.currentStep}`]();
        }
    }
    
    cancelWizard() {
        if (confirm('Cancel disk replacement wizard?')) {
            clearInterval(this.resilverCheckInterval);
            document.getElementById('disk-wizard-modal')?.remove();
            this.currentStep = 1;
        }
    }
    
    finishWizard() {
        clearInterval(this.resilverCheckInterval);
        document.getElementById('disk-wizard-modal')?.remove();
        this.currentStep = 1;
        this.checkPoolStatus(); // Refresh status
        this.showNotification('success', 'Disk replacement completed successfully!');
    }
    
    showNotification(type, message) {
        // Reuse from BackupManager or implement similar
        const notification = document.createElement('div');
        notification.className = `notification notification-${type}`;
        notification.textContent = message;
        notification.style.cssText = `
            position: fixed;
            top: 20px;
            right: 20px;
            padding: 16px 24px;
            border-radius: 8px;
            box-shadow: 0 4px 12px rgba(0,0,0,0.3);
            z-index: 10001;
        `;
        
        if (type === 'success') notification.style.background = '#10b981';
        else if (type === 'error') notification.style.background = '#f87171';
        else if (type === 'info') notification.style.background = '#667eea';
        
        notification.style.color = 'white';
        
        document.body.appendChild(notification);
        setTimeout(() => notification.remove(), 4000);
    }
}

// Initialize
let diskWizard;
document.addEventListener('DOMContentLoaded', () => {
    diskWizard = new DiskReplacementWizard();
});
