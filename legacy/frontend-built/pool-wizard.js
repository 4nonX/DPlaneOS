/**
 * D-PlaneOS v1.14.0 - ZFS Pool Creation Wizard
 * REAL functionality - Executes actual zpool create commands
 */

class PoolWizard {
    constructor() {
        this.currentStep = 1;
        this.totalSteps = 5;
        this.wizardData = {
            selectedDisks: [],
            raidType: 'raidz2',
            options: {
                ashift: 12,
                compression: 'lz4',
                atime: false,
                encryption: false
            },
            poolName: ''
        };
        this.availableDisks = [];
    }

    /**
     * Initialize wizard - Load available disks
     */
    async init() {
        loading.show('Loading available disks...');
        
        try {
            const response = await fetch('/api/hardware-monitor.php?action=scan');
            const data = await response.json();
            
            // Filter out disks already in use
            this.availableDisks = data.disks.filter(disk => !disk.inUse);
            
            loading.hide();
            
            if (this.availableDisks.length === 0) {
                toast.error('No available disks found. All disks are in use or no disks detected.');
                return false;
            }
            
            this.renderStep(1);
            return true;
            
        } catch (error) {
            loading.hide();
            toast.error('Failed to load disks: ' + error.message);
            return false;
        }
    }

    /**
     * Render wizard step
     */
    renderStep(step) {
        this.currentStep = step;
        const container = document.getElementById('wizardContent');
        
        switch (step) {
            case 1:
                container.innerHTML = this.renderDiskSelection();
                break;
            case 2:
                container.innerHTML = this.renderRaidType();
                break;
            case 3:
                container.innerHTML = this.renderOptions();
                break;
            case 4:
                container.innerHTML = this.renderReview();
                break;
            case 5:
                this.createPool();
                break;
        }
        
        this.updateProgressBar();
    }

    /**
     * Step 1: Disk Selection
     */
    renderDiskSelection() {
        return `
            <h2 style="margin-bottom: 24px;">Step 1: Select Disks</h2>
            <p style="color: var(--color-text-secondary); margin-bottom: 24px;">
                Select the disks you want to include in the new ZFS pool. 
                <strong>${this.availableDisks.length} disks available</strong>
            </p>
            
            <div class="disk-grid" style="display: grid; gap: 16px;">
                ${this.availableDisks.map((disk, index) => `
                    <div class="disk-card" data-disk-id="${disk.id}" 
                         style="border: 2px solid var(--color-border); padding: 20px; border-radius: 8px; cursor: pointer; transition: all 0.2s;"
                         onclick="wizard.toggleDisk(${index})">
                        <div style="display: flex; justify-content: space-between; align-items: start;">
                            <div style="flex: 1;">
                                <div style="font-weight: 600; font-size: 16px; margin-bottom: 8px;">
                                    ${disk.model}
                                </div>
                                <div style="color: var(--color-text-secondary); font-size: 14px; margin-bottom: 4px;">
                                    ${disk.device} • ${disk.sizeFormatted}
                                </div>
                                <div style="display: flex; gap: 8px; margin-top: 8px;">
                                    <span class="badge badge-info">SMART: ${disk.smart}</span>
                                    ${disk.temperature ? `<span class="badge badge-info">${disk.temperature}°C</span>` : ''}
                                </div>
                            </div>
                            <div>
                                <input type="checkbox" id="disk-${index}" style="width: 24px; height: 24px;">
                            </div>
                        </div>
                    </div>
                `).join('')}
            </div>
            
            <div style="margin-top: 32px; display: flex; justify-content: space-between;">
                <button class="btn-secondary" onclick="showView('pools-list')">Cancel</button>
                <button class="btn-primary" onclick="wizard.nextStep()" id="nextBtn">
                    Next: RAID Type →
                </button>
            </div>
        `;
    }

