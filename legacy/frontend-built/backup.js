/**
 * D-PlaneOS Config Backup Management
 * Frontend JavaScript - v1.13.0
 */

class BackupManager {
    constructor() {
        this.currentBackups = [];
        this.init();
    }
    
    init() {
        this.loadBackupList();
        this.setupEventListeners();
    }
    
    setupEventListeners() {
        document.getElementById('create-backup-btn')?.addEventListener('click', () => this.createBackup());
        document.getElementById('restore-backup-btn')?.addEventListener('click', () => this.restoreBackup());
        document.getElementById('schedule-backup-btn')?.addEventListener('click', () => this.scheduleBackup());
    }
    
    async createBackup() {
        this.showSpinner('Creating encrypted backup...');
        
        try {
            const response = await fetch('/api/backup.php?action=create', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' }
            });
            
            const data = await response.json();
            
            if (!response.ok) {
                throw new Error(data.error || 'Backup creation failed');
            }
            
            // Show password modal
            this.showPasswordModal(data.password, data.file, data.checksum);
            
            // Reload backup list
            await this.loadBackupList();
            
            this.showNotification('success', `Backup created: ${data.file} (${data.size_mb} MB)`);
            
        } catch (error) {
            this.showNotification('error', `Backup failed: ${error.message}`);
        } finally {
            this.hideSpinner();
        }
    }
    
    showPasswordModal(password, filename, checksum) {
        const modal = document.createElement('div');
        modal.className = 'modal active';
        modal.innerHTML = `
            <div class="modal-content">
                <div class="modal-header">
                    <h3>⚠️ Backup Password - SAVE THIS!</h3>
                    <button class="modal-close" onclick="this.closest('.modal').remove()">&times;</button>
                </div>
                <div class="modal-body">
                    <div class="alert alert-warning">
                        <strong>CRITICAL:</strong> This password is required to decrypt your backup.
                        <strong>Save it in a secure location NOW!</strong>
                    </div>
                    
                    <div style="background: #0f3460; padding: 20px; border-radius: 8px; margin: 20px 0;">
                        <label>Backup Password:</label>
                        <div style="display: flex; gap: 12px; margin-top: 8px;">
                            <input type="text" id="backup-password" value="${password}" readonly 
                                   style="flex: 1; font-family: monospace; font-size: 18px; font-weight: bold;">
                            <button class="btn btn-primary" onclick="navigator.clipboard.writeText('${password}'); 
                                this.textContent='✓ Copied!'; setTimeout(() => this.textContent='📋 Copy', 2000)">
                                📋 Copy
                            </button>
                        </div>
                    </div>
                    
                    <div style="font-size: 13px; color: #94a3b8; margin-top: 16px;">
                        <strong>File:</strong> ${filename}<br>
                        <strong>SHA256:</strong> <code style="font-size: 11px">${checksum}</code>
                    </div>
                    
                    <div style="margin-top: 24px; display: flex; justify-content: space-between;">
                        <button class="btn btn-secondary" onclick="this.closest('.modal').remove()">
                            I've Saved It
                        </button>
                        <button class="btn btn-primary" onclick="backupManager.downloadBackup('${filename}')">
                            📥 Download Backup
                        </button>
                    </div>
                </div>
            </div>
        `;
        document.body.appendChild(modal);
    }
    
    async loadBackupList() {
        try {
            const response = await fetch('/api/backup.php?action=list');
            const data = await response.json();
            
            this.currentBackups = data.backups;
            this.renderBackupTable();
            
        } catch (error) {
            console.error('Failed to load backups:', error);
        }
    }
    
    renderBackupTable() {
        const tbody = document.getElementById('backup-table-body');
        if (!tbody) return;
        
        if (this.currentBackups.length === 0) {
            tbody.innerHTML = `
                <tr>
                    <td colspan="5" style="text-align: center; padding: 40px; color: #94a3b8;">
                        No backups found. Create your first backup above.
                    </td>
                </tr>
            `;
            return;
        }
        
        tbody.innerHTML = this.currentBackups.map(backup => `
            <tr>
                <td>
                    <div style="font-weight: 600;">${backup.filename}</div>
                    <div style="font-size: 12px; color: #94a3b8; margin-top: 4px;">
                        ${backup.file_exists ? '✓ File exists' : '⚠️ File missing'}
                    </div>
                </td>
                <td>${backup.size_mb} MB</td>
                <td>${this.formatDate(backup.created_at)}</td>
                <td>
                    ${backup.metadata?.version || 'N/A'}
                    ${backup.metadata?.installed_apps?.length > 0 ? 
                        `<span class="badge badge-info">${backup.metadata.installed_apps.length} apps</span>` : ''}
                </td>
                <td>
                    <div style="display: flex; gap: 8px;">
                        <button class="btn btn-secondary btn-sm" 
                                onclick="backupManager.downloadBackup('${backup.filename}')">
                            📥 Download
                        </button>
                        <button class="btn btn-danger btn-sm" 
                                onclick="backupManager.deleteBackup('${backup.filename}')">
                            🗑️ Delete
                        </button>
                    </div>
                </td>
            </tr>
        `).join('');
    }
    
    downloadBackup(filename) {
        window.location.href = `/api/backup.php?action=download&file=${encodeURIComponent(filename)}`;
    }
    
    async deleteBackup(filename) {
        if (!confirm(`Delete backup ${filename}?\n\nThis action cannot be undone.`)) {
            return;
        }
        
        try {
            const response = await fetch(`/api/backup.php?action=delete&file=${encodeURIComponent(filename)}`, {
                method: 'POST'
            });
            
            if (!response.ok) {
                throw new Error('Delete failed');
            }
            
            await this.loadBackupList();
            this.showNotification('success', 'Backup deleted');
            
        } catch (error) {
            this.showNotification('error', `Delete failed: ${error.message}`);
        }
    }
    
    async restoreBackup() {
        const fileInput = document.getElementById('restore-file');
        const passwordInput = document.getElementById('restore-password');
        
        if (!fileInput.files[0]) {
            this.showNotification('error', 'Please select a backup file');
            return;
        }
        
        if (!passwordInput.value) {
            this.showNotification('error', 'Please enter the backup password');
            return;
        }
        
        if (!confirm('⚠️ WARNING: Restore will overwrite current configuration!\n\nCurrent config will be backed up first.\n\nContinue?')) {
            return;
        }
        
        this.showSpinner('Restoring configuration...');
        
        try {
            const formData = new FormData();
            formData.append('backup_file', fileInput.files[0]);
            formData.append('password', passwordInput.value);
            formData.append('action', 'restore');
            
            const response = await fetch('/api/backup.php', {
                method: 'POST',
                body: formData
            });
            
            const data = await response.json();
            
            if (!response.ok) {
                throw new Error(data.error || 'Restore failed');
            }
            
            this.showNotification('success', 'Configuration restored successfully!');
            
            // Show restart modal
            this.showRestartModal(data);
            
        } catch (error) {
            this.showNotification('error', `Restore failed: ${error.message}`);
        } finally {
            this.hideSpinner();
        }
    }
    
    showRestartModal(data) {
        const modal = document.createElement('div');
        modal.className = 'modal active';
        modal.innerHTML = `
            <div class="modal-content">
                <div class="modal-header">
                    <h3>✅ Configuration Restored</h3>
                </div>
                <div class="modal-body">
                    <div class="alert alert-success">
                        Configuration has been restored from:<br>
                        <strong>${data.metadata?.hostname || 'backup'}</strong> 
                        (${data.metadata?.version || 'unknown version'})
                    </div>
                    
                    <p>Services will restart automatically in 5 seconds...</p>
                    
                    <div style="margin-top: 20px;">
                        <strong>Pre-restore backup saved to:</strong><br>
                        <code style="font-size: 12px;">${data.pre_restore_backup}</code>
                    </div>
                    
                    <button class="btn btn-primary" style="width: 100%; margin-top: 20px;" 
                            onclick="location.reload()">
                        Reload Now
                    </button>
                </div>
            </div>
        `;
        document.body.appendChild(modal);
        
        setTimeout(() => location.reload(), 5000);
    }
    
    async scheduleBackup() {
        const frequency = document.getElementById('backup-frequency').value;
        const time = document.getElementById('backup-time').value;
        const keepDays = document.getElementById('backup-keep-days').value;
        
        try {
            const response = await fetch('/api/backup.php?action=schedule', {
                method: 'POST',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
                body: new URLSearchParams({
                    frequency,
                    time,
                    keep_days: keepDays
                })
            });
            
            if (!response.ok) {
                throw new Error('Schedule failed');
            }
            
            this.showNotification('success', `Automatic backups scheduled (${frequency})`);
            
        } catch (error) {
            this.showNotification('error', `Schedule failed: ${error.message}`);
        }
    }
    
    formatDate(dateStr) {
        const date = new Date(dateStr);
        const now = new Date();
        const diffMs = now - date;
        const diffMins = Math.floor(diffMs / 60000);
        const diffHours = Math.floor(diffMs / 3600000);
        const diffDays = Math.floor(diffMs / 86400000);
        
        if (diffMins < 1) return 'Just now';
        if (diffMins < 60) return `${diffMins} minutes ago`;
        if (diffHours < 24) return `${diffHours} hours ago`;
        if (diffDays < 7) return `${diffDays} days ago`;
        
        return date.toLocaleDateString() + ' ' + date.toLocaleTimeString();
    }
    
    showSpinner(message) {
        const spinner = document.getElementById('global-spinner') || this.createSpinner();
        spinner.querySelector('.spinner-message').textContent = message;
        spinner.classList.add('active');
    }
    
    hideSpinner() {
        const spinner = document.getElementById('global-spinner');
        if (spinner) spinner.classList.remove('active');
    }
    
    createSpinner() {
        const spinner = document.createElement('div');
        spinner.id = 'global-spinner';
        spinner.className = 'global-spinner';
        spinner.innerHTML = `
            <div class="spinner-content">
                <div class="spinner-icon"></div>
                <div class="spinner-message"></div>
            </div>
        `;
        document.body.appendChild(spinner);
        return spinner;
    }
    
    showNotification(type, message) {
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
            z-index: 10000;
            animation: slideIn 0.3s ease;
        `;
        
        if (type === 'success') {
            notification.style.background = '#10b981';
            notification.style.color = 'white';
        } else if (type === 'error') {
            notification.style.background = '#f87171';
            notification.style.color = 'white';
        }
        
        document.body.appendChild(notification);
        
        setTimeout(() => {
            notification.style.animation = 'slideOut 0.3s ease';
            setTimeout(() => notification.remove(), 300);
        }, 4000);
    }
}

// Initialize on DOM ready
let backupManager;
document.addEventListener('DOMContentLoaded', () => {
    backupManager = new BackupManager();
});