    /**
     * Toggle disk selection
     */
    toggleDisk(index) {
        const disk = this.availableDisks[index];
        const checkbox = document.getElementById(`disk-${index}`);
        const card = document.querySelector(`[data-disk-id="${disk.id}"]`);
        
        checkbox.checked = !checkbox.checked;
        
        if (checkbox.checked) {
            this.wizardData.selectedDisks.push(disk);
            card.style.borderColor = 'var(--color-primary)';
            card.style.background = 'rgba(124, 58, 237, 0.1)';
        } else {
            this.wizardData.selectedDisks = this.wizardData.selectedDisks.filter(d => d.id !== disk.id);
            card.style.borderColor = 'var(--color-border)';
            card.style.background = 'transparent';
        }
        
        console.log('Selected disks:', this.wizardData.selectedDisks.length);
    }

    /**
     * Step 2: RAID Type Selection
     */
    renderRaidType() {
        const diskCount = this.wizardData.selectedDisks.length;
        
        const raidTypes = [
            {
                value: 'stripe',
                name: 'Stripe (RAID 0)',
                description: 'No redundancy, maximum performance and capacity',
                minDisks: 1,
                efficiency: '100%',
                canLose: 0
            },
            {
                value: 'mirror',
                name: 'Mirror (RAID 1)',
                description: 'Full redundancy, 50% capacity',
                minDisks: 2,
                efficiency: '50%',
                canLose: diskCount - 1
            },
            {
                value: 'raidz1',
                name: 'RAID-Z1',
                description: 'Single parity, similar to RAID 5',
                minDisks: 3,
                efficiency: ((diskCount - 1) / diskCount * 100).toFixed(0) + '%',
                canLose: 1
            },
            {
                value: 'raidz2',
                name: 'RAID-Z2',
                description: 'Double parity, similar to RAID 6',
                minDisks: 4,
                efficiency: ((diskCount - 2) / diskCount * 100).toFixed(0) + '%',
                canLose: 2
            },
            {
                value: 'raidz3',
                name: 'RAID-Z3',
                description: 'Triple parity, maximum redundancy',
                minDisks: 5,
                efficiency: ((diskCount - 3) / diskCount * 100).toFixed(0) + '%',
                canLose: 3
            }
        ];
        
        return `
            <h2 style="margin-bottom: 24px;">Step 2: RAID Type</h2>
            <p style="color: var(--color-text-secondary); margin-bottom: 24px;">
                Selected ${diskCount} disks. Choose your RAID configuration.
            </p>
            
            <div style="display: grid; gap: 16px;">
                ${raidTypes.map(type => {
                    const available = diskCount >= type.minDisks;
                    return `
                        <div class="raid-card ${!available ? 'disabled' : ''}" 
                             style="border: 2px solid var(--color-border); padding: 20px; border-radius: 8px; 
                                    cursor: ${available ? 'pointer' : 'not-allowed'}; 
                                    opacity: ${available ? '1' : '0.5'}; transition: all 0.2s;"
                             onclick="${available ? `wizard.selectRaidType('${type.value}')` : ''}">
                            <div style="display: flex; justify-content: space-between; align-items: start;">
                                <div style="flex: 1;">
                                    <div style="font-weight: 600; font-size: 16px; margin-bottom: 8px;">
                                        ${type.name}
                                        ${!available ? `<span style="color: var(--color-error);">(Need ${type.minDisks}+ disks)</span>` : ''}
                                    </div>
                                    <div style="color: var(--color-text-secondary); margin-bottom: 12px;">
                                        ${type.description}
                                    </div>
                                    <div style="display: flex; gap: 16px; font-size: 14px;">
                                        <div><strong>Efficiency:</strong> ${type.efficiency}</div>
                                        <div><strong>Can lose:</strong> ${type.canLose} disk${type.canLose !== 1 ? 's' : ''}</div>
                                    </div>
                                </div>
                                <div>
                                    <input type="radio" name="raidType" value="${type.value}" 
                                           ${type.value === this.wizardData.raidType ? 'checked' : ''}
                                           ${!available ? 'disabled' : ''}
                                           style="width: 24px; height: 24px;">
                                </div>
                            </div>
                        </div>
                    `;
                }).join('')}
            </div>
            
            <div style="margin-top: 32px; display: flex; justify-content: space-between;">
                <button class="btn-secondary" onclick="wizard.prevStep()">← Back</button>
                <button class="btn-primary" onclick="wizard.nextStep()">Next: Options →</button>
            </div>
        `;
    }

    /**
     * Select RAID type
     */
    selectRaidType(type) {
        this.wizardData.raidType = type;
        
        // Update radio buttons
        document.querySelectorAll('.raid-card').forEach(card => {
            card.style.borderColor = 'var(--color-border)';
            card.style.background = 'transparent';
        });
        
        event.currentTarget.style.borderColor = 'var(--color-primary)';
        event.currentTarget.style.background = 'rgba(124, 58, 237, 0.1)';
        
        console.log('Selected RAID type:', type);
    }

    /**
     * Step 3: Pool Options
     */
    renderOptions() {
        return `
            <h2 style="margin-bottom: 24px;">Step 3: Pool Options</h2>
            
            <div style="max-width: 600px;">
                <div class="glass-card" style="margin-bottom: 24px; padding: 24px;">
                    <label style="display: block; margin-bottom: 8px; font-weight: 600;">Pool Name</label>
                    <input type="text" id="poolName" placeholder="tank" 
                           value="${this.wizardData.poolName}"
                           oninput="wizard.wizardData.poolName = this.value"
                           style="width: 100%; padding: 12px; background: var(--color-surface-light); 
                                  border: 1px solid var(--color-border); border-radius: 6px; color: var(--color-text-primary);">
                    <p style="color: var(--color-text-secondary); font-size: 13px; margin-top: 8px;">
                        Pool name (e.g., "tank", "storage", "backup")
                    </p>
                </div>
                
                <div class="glass-card" style="margin-bottom: 24px; padding: 24px;">
                    <label style="display: block; margin-bottom: 16px; font-weight: 600;">Compression</label>
                    <select id="compression" onchange="wizard.wizardData.options.compression = this.value"
                            style="width: 100%; padding: 12px; background: var(--color-surface-light); 
                                   border: 1px solid var(--color-border); border-radius: 6px; color: var(--color-text-primary);">
                        <option value="off">Off (no compression)</option>
                        <option value="lz4" selected>LZ4 (recommended - fast)</option>
                        <option value="gzip">GZIP (slower, better compression)</option>
                        <option value="zstd">ZSTD (best compression)</option>
                    </select>
                </div>
                
                <div class="glass-card" style="margin-bottom: 24px; padding: 24px;">
                    <label style="display: flex; align-items: center; cursor: pointer;">
                        <input type="checkbox" id="atimeOff" 
                               ${!this.wizardData.options.atime ? 'checked' : ''}
                               onchange="wizard.wizardData.options.atime = !this.checked"
                               style="width: 20px; height: 20px; margin-right: 12px;">
                        <div>
                            <div style="font-weight: 600; margin-bottom: 4px;">Disable Access Time (atime)</div>
                            <div style="color: var(--color-text-secondary); font-size: 13px;">
                                Improves performance by not updating access time on reads
                            </div>
                        </div>
                    </label>
                </div>
                
                <div class="glass-card" style="padding: 24px;">
                    <label style="display: flex; align-items: center; cursor: pointer;">
                        <input type="checkbox" id="encryption" 
                               ${this.wizardData.options.encryption ? 'checked' : ''}
                               onchange="wizard.wizardData.options.encryption = this.checked"
                               style="width: 20px; height: 20px; margin-right: 12px;">
                        <div>
                            <div style="font-weight: 600; margin-bottom: 4px;">Enable Encryption</div>
                            <div style="color: var(--color-text-secondary); font-size: 13px;">
                                Encrypt pool data at rest (you'll need to enter a passphrase)
                            </div>
                        </div>
                    </label>
                </div>
            </div>
            
            <div style="margin-top: 32px; display: flex; justify-content: space-between;">
                <button class="btn-secondary" onclick="wizard.prevStep()">← Back</button>
                <button class="btn-primary" onclick="wizard.nextStep()">Next: Review →</button>
            </div>
        `;
    }

    /**
     * Step 4: Review & Create
     */
    renderReview() {
        const totalSize = this.wizardData.selectedDisks.reduce((sum, disk) => sum + disk.size, 0);
        const totalSizeFormatted = this.formatBytes(totalSize);
        
        let usableSize;
        switch (this.wizardData.raidType) {
            case 'stripe':
                usableSize = totalSize;
                break;
            case 'mirror':
                usableSize = totalSize / this.wizardData.selectedDisks.length;
                break;
            case 'raidz1':
                usableSize = totalSize * (this.wizardData.selectedDisks.length - 1) / this.wizardData.selectedDisks.length;
                break;
            case 'raidz2':
                usableSize = totalSize * (this.wizardData.selectedDisks.length - 2) / this.wizardData.selectedDisks.length;
                break;
            case 'raidz3':
                usableSize = totalSize * (this.wizardData.selectedDisks.length - 3) / this.wizardData.selectedDisks.length;
                break;
        }
        
        return `
            <h2 style="margin-bottom: 24px;">Step 4: Review Configuration</h2>
            
            <div class="glass-card" style="padding: 24px; margin-bottom: 24px;">
                <h3 style="margin-bottom: 16px;">Pool Configuration</h3>
                <div style="display: grid; gap: 16px;">
                    <div style="display: flex; justify-content: space-between;">
                        <span style="color: var(--color-text-secondary);">Pool Name:</span>
                        <strong>${this.wizardData.poolName || '<span style="color: var(--color-error);">Not set!</span>'}</strong>
                    </div>
                    <div style="display: flex; justify-content: space-between;">
                        <span style="color: var(--color-text-secondary);">RAID Type:</span>
                        <strong>${this.wizardData.raidType.toUpperCase()}</strong>
                    </div>
                    <div style="display: flex; justify-content: space-between;">
                        <span style="color: var(--color-text-secondary);">Disks:</span>
                        <strong>${this.wizardData.selectedDisks.length}</strong>
                    </div>
                    <div style="display: flex; justify-content: space-between;">
                        <span style="color: var(--color-text-secondary);">Raw Capacity:</span>
                        <strong>${totalSizeFormatted}</strong>
                    </div>
                    <div style="display: flex; justify-content: space-between;">
                        <span style="color: var(--color-text-secondary);">Usable Capacity:</span>
                        <strong style="color: var(--color-primary);">${this.formatBytes(usableSize)}</strong>
                    </div>
                </div>
            </div>
            
            <div class="glass-card" style="padding: 24px; margin-bottom: 24px;">
                <h3 style="margin-bottom: 16px;">Selected Disks</h3>
                ${this.wizardData.selectedDisks.map(disk => `
                    <div style="display: flex; justify-content: space-between; padding: 12px 0; border-bottom: 1px solid var(--color-border);">
                        <span>${disk.model}</span>
                        <span style="color: var(--color-text-secondary);">${disk.sizeFormatted}</span>
                    </div>
                `).join('')}
            </div>
            
            <div class="glass-card" style="padding: 24px;">
                <h3 style="margin-bottom: 16px;">Options</h3>
                <div style="display: grid; gap: 12px;">
                    <div style="display: flex; justify-content: space-between;">
                        <span style="color: var(--color-text-secondary);">Compression:</span>
                        <strong>${this.wizardData.options.compression.toUpperCase()}</strong>
                    </div>
                    <div style="display: flex; justify-content: space-between;">
                        <span style="color: var(--color-text-secondary);">Access Time:</span>
                        <strong>${this.wizardData.options.atime ? 'Enabled' : 'Disabled'}</strong>
                    </div>
                    <div style="display: flex; justify-content: space-between;">
                        <span style="color: var(--color-text-secondary);">Encryption:</span>
                        <strong>${this.wizardData.options.encryption ? 'Enabled' : 'Disabled'}</strong>
                    </div>
                </div>
            </div>
            
            <div style="margin-top: 32px; display: flex; justify-content: space-between;">
                <button class="btn-secondary" onclick="wizard.prevStep()">← Back</button>
                <button class="btn-primary" onclick="wizard.createPool()" id="createBtn">
                    🚀 Create Pool
                </button>
            </div>
        `;
    }

    /**
     * Step 5: Execute pool creation - REAL COMMAND
     */
    async createPool() {
        // Validation
        if (!this.wizardData.poolName) {
            toast.error('Please enter a pool name');
            this.renderStep(3);
            return;
        }
        
        if (this.wizardData.selectedDisks.length === 0) {
            toast.error('No disks selected');
            this.renderStep(1);
            return;
        }
        
        // Show creating overlay
        loading.show(`Creating ZFS pool "${this.wizardData.poolName}"...`);
        
        // Prepare data for API
        const poolData = {
            name: this.wizardData.poolName,
            type: this.wizardData.raidType,
            disks: this.wizardData.selectedDisks.map(d => d.device),
            options: this.wizardData.options
        };
        
        console.log('Creating pool with data:', poolData);
        
        try {
            // CALL REAL ZFS API
            const response = await fetch('/api/zfs.php?action=create_pool', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(poolData)
            });
            
            const result = await response.json();
            
            loading.hide();
            
            if (result.success) {
                // SUCCESS!
                toast.success(`✅ Pool "${this.wizardData.poolName}" created successfully!`, 8000);
                
                // Show success screen
                document.getElementById('wizardContent').innerHTML = `
                    <div style="text-align: center; padding: 60px 20px;">
                        <div style="font-size: 80px; margin-bottom: 24px;">✅</div>
                        <h2 style="font-size: 32px; margin-bottom: 16px;">Pool Created!</h2>
                        <p style="color: var(--color-text-secondary); font-size: 18px; margin-bottom: 32px;">
                            Your ZFS pool "${this.wizardData.poolName}" has been created successfully.
                        </p>
                        <button class="btn-primary" onclick="showView('pools-list')" style="padding: 16px 32px; font-size: 16px;">
                            View Pool List
                        </button>
                    </div>
                `;
                
                // Redirect to pool list after 3 seconds
                setTimeout(() => {
                    showView('pools-list');
                    if (typeof loadPools === 'function') {
                        loadPools();
                    }
                }, 3000);
                
            } else {
                // ERROR
                toast.error(`Failed to create pool: ${result.error}`, 15000);
                
                // Show error screen with retry option
                document.getElementById('wizardContent').innerHTML = `
                    <div style="text-align: center; padding: 60px 20px;">
                        <div style="font-size: 80px; margin-bottom: 24px;">❌</div>
                        <h2 style="font-size: 32px; margin-bottom: 16px;">Creation Failed</h2>
                        <p style="color: var(--color-error); font-size: 16px; margin-bottom: 8px;">
                            ${result.error}
                        </p>
                        <p style="color: var(--color-text-secondary); font-size: 14px; margin-bottom: 32px;">
                            Check that all disks are accessible and not in use.
                        </p>
                        <div style="display: flex; gap: 16px; justify-content: center;">
                            <button class="btn-secondary" onclick="wizard.renderStep(1)">
                                Start Over
                            </button>
                            <button class="btn-primary" onclick="wizard.createPool()">
                                Retry Creation
                            </button>
                        </div>
                    </div>
                `;
            }
            
        } catch (error) {
            loading.hide();
            toast.error('Network error: ' + error.message);
            console.error('Pool creation error:', error);
        }
    }

    /**
     * Navigation helpers
     */
    nextStep() {
        // Validation
        if (this.currentStep === 1 && this.wizardData.selectedDisks.length === 0) {
            toast.error('Please select at least one disk');
            return;
        }
        
        if (this.currentStep === 3 && !this.wizardData.poolName) {
            toast.error('Please enter a pool name');
            document.getElementById('poolName').focus();
            return;
        }
        
        if (this.currentStep < this.totalSteps) {
            this.renderStep(this.currentStep + 1);
        }
    }

    prevStep() {
        if (this.currentStep > 1) {
            this.renderStep(this.currentStep - 1);
        }
    }

    updateProgressBar() {
        const progress = (this.currentStep / this.totalSteps) * 100;
        const progressBar = document.querySelector('.wizard-progress-bar');
        if (progressBar) {
            progressBar.style.width = progress + '%';
        }
    }

    formatBytes(bytes) {
        if (bytes === 0) return '0 B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
    }
}

// Global instance
const wizard = new PoolWizard();

// Initialize wizard when view loads
document.addEventListener('DOMContentLoaded', function() {
    // Auto-init wizard if on wizard view
    const wizardView = document.getElementById('view-wizard-pool');
    if (wizardView && wizardView.classList.contains('active')) {
        wizard.init();
    }
});
