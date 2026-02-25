/**
 * D-PlaneOS v1.14.0 - Main JavaScript
 * Complete UI Controller and API Integration
 */

// ==========================================
// VIEW MANAGEMENT
// ==========================================

function showView(viewId) {
    // Hide all views
    document.querySelectorAll('.view').forEach(v => v.classList.remove('active'));
    
    // Show selected view
    const view = document.getElementById('view-' + viewId);
    if (view) {
        view.classList.add('active');
        
        // Update active navigation
        document.querySelectorAll('.nav-item').forEach(i => i.classList.remove('active'));
        
        // Scroll to top
        window.scrollTo(0, 0);
        
        // Show loading for data-heavy views
        const dataViews = ['containers', 'containers-all', 'pools-list', 'appstore'];
        if (dataViews.includes(viewId)) {
            loading.show('Loading ' + viewId.replace('-', ' ') + '...');
            setTimeout(() => loading.hide(), 800); // Simulate loading
        }
        
        // Load data for view
        loadViewData(viewId);
    } else {
        console.error('View not found:', viewId);
        toast.error('Page not found: ' + viewId);
    }
}

function loadViewData(viewId) {
    switch(viewId) {
        case 'dashboard':
            loadDashboard();
            break;
        case 'containers':
        case 'containers-all':
        case 'containers-list':
            loadContainers();
            break;
        case 'images':
        case 'images-list':
            loadImages();
            break;
        case 'networks':
        case 'networks-list':
            loadNetworks();
            break;
        case 'volumes':
        case 'volumes-list':
            loadVolumes();
            break;
        case 'deploy-compose':
            showComposeDeployer();
            break;
        case 'appstore':
            loadAppStore();
            break;
        case 'pools':
        case 'pools-list':
            loadPools();
            break;
        case 'datasets':
        case 'datasets-list':
            loadDatasets();
            break;
        case 'snapshots':
        case 'snapshots-all':
        case 'snapshots-list':
            loadSnapshots();
            break;
        case 'nfs':
        case 'nfs-list':
            loadNFSShares();
            break;
        case 'smb':
        case 'smb-list':
            loadSMBShares();
            break;
        case 'users':
        case 'users-list':
            loadUsers();
            break;
        case 'groups':
        case 'groups-list':
            loadGroups();
            break;
        case 'backup':
        case 'backup-list':
            loadBackups();
            break;
        case 'services':
        case 'services-list':
            loadServices();
            break;
        case 'settings-system':
            loadSystemInfo();
            break;
        case 'wizard':
        case 'wizard-pool':
            wizard.init();
            break;
    }
}

// ==========================================
// DASHBOARD FUNCTIONS
// ==========================================

let dashboardInterval;

async function loadDashboard() {
    // Stop any existing interval
    if (dashboardInterval) {
        clearInterval(dashboardInterval);
    }
    
    // Load initial data
    await refreshDashboard();
    
    // Auto-refresh every 3 seconds
    dashboardInterval = setInterval(refreshDashboard, 3000);
}

async function refreshDashboard() {
    try {
        // Show loading for first load
        if (!document.getElementById('cpuMetric').textContent || document.getElementById('cpuMetric').textContent === '0%') {
            loading.show('Loading dashboard...');
        }
        
        const response = await fetch('/api/system.php?action=stats');
        const data = await response.json();
        
        // Update metrics
        document.getElementById('cpuMetric').textContent = data.cpu + '%';
        document.getElementById('memMetric').textContent = data.memory + '%';
        document.getElementById('diskMetric').textContent = data.disk + '%';
        document.getElementById('netMetric').textContent = formatBytes(data.network.in) + '/s';
        
        // Update dashboard panels
        loadDashboardPools();
        loadDashboardContainers();
        
        // Hide loading
        loading.hide();
        
    } catch (error) {
        console.error('Failed to load dashboard:', error);
        loading.hide();
        toast.error('Failed to load dashboard data');
    }
}

async function loadDashboardPools() {
    try {
        const response = await fetch('/api/zfs.php?action=list_pools');
        const data = await response.json();
        
        const container = document.getElementById('dashboardPools');
        if (!container) return;
        
        if (!data.pools || data.pools.length === 0) {
            container.innerHTML = '<p style="color:var(--color-text-secondary);padding:20px;text-align:center">No pools created yet</p>';
            return;
        }
        
        container.innerHTML = data.pools.slice(0, 3).map(pool => `
            <div style="padding:12px;border-bottom:1px solid var(--color-border)">
                <div style="display:flex;justify-content:space-between;align-items:center">
                    <div>
                        <div style="font-weight:600">${pool.name}</div>
                        <div style="font-size:13px;color:var(--color-text-secondary)">${pool.used} / ${pool.capacity}</div>
                    </div>
                    <span class="badge ${pool.health === 'ONLINE' ? 'badge-success' : 'badge-warning'}">${pool.health}</span>
                </div>
            </div>
        `).join('');
        
    } catch (error) {
        console.error('Failed to load pools:', error);
    }
}

async function loadDashboardContainers() {
    try {
        const response = await fetch('/api/docker.php?action=list');
        const data = await response.json();
        
        const container = document.getElementById('dashboardContainers');
        if (!container) return;
        
        if (!data.containers || data.containers.length === 0) {
            container.innerHTML = '<p style="color:var(--color-text-secondary);padding:20px;text-align:center">No containers running</p>';
            return;
        }
        
        const running = data.containers.filter(c => c.state === 'running').slice(0, 3);
        
        container.innerHTML = running.map(c => `
            <div style="padding:12px;border-bottom:1px solid var(--color-border)">
                <div style="display:flex;justify-content:space-between;align-items:center">
                    <div>
                        <div style="font-weight:600">${c.name}</div>
                        <div style="font-size:13px;color:var(--color-text-secondary);font-family:monospace">${c.image}</div>
                    </div>
                    <span class="status-dot ${c.state === 'running' ? 'online' : 'offline'}"></span>
                </div>
            </div>
        `).join('');
        
    } catch (error) {
        console.error('Failed to load containers:', error);
    }
}

// ==========================================
// CONTAINERS FUNCTIONS
// ==========================================

async function loadContainers() {
    try {
        const response = await fetch('/api/docker.php?action=list');
        const data = await response.json();
        
        // Update containers list (will be built in Module 3)
        console.log('Containers loaded:', data);
        
    } catch (error) {
        console.error('Failed to load containers:', error);
    }
}

async function refreshContainers() {
    await loadContainers();
}

// ==========================================
// APP STORE FUNCTIONS
// ==========================================

async function loadAppStore() {
    // App grid is rendered statically in index.html — nothing to fetch here.
    // SSH key status is loaded when the repo modal opens.
}

async function refreshAppStore() {
    await loadAppStore();
}

function openRepoModal() {
    showModal('repoModal');
    checkSSHKeyStatus();
    loadRepos();
}

// ─── SSH Key Management ───────────────────────────────────
// Talks to docker.php actions: save_ssh_key / get_ssh_key / delete_ssh_key

async function checkSSHKeyStatus() {
    try {
        const res    = await fetch('/api/docker.php?action=get_ssh_key');
        const status = await res.json();
        const statusEl  = document.getElementById('sshKeyStatus');
        const textarea  = document.getElementById('sshKeyTextarea');
        const deleteBtn = document.getElementById('sshKeyDeleteBtn');
        if (!statusEl) return;

        if (status.exists && status.valid) {
            statusEl.innerHTML = '<span style="color:#10b981">✓ SSH key valid</span> — permissions: ' + status.permissions;
            if (textarea)  textarea.style.display  = 'none';
            if (deleteBtn) deleteBtn.style.display = 'inline-block';
        } else if (status.exists && !status.valid) {
            statusEl.innerHTML = '<span style="color:#ef4444">✗ Key issues: ' + (status.errors || []).join(', ') + '</span>';
            if (textarea)  textarea.style.display  = 'block';
            if (deleteBtn) deleteBtn.style.display = 'inline-block';
        } else {
            statusEl.innerHTML = '<span style="color:var(--color-text-secondary)">No SSH key stored</span>';
            if (textarea)  textarea.style.display  = 'block';
            if (deleteBtn) deleteBtn.style.display = 'none';
        }
    } catch (e) {
        console.error('SSH key status check failed:', e);
    }
}

async function saveSSHKey() {
    const textarea = document.getElementById('sshKeyTextarea');
    const key = textarea ? textarea.value.trim() : '';
    if (!key) { alert('Paste your SSH private key first.'); return; }

    try {
        const res = await fetch('/api/docker.php?action=save_ssh_key', {
            method:  'POST',
            headers: { 'Content-Type': 'application/json' },
            body:    JSON.stringify({ key: key })
        });
        const result = await res.json();
        if (result.success) {
            alert(result.message);
            if (textarea) textarea.value = '';
            checkSSHKeyStatus();
        } else {
            alert('Save failed: ' + result.error);
        }
    } catch (e) { alert('Error: ' + e.message); }
}

async function deleteSSHKey() {
    if (!confirm('Delete SSH key? Private repos will no longer be accessible.')) return;
    try {
        const res    = await fetch('/api/docker.php?action=delete_ssh_key', { method: 'POST' });
        const result = await res.json();
        if (result.success) { checkSSHKeyStatus(); }
        else { alert(result.error); }
    } catch (e) { alert('Error: ' + e.message); }
}

// ─── Repository Management ────────────────────────────────

async function addRepository() {
    const urlInput     = document.getElementById('repoUrlInput');
    const nameInput    = document.getElementById('repoNameInput');
    const privateCheck = document.getElementById('repoPrivateCheckbox');
    
    const url = urlInput ? urlInput.value.trim() : '';
    if (!url) { alert('Enter a repository URL.'); return; }

    // Client-side guard: warn early if private + no key
    if (privateCheck && privateCheck.checked) {
        try {
            const chk = await fetch('/api/docker.php?action=get_ssh_key');
            const st  = await chk.json();
            if (!st.exists || !st.valid) {
                alert('Private repos require a valid SSH key. Save your key first.');
                return;
            }
        } catch (e) { /* let server-side catch it */ }
    }

    try {
        const res = await fetch('/api/docker.php?action=save_repo', {
            method:  'POST',
            headers: { 'Content-Type': 'application/json' },
            body:    JSON.stringify({
                url:     url,
                name:    nameInput ? nameInput.value.trim() : '',
                private: privateCheck ? privateCheck.checked : false
            })
        });
        const result = await res.json();
        if (result.success) {
            alert('Repository added!');
            if (urlInput)     urlInput.value     = '';
            if (nameInput)    nameInput.value    = '';
            if (privateCheck) privateCheck.checked = false;
        } else {
            alert('Failed: ' + result.error);
        }
    } catch (e) { alert('Error: ' + e.message); }
}

async function loadRepos() {
    const el = document.getElementById('repoList');
    if (!el) return;
    try {
        const res  = await fetch('/api/docker.php?action=list_repos');
        const data = await res.json();
        const repos = data.repos || [];
        if (repos.length === 0) {
            el.innerHTML = '<p style="color:var(--color-text-secondary);font-size:13px;">No repositories added yet.</p>';
            return;
        }
        el.innerHTML = repos.map(r => `
            <div class="repo-card">
                <div class="repo-header">
                    <div style="flex:1">
                        <div class="repo-name">${r.name || r.url}</div>
                        <div class="repo-url">${r.url}</div>
                        <div class="repo-meta">📌 Branch: ${r.branch || 'main'} ${r.private ? '• 🔒 Private' : ''}</div>
                    </div>
                    <button class="btn btn-secondary btn-sm" onclick="deleteRepo('${r.url.replace(/'/g, "\\'")}')">🗑️</button>
                </div>
            </div>
        `).join('');
    } catch (e) { console.error('loadRepos failed:', e); }
}

async function deleteRepo(url) {
    if (!confirm('Remove this repository?')) return;
    try {
        const res = await fetch('/api/docker.php?action=delete_repo', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({ url: url })
        });
        const result = await res.json();
        if (result.success) { await loadRepos(); }
        else { alert('Failed: ' + result.error); }
    } catch (e) { alert('Error: ' + e.message); }
}

// ==========================================
// STORAGE FUNCTIONS
// ==========================================

async function loadPools() {
    loading.show('Loading ZFS pools...');
    
    try {
        const response = await fetch('/api/zfs.php?action=list_pools');
        const data = await response.json();
        
        const container = document.getElementById('poolsList');
        if (!container) return;
        
        if (!data.pools || data.pools.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <div class="empty-state-icon">💾</div>
                    <h3>No Pools Created</h3>
                    <p>Create your first ZFS pool to get started</p>
                    <button class="btn-primary" onclick="showView('wizard-pool')" style="margin-top: 16px;">
                        ✨ Create Pool
                    </button>
                </div>
            `;
            loading.hide();
            return;
        }
        
        container.innerHTML = data.pools.map(pool => {
            const healthColor = pool.health === 'ONLINE' ? 'success' : pool.health === 'DEGRADED' ? 'warning' : 'danger';
            const capacityPercent = parseInt(pool.capacity);
            
            return `
                <div class="glass-card" style="margin-bottom: 16px;">
                    <div style="display: flex; justify-content: space-between; align-items: start; margin-bottom: 16px;">
                        <div style="flex: 1;">
                            <h3 style="font-size: 20px; margin-bottom: 8px;">${pool.name}</h3>
                            <div style="display: flex; gap: 16px; color: var(--color-text-secondary); font-size: 14px;">
                                <span>💾 ${pool.size}</span>
                                <span>📊 ${pool.alloc} used</span>
                                <span>🆓 ${pool.free} free</span>
                            </div>
                        </div>
                        <span class="badge badge-${healthColor}">${pool.health}</span>
                    </div>
                    
                    <div class="progress" style="margin-bottom: 16px;">
                        <div class="progress-bar ${healthColor === 'success' ? 'success' : ''}" style="width: ${capacityPercent}%"></div>
                    </div>
                    
                    <div style="display: flex; gap: 8px; flex-wrap: wrap;">
                        <button class="btn-secondary btn-sm" onclick="viewPoolStatus('${pool.name}')">📊 Status</button>
                        <button class="btn-secondary btn-sm" onclick="scrubPool('${pool.name}')">🔍 Scrub</button>
                        <button class="btn-secondary btn-sm" onclick="scrubStatus('${pool.name}')">📋 Scrub Status</button>
                        <button class="btn-secondary btn-sm" onclick="exportPool('${pool.name}')">📤 Export</button>
                        <button class="btn-danger btn-sm" onclick="destroyPoolConfirm('${pool.name}')">🗑️ Destroy</button>
                    </div>
                </div>
            `;
        }).join('');
        
        loading.hide();
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load pools: ' + error.message);
    }
}

async function viewPoolStatus(poolName) {
    loading.show('Loading pool status...');
    
    try {
        const response = await fetch('/api/zfs.php?action=pool_status&pool=' + poolName);
        const data = await response.json();
        
        loading.hide();
        
        // Show in modal
        const modal = document.createElement('div');
        modal.className = 'modal active';
        modal.innerHTML = `
            <div class="modal-content" style="max-width: 800px;">
                <div class="modal-header">
                    <h3>Pool Status: ${poolName}</h3>
                    <button class="modal-close" onclick="this.closest('.modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <pre style="background: var(--color-surface-light); padding: 16px; border-radius: 8px; overflow-x: auto; font-family: monospace; font-size: 13px;">${data.status}</pre>
                </div>
            </div>
        `;
        document.body.appendChild(modal);
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load status: ' + error.message);
    }
}

async function scrubPool(poolName) {
    loading.show('Starting scrub...');
    
    try {
        const response = await fetch('/api/zfs.php?action=scrub', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({pool: poolName, action: 'start'})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success(`Scrub started on pool "${poolName}"`);
        } else {
            toast.error('Scrub failed: ' + data.error);
        }
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to start scrub: ' + error.message);
    }
}

async function exportPool(poolName) {
    if (!confirm(`Export pool "${poolName}"? This will unmount the pool.`)) return;
    
    loading.show('Exporting pool...');
    
    try {
        const response = await fetch('/api/zfs.php?action=export_pool', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({pool: poolName})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success(`Pool "${poolName}" exported successfully`);
            loadPools();
        } else {
            toast.error('Export failed: ' + data.error);
        }
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to export pool: ' + error.message);
    }
}

async function scrubStatus(poolName) {
    loading.show('Checking scrub status...');
    try {
        const response = await fetch('/api/zfs.php?action=scrub_status&pool=' + poolName);
        const data = await response.json();
        loading.hide();

        const modal = document.createElement('div');
        modal.className = 'modal active';
        modal.innerHTML = `
            <div class="modal-content" style="max-width: 600px;">
                <div class="modal-header">
                    <h3>🔍 Scrub Status: ${poolName}</h3>
                    <button class="modal-close" onclick="this.closest('.modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <pre style="background: var(--color-surface-light); padding: 16px; border-radius: 8px; overflow-x: auto; font-family: monospace; font-size: 13px; white-space: pre-wrap;">${data.status || data.error || 'No scrub status available'}</pre>
                </div>
            </div>
        `;
        document.body.appendChild(modal);
    } catch (error) {
        loading.hide();
        toast.error('Failed to check scrub status: ' + error.message);
    }
}

async function importPool() {
    const poolName = prompt('Enter the name of the exported pool to import:');
    if (!poolName || !poolName.trim()) return;

    loading.show('Importing pool...');
    try {
        const response = await fetch('/api/zfs.php?action=import_pool', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({pool: poolName.trim()})
        });
        const data = await response.json();
        loading.hide();

        if (data.success) {
            toast.success('Pool "' + poolName.trim() + '" imported successfully');
            loadPools();
        } else {
            toast.error('Import failed: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to import pool: ' + error.message);
    }
}

function destroyPoolConfirm(poolName) {
    const modal = document.createElement('div');
    modal.className = 'modal active';
    modal.innerHTML = `
        <div class="modal-content" style="max-width: 500px;">
            <div class="modal-header">
                <h3>⚠️ Destroy Pool</h3>
                <button class="modal-close" onclick="this.closest('.modal').remove()">×</button>
            </div>
            <div class="modal-body">
                <p style="margin-bottom: 16px; font-size: 16px;">
                    Are you sure you want to destroy pool <strong>"${poolName}"</strong>?
                </p>
                <p style="color: var(--color-error); margin-bottom: 24px;">
                    ⚠️ This will permanently delete all data! This action cannot be undone.
                </p>
                <div style="display: flex; gap: 12px;">
                    <button class="btn-secondary" onclick="this.closest('.modal').remove()">Cancel</button>
                    <button class="btn-danger" onclick="destroyPool('${poolName}'); this.closest('.modal').remove();">
                        Destroy Pool
                    </button>
                </div>
            </div>
        </div>
    `;
    document.body.appendChild(modal);
}

async function destroyPool(poolName) {
    loading.show('Destroying pool...');
    
    try {
        const response = await fetch('/api/zfs.php?action=destroy_pool', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({pool: poolName})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success(`Pool "${poolName}" destroyed`);
            loadPools();
        } else {
            toast.error('Failed to destroy pool: ' + data.error);
        }
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to destroy pool: ' + error.message);
    }
}

// ==========================================
// UTILITY FUNCTIONS
// ==========================================

function formatBytes(bytes) {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return Math.round(bytes / Math.pow(k, i) * 100) / 100 + ' ' + sizes[i];
}

function formatUptime(seconds) {
    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    
    if (days > 0) return `${days}d ${hours}h`;
    if (hours > 0) return `${hours}h ${minutes}m`;
    return `${minutes}m`;
}

// ==========================================
// INITIALIZATION
// ==========================================

document.addEventListener('DOMContentLoaded', function() {
    // Load dashboard on startup
    loadDashboard();
    
    console.log('D-PlaneOS v1.14.0 - TrueNAS Killer - Initialized');
    console.log('MODULE 1: Core Foundation - COMPLETE');
    console.log('All 50+ pages loaded and ready for Module 2');
});

// Clean up on page unload
window.addEventListener('beforeunload', function() {
    if (dashboardInterval) {
        clearInterval(dashboardInterval);
    }
});

// ==========================================
// DATASET MANAGEMENT
// ==========================================

async function loadDatasets(poolName = null) {
    loading.show('Loading datasets...');
    
    try {
        const url = poolName 
            ? `/api/zfs.php?action=list_datasets&pool=${poolName}`
            : '/api/zfs.php?action=list_datasets';
        
        const response = await fetch(url);
        const data = await response.json();
        
        const container = document.getElementById('datasetsList');
        if (!container) return;
        
        if (!data.datasets || data.datasets.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <div class="empty-state-icon">📁</div>
                    <h3>No Datasets</h3>
                    <p>Create datasets to organize your data</p>
                </div>
            `;
            loading.hide();
            return;
        }
        
        container.innerHTML = `
            <table class="data-table">
                <thead>
                    <tr>
                        <th>Name</th>
                        <th>Used</th>
                        <th>Available</th>
                        <th>Referenced</th>
                        <th>Mountpoint</th>
                        <th>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    ${data.datasets.map(ds => `
                        <tr>
                            <td style="font-weight: 600;">${ds.name}</td>
                            <td>${ds.used}</td>
                            <td>${ds.avail}</td>
                            <td>${ds.refer}</td>
                            <td style="font-family: monospace; font-size: 12px;">${ds.mountpoint}</td>
                            <td>
                                <button class="btn-secondary btn-sm" onclick="createSnapshot('${ds.name}')">📸</button>
                                <button class="btn-danger btn-sm" onclick="destroyDatasetConfirm('${ds.name}')">🗑️</button>
                            </td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>
        `;
        
        loading.hide();
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load datasets: ' + error.message);
    }
}

function showCreateDataset() {
    const modal = document.createElement('div');
    modal.className = 'modal active';
    modal.innerHTML = `
        <div class="modal-content">
            <div class="modal-header">
                <h3>Create Dataset</h3>
                <button class="modal-close" onclick="this.closest('.modal').remove()">×</button>
            </div>
            <div class="modal-body">
                <div style="display: grid; gap: 16px;">
                    <div>
                        <label>Dataset Name</label>
                        <input type="text" id="datasetName" placeholder="poolname/datasetname">
                    </div>
                    <div>
                        <label>Compression</label>
                        <select id="datasetCompression">
                            <option value="lz4">LZ4 (recommended)</option>
                            <option value="gzip">GZIP</option>
                            <option value="zstd">ZSTD</option>
                            <option value="off">Off</option>
                        </select>
                    </div>
                    <div>
                        <label>Quota (optional)</label>
                        <input type="text" id="datasetQuota" placeholder="e.g., 100G, 1T">
                    </div>
                    <button class="btn-primary" onclick="createDataset()">Create Dataset</button>
                </div>
            </div>
        </div>
    `;
    document.body.appendChild(modal);
}

async function createDataset() {
    const name = document.getElementById('datasetName').value;
    const compression = document.getElementById('datasetCompression').value;
    const quota = document.getElementById('datasetQuota').value;
    
    if (!name) {
        toast.error('Please enter dataset name');
        return;
    }
    
    loading.show('Creating dataset...');
    
    try {
        const response = await fetch('/api/zfs.php?action=create_dataset', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({
                name,
                compression,
                quota: quota || null
            })
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success(`Dataset "${name}" created`);
            document.querySelector('.modal.active').remove();
            loadDatasets();
        } else {
            toast.error('Failed to create dataset: ' + data.error);
        }
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to create dataset: ' + error.message);
    }
}

function destroyDatasetConfirm(name) {
    if (!confirm(`Destroy dataset "${name}"? This will delete all data!`)) return;
    destroyDataset(name);
}

async function destroyDataset(name) {
    loading.show('Destroying dataset...');
    
    try {
        const response = await fetch('/api/zfs.php?action=destroy_dataset', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({dataset: name})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success(`Dataset "${name}" destroyed`);
            loadDatasets();
        } else {
            toast.error('Failed to destroy dataset: ' + data.error);
        }
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to destroy dataset: ' + error.message);
    }
}

// ==========================================
// SNAPSHOT MANAGEMENT
// ==========================================

async function loadSnapshots(datasetName = null) {
    loading.show('Loading snapshots...');
    
    try {
        const url = datasetName
            ? `/api/zfs.php?action=list_snapshots&dataset=${datasetName}`
            : '/api/zfs.php?action=list_snapshots';
        
        const response = await fetch(url);
        const data = await response.json();
        
        const container = document.getElementById('snapshotsList');
        if (!container) return;
        
        if (!data.snapshots || data.snapshots.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <div class="empty-state-icon">📸</div>
                    <h3>No Snapshots</h3>
                    <p>Create snapshots to preserve point-in-time copies of your data</p>
                </div>
            `;
            loading.hide();
            return;
        }
        
        container.innerHTML = `
            <table class="data-table">
                <thead>
                    <tr>
                        <th>Snapshot</th>
                        <th>Used</th>
                        <th>Referenced</th>
                        <th>Created</th>
                        <th>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    ${data.snapshots.map(snap => `
                        <tr>
                            <td style="font-weight: 600; font-family: monospace; font-size: 13px;">${snap.name}</td>
                            <td>${snap.used}</td>
                            <td>${snap.refer}</td>
                            <td>${snap.creation}</td>
                            <td>
                                <button class="btn-secondary btn-sm" onclick="rollbackSnapshotConfirm('${snap.name}')">⏮️</button>
                                <button class="btn-secondary btn-sm" onclick="cloneSnapshotPrompt('${snap.name}')">🔄</button>
                                <button class="btn-danger btn-sm" onclick="destroySnapshotConfirm('${snap.name}')">🗑️</button>
                            </td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>
        `;
        
        loading.hide();
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load snapshots: ' + error.message);
    }
}

async function createSnapshot(datasetName) {
    const snapName = prompt(`Create snapshot of ${datasetName}\n\nSnapshot name:`, 'snap-' + Date.now());
    if (!snapName) return;
    
    loading.show('Creating snapshot...');
    
    try {
        const response = await fetch('/api/zfs.php?action=create_snapshot', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({
                dataset: datasetName,
                snapshot: snapName
            })
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success(`Snapshot created: ${datasetName}@${snapName}`);
            if (typeof loadSnapshots === 'function') loadSnapshots();
        } else {
            toast.error('Failed to create snapshot: ' + data.error);
        }
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to create snapshot: ' + error.message);
    }
}

function rollbackSnapshotConfirm(snapshot) {
    if (!confirm(`Rollback to snapshot "${snapshot}"?\n\nThis will discard all changes made after this snapshot!`)) return;
    rollbackSnapshot(snapshot);
}

async function rollbackSnapshot(snapshot) {
    loading.show('Rolling back...');
    
    try {
        const response = await fetch('/api/zfs.php?action=rollback_snapshot', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({snapshot})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Rolled back to snapshot');
            loadSnapshots();
        } else {
            toast.error('Rollback failed: ' + data.error);
        }
        
    } catch (error) {
        loading.hide();
        toast.error('Rollback failed: ' + error.message);
    }
}

function cloneSnapshotPrompt(snapshot) {
    const cloneName = prompt(`Clone snapshot to new dataset:\n\nClone name:`, snapshot.split('@')[0] + '-clone');
    if (!cloneName) return;
    cloneSnapshot(snapshot, cloneName);
}

async function cloneSnapshot(snapshot, cloneName) {
    loading.show('Cloning snapshot...');
    
    try {
        const response = await fetch('/api/zfs.php?action=clone_snapshot', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({snapshot, clone: cloneName})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Snapshot cloned successfully');
            loadDatasets();
        } else {
            toast.error('Clone failed: ' + data.error);
        }
        
    } catch (error) {
        loading.hide();
        toast.error('Clone failed: ' + error.message);
    }
}

function destroySnapshotConfirm(snapshot) {
    if (!confirm(`Destroy snapshot "${snapshot}"?`)) return;
    destroySnapshot(snapshot);
}

async function destroySnapshot(snapshot) {
    loading.show('Destroying snapshot...');
    
    try {
        const response = await fetch('/api/zfs.php?action=destroy_snapshot', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({snapshot})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Snapshot destroyed');
            loadSnapshots();
        } else {
            toast.error('Failed to destroy snapshot: ' + data.error);
        }
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to destroy snapshot: ' + error.message);
    }
}

// Helper refresh functions
async function refreshPools() {
    await loadPools();
}

async function refreshDatasets() {
    await loadDatasets();
}

async function refreshSnapshots() {
    await loadSnapshots();
}

// ==========================================
// DOCKER/CONTAINER MANAGEMENT
// ==========================================

async function loadContainers() {
    loading.show('Loading containers...');
    
    try {
        const all = window.location.hash === '#all' || document.getElementById('view-containers-all')?.classList.contains('active');
        const response = await fetch(`/api/docker.php?action=list${all ? '&all=true' : ''}`);
        const data = await response.json();
        
        const container = document.getElementById('containersList') || document.getElementById('containersAllList');
        if (!container) return;
        
        if (!data.containers || data.containers.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <div class="empty-state-icon">🐳</div>
                    <h3>No Containers</h3>
                    <p>Deploy containers to get started</p>
                    <button class="btn-primary" onclick="showView('deploy-compose')" style="margin-top: 16px;">
                        Deploy Container
                    </button>
                </div>
            `;
            loading.hide();
            return;
        }
        
        container.innerHTML = data.containers.map(c => {
            const stateColor = c.state === 'running' ? 'success' : 'danger';
            return `
                <div class="container-item">
                    <div class="container-info">
                        <div class="container-name">
                            <span class="status-dot ${c.state === 'running' ? 'online' : 'offline'}"></span>
                            ${c.name}
                        </div>
                        <div class="container-image">${c.image}</div>
                        <div style="font-size: 12px; color: var(--color-text-secondary); margin-top: 4px;">${c.status}</div>
                    </div>
                    <div class="container-actions">
                        ${c.state === 'running' 
                            ? `<button class="btn-secondary btn-sm" onclick="stopContainer('${c.id}')">⏸️ Stop</button>`
                            : `<button class="btn-primary btn-sm" onclick="startContainer('${c.id}')">▶️ Start</button>`
                        }
                        <button class="btn-secondary btn-sm" onclick="restartContainer('${c.id}')">🔄 Restart</button>
                        <button class="btn-secondary btn-sm" onclick="viewLogs('${c.id}', '${c.name}')">📋 Logs</button>
                        <button class="btn-danger btn-sm" onclick="removeContainerConfirm('${c.id}', '${c.name}')">🗑️</button>
                    </div>
                </div>
            `;
        }).join('');
        
        loading.hide();
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load containers: ' + error.message);
    }
}

async function startContainer(id) {
    loading.show('Starting container...');
    
    try {
        const response = await fetch('/api/docker.php?action=start', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({id})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Container started');
            loadContainers();
        } else {
            toast.error('Failed to start: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to start container: ' + error.message);
    }
}

async function stopContainer(id) {
    loading.show('Stopping container...');
    
    try {
        const response = await fetch('/api/docker.php?action=stop', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({id})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Container stopped');
            loadContainers();
        } else {
            toast.error('Failed to stop: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to stop container: ' + error.message);
    }
}

async function restartContainer(id) {
    loading.show('Restarting container...');
    
    try {
        const response = await fetch('/api/docker.php?action=restart', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({id})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Container restarted');
            loadContainers();
        } else {
            toast.error('Failed to restart: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to restart container: ' + error.message);
    }
}

function removeContainerConfirm(id, name) {
    if (!confirm(`Remove container "${name}"?`)) return;
    removeContainer(id);
}

async function removeContainer(id) {
    loading.show('Removing container...');
    
    try {
        const response = await fetch('/api/docker.php?action=remove', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({id, force: true})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Container removed');
            loadContainers();
        } else {
            toast.error('Failed to remove: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to remove container: ' + error.message);
    }
}

async function viewLogs(id, name) {
    loading.show('Loading logs...');
    
    try {
        const response = await fetch(`/api/docker.php?action=logs&id=${id}&lines=200`);
        const data = await response.json();
        
        loading.hide();
        
        const modal = document.createElement('div');
        modal.className = 'modal active';
        modal.innerHTML = `
            <div class="modal-content" style="max-width: 1000px; max-height: 80vh;">
                <div class="modal-header">
                    <h3>📋 Logs: ${name}</h3>
                    <button class="modal-close" onclick="this.closest('.modal').remove()">×</button>
                </div>
                <div class="modal-body" style="max-height: 60vh; overflow-y: auto;">
                    <pre style="background: var(--color-bg); padding: 16px; border-radius: 8px; font-family: monospace; font-size: 12px; white-space: pre-wrap;">${data.logs || 'No logs'}</pre>
                </div>
            </div>
        `;
        document.body.appendChild(modal);
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load logs: ' + error.message);
    }
}

// ==========================================
// DOCKER IMAGES
// ==========================================

async function loadImages() {
    loading.show('Loading images...');
    
    try {
        const response = await fetch('/api/docker.php?action=list_images');
        const data = await response.json();
        
        const container = document.getElementById('imagesList');
        if (!container) return;
        
        if (!data.images || data.images.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <div class="empty-state-icon">🖼️</div>
                    <h3>No Images</h3>
                    <p>Pull images to get started</p>
                    <button class="btn-primary" onclick="showPullImage()" style="margin-top: 16px;">
                        Pull Image
                    </button>
                </div>
            `;
            loading.hide();
            return;
        }
        
        container.innerHTML = `
            <table class="data-table">
                <thead>
                    <tr>
                        <th>Repository</th>
                        <th>Tag</th>
                        <th>Size</th>
                        <th>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    ${data.images.map(img => `
                        <tr>
                            <td style="font-weight: 600;">${img.repository}</td>
                            <td>${img.tag}</td>
                            <td>${img.size}</td>
                            <td>
                                <button class="btn-danger btn-sm" onclick="removeImageConfirm('${img.id}', '${img.repository}:${img.tag}')">🗑️</button>
                            </td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>
        `;
        
        loading.hide();
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load images: ' + error.message);
    }
}

function showPullImage() {
    const image = prompt('Enter image name (e.g., nginx:latest, postgres:15):');
    if (!image) return;
    pullImage(image);
}

async function pullImage(image) {
    loading.show(`Pulling image ${image}...`);
    
    try {
        const response = await fetch('/api/docker.php?action=pull_image', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({image})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success(`Image ${image} pulled successfully`);
            if (typeof loadImages === 'function') loadImages();
        } else {
            toast.error('Failed to pull image: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to pull image: ' + error.message);
    }
}

function removeImageConfirm(id, name) {
    if (!confirm(`Remove image "${name}"?`)) return;
    removeImage(id);
}

async function removeImage(id) {
    loading.show('Removing image...');
    
    try {
        const response = await fetch('/api/docker.php?action=remove_image', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({id, force: true})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Image removed');
            loadImages();
        } else {
            toast.error('Failed to remove: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to remove image: ' + error.message);
    }
}

// ==========================================
// DOCKER COMPOSE DEPLOYMENT
// ==========================================

function showComposeDeployer() {
    const modal = document.createElement('div');
    modal.className = 'modal active';
    modal.innerHTML = `
        <div class="modal-content" style="max-width: 800px;">
            <div class="modal-header">
                <h3>🚀 Deploy from Docker Compose</h3>
                <button class="modal-close" onclick="this.closest('.modal').remove()">×</button>
            </div>
            <div class="modal-body">
                <div style="margin-bottom: 16px;">
                    <label>Project Name</label>
                    <input type="text" id="composeProject" placeholder="my-app">
                </div>
                <div style="margin-bottom: 16px;">
                    <label>Docker Compose YAML</label>
                    <textarea id="composeYaml" rows="15" style="font-family: monospace; font-size: 13px;" placeholder="version: '3'
services:
  web:
    image: nginx:latest
    ports:
      - 80:80"></textarea>
                </div>
                <button class="btn-primary" onclick="deployCompose()">🚀 Deploy</button>
            </div>
        </div>
    `;
    document.body.appendChild(modal);
}

async function deployCompose() {
    const project = document.getElementById('composeProject').value;
    const yaml = document.getElementById('composeYaml').value;
    
    if (!project || !yaml) {
        toast.error('Please enter project name and YAML');
        return;
    }
    
    loading.show('Deploying compose project...');
    
    try {
        const response = await fetch('/api/docker.php?action=deploy_compose', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({project, yaml})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success(`Project "${project}" deployed successfully`);
            document.querySelector('.modal.active')?.remove();
            loadContainers();
        } else {
            toast.error('Deployment failed: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Deployment failed: ' + error.message);
    }
}

async function refreshContainers() {
    await loadContainers();
}

async function refreshImages() {
    await loadImages();
}

// ==========================================
// DOCKER NETWORKS
// ==========================================

async function loadNetworks() {
    loading.show('Loading networks...');
    
    try {
        const response = await fetch('/api/docker.php?action=networks');
        const data = await response.json();
        
        const container = document.getElementById('networksList');
        if (!container) return;
        
        if (!data.networks || data.networks.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <div class="empty-state-icon">🌐</div>
                    <h3>No Networks</h3>
                    <p>Create networks to connect containers</p>
                </div>
            `;
            loading.hide();
            return;
        }
        
        container.innerHTML = `
            <table class="data-table">
                <thead>
                    <tr>
                        <th>Name</th>
                        <th>Driver</th>
                        <th>Scope</th>
                        <th>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    ${data.networks.map(net => `
                        <tr>
                            <td style="font-weight: 600;">${net.name}</td>
                            <td>${net.driver}</td>
                            <td>${net.scope}</td>
                            <td>
                                ${!['bridge', 'host', 'none'].includes(net.name) 
                                    ? `<button class="btn-danger btn-sm" onclick="removeNetworkConfirm('${net.id}', '${net.name}')">🗑️</button>`
                                    : '<span style="color: var(--color-text-secondary); font-size: 12px;">System network</span>'
                                }
                            </td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>
        `;
        
        loading.hide();
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load networks: ' + error.message);
    }
}

function showCreateNetwork() {
    const name = prompt('Network name:');
    if (!name) return;
    createNetwork(name);
}

async function createNetwork(name) {
    loading.show('Creating network...');
    
    try {
        const response = await fetch('/api/docker.php?action=create_network', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({name, driver: 'bridge'})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success(`Network "${name}" created`);
            loadNetworks();
        } else {
            toast.error('Failed to create network: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to create network: ' + error.message);
    }
}

function removeNetworkConfirm(id, name) {
    if (!confirm(`Remove network "${name}"?`)) return;
    removeNetwork(id);
}

async function removeNetwork(id) {
    loading.show('Removing network...');
    
    try {
        const response = await fetch('/api/docker.php?action=remove_network', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({id})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Network removed');
            loadNetworks();
        } else {
            toast.error('Failed to remove network: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to remove network: ' + error.message);
    }
}

// ==========================================
// DOCKER VOLUMES
// ==========================================

async function loadVolumes() {
    loading.show('Loading volumes...');
    
    try {
        const response = await fetch('/api/docker.php?action=volumes');
        const data = await response.json();
        
        const container = document.getElementById('volumesList');
        if (!container) return;
        
        if (!data.volumes || data.volumes.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <div class="empty-state-icon">💾</div>
                    <h3>No Volumes</h3>
                    <p>Create volumes to persist container data</p>
                </div>
            `;
            loading.hide();
            return;
        }
        
        container.innerHTML = `
            <table class="data-table">
                <thead>
                    <tr>
                        <th>Name</th>
                        <th>Driver</th>
                        <th>Mountpoint</th>
                        <th>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    ${data.volumes.map(vol => `
                        <tr>
                            <td style="font-weight: 600; font-family: monospace; font-size: 13px;">${vol.name}</td>
                            <td>${vol.driver}</td>
                            <td style="font-family: monospace; font-size: 12px;">${vol.mountpoint || '-'}</td>
                            <td>
                                <button class="btn-danger btn-sm" onclick="removeVolumeConfirm('${vol.name}')">🗑️</button>
                            </td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>
        `;
        
        loading.hide();
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load volumes: ' + error.message);
    }
}

function showCreateVolume() {
    const name = prompt('Volume name:');
    if (!name) return;
    createVolume(name);
}

async function createVolume(name) {
    loading.show('Creating volume...');
    
    try {
        const response = await fetch('/api/docker.php?action=create_volume', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({name})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success(`Volume "${name}" created`);
            loadVolumes();
        } else {
            toast.error('Failed to create volume: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to create volume: ' + error.message);
    }
}

function removeVolumeConfirm(name) {
    if (!confirm(`Remove volume "${name}"?\n\nThis will delete all data in the volume!`)) return;
    removeVolume(name);
}

async function removeVolume(name) {
    loading.show('Removing volume...');
    
    try {
        const response = await fetch('/api/docker.php?action=remove_volume', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({name})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Volume removed');
            loadVolumes();
        } else {
            toast.error('Failed to remove volume: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to remove volume: ' + error.message);
    }
}

function pruneVolumesConfirm() {
    if (!confirm('Remove all unused volumes?\n\nThis will delete data from unused volumes!')) return;
    pruneVolumes();
}

async function pruneVolumes() {
    loading.show('Pruning volumes...');
    
    try {
        const response = await fetch('/api/docker.php?action=prune_volumes', {
            method: 'POST'
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Unused volumes removed');
            loadVolumes();
        } else {
            toast.error('Failed to prune volumes: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to prune volumes: ' + error.message);
    }
}

function pruneImagesConfirm() {
    if (!confirm('Remove all unused images?')) return;
    pruneImages();
}

async function pruneImages() {
    loading.show('Pruning images...');
    
    try {
        const response = await fetch('/api/docker.php?action=prune_images', {
            method: 'POST'
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Unused images removed');
            loadImages();
        } else {
            toast.error('Failed to prune images: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to prune images: ' + error.message);
    }
}

async function refreshNetworks() {
    await loadNetworks();
}

async function refreshVolumes() {
    await loadVolumes();
}

// ==========================================
// NFS SHARE MANAGEMENT
// ==========================================

async function loadNFSShares() {
    loading.show('Loading NFS shares...');
    
    try {
        const response = await fetch('/api/nfs.php?action=list');
        const data = await response.json();
        
        const container = document.getElementById('nfsSharesList');
        if (!container) return;
        
        if (!data.shares || data.shares.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <div class="empty-state-icon">📁</div>
                    <h3>No NFS Shares</h3>
                    <p>Create NFS shares to share files with Linux/Unix systems</p>
                    <button class="btn-primary" onclick="showCreateNFSShare()" style="margin-top: 16px;">
                        Create NFS Share
                    </button>
                </div>
            `;
            loading.hide();
            return;
        }
        
        container.innerHTML = `
            <table class="data-table">
                <thead>
                    <tr>
                        <th>Path</th>
                        <th>Clients</th>
                        <th>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    ${data.shares.map(share => `
                        <tr>
                            <td style="font-weight: 600; font-family: monospace;">${share.path}</td>
                            <td style="font-size: 13px;">${share.clients}</td>
                            <td>
                                <button class="btn-danger btn-sm" onclick="removeNFSShareConfirm('${share.path}')">🗑️ Remove</button>
                            </td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>
        `;
        
        loading.hide();
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load NFS shares: ' + error.message);
    }
}

function showCreateNFSShare() {
    const modal = document.createElement('div');
    modal.className = 'modal active';
    modal.innerHTML = `
        <div class="modal-content">
            <div class="modal-header">
                <h3>Create NFS Share</h3>
                <button class="modal-close" onclick="this.closest('.modal').remove()">×</button>
            </div>
            <div class="modal-body">
                <div style="display: grid; gap: 16px;">
                    <div>
                        <label>Path to Share</label>
                        <input type="text" id="nfsPath" placeholder="/mnt/tank/data">
                    </div>
                    <div>
                        <label>Allowed Clients</label>
                        <input type="text" id="nfsClients" value="*" placeholder="* or 192.168.1.0/24">
                        <p style="font-size: 12px; color: var(--color-text-secondary); margin-top: 4px;">
                            * = all clients, IP address, or network CIDR
                        </p>
                    </div>
                    <div>
                        <label>Options</label>
                        <input type="text" id="nfsOptions" value="rw,sync,no_subtree_check">
                    </div>
                    <button class="btn-primary" onclick="createNFSShare()">Create Share</button>
                </div>
            </div>
        </div>
    `;
    document.body.appendChild(modal);
}

async function createNFSShare() {
    const path = document.getElementById('nfsPath').value;
    const client = document.getElementById('nfsClients').value;
    const options = document.getElementById('nfsOptions').value;
    
    if (!path) {
        toast.error('Please enter a path');
        return;
    }
    
    loading.show('Creating NFS share...');
    
    try {
        const response = await fetch('/api/nfs.php?action=add', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({path, client, options})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('NFS share created');
            document.querySelector('.modal.active')?.remove();
            loadNFSShares();
        } else {
            toast.error('Failed to create share: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to create share: ' + error.message);
    }
}

function removeNFSShareConfirm(path) {
    if (!confirm(`Remove NFS share for "${path}"?`)) return;
    removeNFSShare(path);
}

async function removeNFSShare(path) {
    loading.show('Removing NFS share...');
    
    try {
        const response = await fetch('/api/nfs.php?action=remove', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({path})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('NFS share removed');
            loadNFSShares();
        } else {
            toast.error('Failed to remove share: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to remove share: ' + error.message);
    }
}

// ==========================================
// SMB/SAMBA SHARE MANAGEMENT
// ==========================================

async function loadSMBShares() {
    loading.show('Loading SMB shares...');
    
    try {
        const response = await fetch('/api/smb.php?action=list');
        const data = await response.json();
        
        const container = document.getElementById('smbSharesList');
        if (!container) return;
        
        if (!data.shares || data.shares.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <div class="empty-state-icon">🪟</div>
                    <h3>No SMB Shares</h3>
                    <p>Create SMB shares to share files with Windows systems</p>
                    <button class="btn-primary" onclick="showCreateSMBShare()" style="margin-top: 16px;">
                        Create SMB Share
                    </button>
                </div>
            `;
            loading.hide();
            return;
        }
        
        container.innerHTML = `
            <table class="data-table">
                <thead>
                    <tr>
                        <th>Name</th>
                        <th>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    ${data.shares.map(share => `
                        <tr>
                            <td style="font-weight: 600;">${share.name}</td>
                            <td>
                                <button class="btn-secondary btn-sm" onclick="viewSMBShareDetails('${share.name}')">ℹ️ Details</button>
                                <button class="btn-danger btn-sm" onclick="removeSMBShareConfirm('${share.name}')">🗑️ Remove</button>
                            </td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>
        `;
        
        loading.hide();
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load SMB shares: ' + error.message);
    }
}

function showCreateSMBShare() {
    const modal = document.createElement('div');
    modal.className = 'modal active';
    modal.innerHTML = `
        <div class="modal-content">
            <div class="modal-header">
                <h3>Create SMB Share</h3>
                <button class="modal-close" onclick="this.closest('.modal').remove()">×</button>
            </div>
            <div class="modal-body">
                <div style="display: grid; gap: 16px;">
                    <div>
                        <label>Share Name</label>
                        <input type="text" id="smbName" placeholder="myshare">
                    </div>
                    <div>
                        <label>Path</label>
                        <input type="text" id="smbPath" placeholder="/mnt/tank/data">
                    </div>
                    <div>
                        <label>Comment (optional)</label>
                        <input type="text" id="smbComment" placeholder="My shared folder">
                    </div>
                    <div>
                        <label style="display: flex; align-items: center;">
                            <input type="checkbox" id="smbGuest" style="width: 20px; height: 20px; margin-right: 8px;">
                            Allow guest access
                        </label>
                    </div>
                    <button class="btn-primary" onclick="createSMBShare()">Create Share</button>
                </div>
            </div>
        </div>
    `;
    document.body.appendChild(modal);
}

async function createSMBShare() {
    const name = document.getElementById('smbName').value;
    const path = document.getElementById('smbPath').value;
    const comment = document.getElementById('smbComment').value;
    const guest = document.getElementById('smbGuest').checked;
    
    if (!name || !path) {
        toast.error('Please enter name and path');
        return;
    }
    
    loading.show('Creating SMB share...');
    
    try {
        const response = await fetch('/api/smb.php?action=add', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({name, path, comment, guest})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('SMB share created');
            document.querySelector('.modal.active')?.remove();
            loadSMBShares();
        } else {
            toast.error('Failed to create share: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to create share: ' + error.message);
    }
}

function removeSMBShareConfirm(name) {
    if (!confirm(`Remove SMB share "${name}"?`)) return;
    removeSMBShare(name);
}

async function removeSMBShare(name) {
    loading.show('Removing SMB share...');
    
    try {
        const response = await fetch('/api/smb.php?action=remove', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({name})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('SMB share removed');
            loadSMBShares();
        } else {
            toast.error('Failed to remove share: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to remove share: ' + error.message);
    }
}

async function viewSMBShareDetails(name) {
    loading.show('Loading share details...');
    
    try {
        const response = await fetch(`/api/smb.php?action=details&name=${name}`);
        const data = await response.json();
        
        loading.hide();
        
        if (data.error) {
            toast.error('Failed to load details: ' + data.error);
            return;
        }
        
        const modal = document.createElement('div');
        modal.className = 'modal active';
        modal.innerHTML = `
            <div class="modal-content">
                <div class="modal-header">
                    <h3>Share Details: ${name}</h3>
                    <button class="modal-close" onclick="this.closest('.modal').remove()">×</button>
                </div>
                <div class="modal-body">
                    <pre style="background: var(--color-surface-light); padding: 16px; border-radius: 8px; font-family: monospace; font-size: 13px;">${JSON.stringify(data.details, null, 2)}</pre>
                </div>
            </div>
        `;
        document.body.appendChild(modal);
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load details: ' + error.message);
    }
}

async function refreshNFSShares() {
    await loadNFSShares();
}

async function refreshSMBShares() {
    await loadSMBShares();
}

// ==========================================
// USER MANAGEMENT
// ==========================================

async function loadUsers() {
    loading.show('Loading users...');
    
    try {
        const response = await fetch('/api/users.php?action=list');
        const data = await response.json();
        
        const container = document.getElementById('usersList');
        if (!container) return;
        
        if (!data.users || data.users.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <div class="empty-state-icon">👤</div>
                    <h3>No Users</h3>
                    <p>Create users to manage system access</p>
                </div>
            `;
            loading.hide();
            return;
        }
        
        container.innerHTML = `
            <table class="data-table">
                <thead>
                    <tr>
                        <th>Username</th>
                        <th>UID</th>
                        <th>Full Name</th>
                        <th>Home</th>
                        <th>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    ${data.users.map(user => `
                        <tr>
                            <td style="font-weight: 600;">${user.username}</td>
                            <td>${user.uid}</td>
                            <td>${user.fullname || '-'}</td>
                            <td style="font-family: monospace; font-size: 12px;">${user.home}</td>
                            <td>
                                <button class="btn-secondary btn-sm" onclick="changeUserPassword('${user.username}')">🔑 Password</button>
                                <button class="btn-danger btn-sm" onclick="removeUserConfirm('${user.username}')">🗑️</button>
                            </td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>
        `;
        
        loading.hide();
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load users: ' + error.message);
    }
}

function showCreateUser() {
    const modal = document.createElement('div');
    modal.className = 'modal active';
    modal.innerHTML = `
        <div class="modal-content">
            <div class="modal-header">
                <h3>Create User</h3>
                <button class="modal-close" onclick="this.closest('.modal').remove()">×</button>
            </div>
            <div class="modal-body">
                <div style="display: grid; gap: 16px;">
                    <div>
                        <label>Username</label>
                        <input type="text" id="userName" placeholder="john">
                    </div>
                    <div>
                        <label>Full Name</label>
                        <input type="text" id="userFullName" placeholder="John Doe">
                    </div>
                    <div>
                        <label>Password</label>
                        <input type="password" id="userPassword">
                    </div>
                    <div>
                        <label>Shell</label>
                        <select id="userShell">
                            <option value="/bin/bash">/bin/bash</option>
                            <option value="/bin/sh">/bin/sh</option>
                            <option value="/bin/zsh">/bin/zsh</option>
                            <option value="/usr/sbin/nologin">No login</option>
                        </select>
                    </div>
                    <button class="btn-primary" onclick="createUser()">Create User</button>
                </div>
            </div>
        </div>
    `;
    document.body.appendChild(modal);
}

async function createUser() {
    const username = document.getElementById('userName').value;
    const fullname = document.getElementById('userFullName').value;
    const password = document.getElementById('userPassword').value;
    const shell = document.getElementById('userShell').value;
    
    if (!username) {
        toast.error('Please enter username');
        return;
    }
    
    loading.show('Creating user...');
    
    try {
        const response = await fetch('/api/users.php?action=add', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({username, fullname, password, shell, createHome: true})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success(`User "${username}" created`);
            document.querySelector('.modal.active')?.remove();
            loadUsers();
        } else {
            toast.error('Failed to create user: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to create user: ' + error.message);
    }
}

function removeUserConfirm(username) {
    if (!confirm(`Remove user "${username}"?\n\nThis will also remove the user's home directory!`)) return;
    removeUser(username);
}

async function removeUser(username) {
    loading.show('Removing user...');
    
    try {
        const response = await fetch('/api/users.php?action=remove', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({username, removeHome: true})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('User removed');
            loadUsers();
        } else {
            toast.error('Failed to remove user: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to remove user: ' + error.message);
    }
}

function changeUserPassword(username) {
    const password = prompt(`Enter new password for ${username}:`);
    if (!password) return;
    
    setUserPassword(username, password);
}

async function setUserPassword(username, password) {
    loading.show('Changing password...');
    
    try {
        const response = await fetch('/api/users.php?action=change_password', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({username, password})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Password changed');
        } else {
            toast.error('Failed to change password: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to change password: ' + error.message);
    }
}

async function loadGroups() {
    loading.show('Loading groups...');
    
    try {
        const response = await fetch('/api/users.php?action=list_groups');
        const data = await response.json();
        
        const container = document.getElementById('groupsList');
        if (!container) return;
        
        if (!data.groups || data.groups.length === 0) {
            container.innerHTML = `
                <div class="empty-state">
                    <div class="empty-state-icon">👥</div>
                    <h3>No Groups</h3>
                    <p>Create groups to organize users</p>
                </div>
            `;
            loading.hide();
            return;
        }
        
        container.innerHTML = `
            <table class="data-table">
                <thead>
                    <tr>
                        <th>Group Name</th>
                        <th>GID</th>
                        <th>Members</th>
                        <th>Actions</th>
                    </tr>
                </thead>
                <tbody>
                    ${data.groups.map(group => `
                        <tr>
                            <td style="font-weight: 600;">${group.name}</td>
                            <td>${group.gid}</td>
                            <td>${group.members.length > 0 ? group.members.join(', ') : '-'}</td>
                            <td>
                                <button class="btn-danger btn-sm" onclick="removeGroupConfirm('${group.name}')">🗑️</button>
                            </td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>
        `;
        
        loading.hide();
        
    } catch (error) {
        loading.hide();
        toast.error('Failed to load groups: ' + error.message);
    }
}

function showCreateGroup() {
    const name = prompt('Group name:');
    if (!name) return;
    createGroup(name);
}

async function createGroup(name) {
    loading.show('Creating group...');
    
    try {
        const response = await fetch('/api/users.php?action=add_group', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({name})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success(`Group "${name}" created`);
            loadGroups();
        } else {
            toast.error('Failed to create group: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to create group: ' + error.message);
    }
}

function removeGroupConfirm(name) {
    if (!confirm(`Remove group "${name}"?`)) return;
    removeGroup(name);
}

async function removeGroup(name) {
    loading.show('Removing group...');
    
    try {
        const response = await fetch('/api/users.php?action=remove_group', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({name})
        });
        
        const data = await response.json();
        loading.hide();
        
        if (data.success) {
            toast.success('Group removed');
            loadGroups();
        } else {
            toast.error('Failed to remove group: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed to remove group: ' + error.message);
    }
}

async function refreshUsers() {
    await loadUsers();
}

async function refreshGroups() {
    await loadGroups();
}

// ==========================================
// BACKUP MANAGEMENT
// ==========================================

async function loadBackups() {
    loading.show('Loading backups...');
    try {
        const response = await fetch('/api/backup.php?action=list');
        const data = await response.json();
        const container = document.getElementById('backupsList');
        if (!container) return;
        if (!data.backups || data.backups.length === 0) {
            container.innerHTML = '<div class="empty-state"><div class="empty-state-icon">💾</div><h3>No Backups</h3><p>Create backups to protect your data</p></div>';
            loading.hide();
            return;
        }
        container.innerHTML = '<table class="data-table"><thead><tr><th>Name</th><th>Size</th><th>Date</th><th>Actions</th></tr></thead><tbody>' +
            data.backups.map(b => `<tr><td>${b.name}</td><td>${b.sizeFormatted}</td><td>${b.date}</td><td><button class="btn-danger btn-sm" onclick="deleteBackupConfirm('${b.name}')">🗑️</button></td></tr>`).join('') +
            '</tbody></table>';
        loading.hide();
    } catch (error) {
        loading.hide();
        toast.error('Failed to load backups: ' + error.message);
    }
}

function showCreateBackup() {
    const modal = document.createElement('div');
    modal.className = 'modal active';
    modal.innerHTML = `<div class="modal-content"><div class="modal-header"><h3>Create Backup</h3><button class="modal-close" onclick="this.closest('.modal').remove()">×</button></div><div class="modal-body"><div><label>Backup Name</label><input type="text" id="backupName" value="backup-${Date.now()}"></div><button class="btn-primary" style="margin-top:16px" onclick="createBackup()">Create Backup</button></div></div>`;
    document.body.appendChild(modal);
}

async function createBackup() {
    const name = document.getElementById('backupName').value;
    if (!name) { toast.error('Enter backup name'); return; }
    loading.show('Creating backup...');
    try {
        const response = await fetch('/api/backup.php?action=create', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({name, paths: ['/etc']})
        });
        const data = await response.json();
        loading.hide();
        if (data.success) {
            toast.success('Backup created');
            document.querySelector('.modal.active')?.remove();
            loadBackups();
        } else {
            toast.error('Failed: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed: ' + error.message);
    }
}

function deleteBackupConfirm(name) {
    if (!confirm(`Delete backup "${name}"?`)) return;
    deleteBackup(name);
}

async function deleteBackup(name) {
    loading.show('Deleting...');
    try {
        const response = await fetch('/api/backup.php?action=delete', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({backup: name})
        });
        const data = await response.json();
        loading.hide();
        if (data.success) {
            toast.success('Backup deleted');
            loadBackups();
        } else {
            toast.error('Failed: ' + data.error);
        }
    } catch (error) {
        loading.hide();
        toast.error('Failed: ' + error.message);
    }
}

// ==========================================
// SYSTEM SETTINGS
// ==========================================

async function loadSystemInfo() {
    try {
        const response = await fetch('/api/settings.php?action=info');
        const data = await response.json();
        const container = document.getElementById('systemInfo');
        if (!container) return;
        container.innerHTML = `<div class="glass-card"><h3>System Information</h3><div style="display:grid;gap:12px;margin-top:16px;">
            <div><strong>Hostname:</strong> ${data.hostname}</div>
            <div><strong>OS:</strong> ${data.os || 'Linux'}</div>
            <div><strong>Kernel:</strong> ${data.kernel}</div>
            <div><strong>Uptime:</strong> ${data.uptime}</div>
            <div><strong>Timezone:</strong> ${data.timezone}</div>
        </div></div>`;
    } catch (error) {
        console.error('Failed to load system info:', error);
    }
}

async function loadServices() {
    loading.show('Loading services...');
    try {
        const response = await fetch('/api/settings.php?action=services');
        const data = await response.json();
        const container = document.getElementById('servicesList');
        if (!container) return;
        container.innerHTML = '<table class="data-table"><thead><tr><th>Service</th><th>Status</th><th>Actions</th></tr></thead><tbody>' +
            data.services.slice(0, 50).map(s => `<tr><td>${s.name}</td><td><span class="badge badge-${s.active === 'active' ? 'success' : 'danger'}">${s.active}</span></td><td>
                ${s.active === 'active' ? `<button class="btn-secondary btn-sm" onclick="stopService('${s.name}')">⏸️ Stop</button>` : `<button class="btn-primary btn-sm" onclick="startService('${s.name}')">▶️ Start</button>`}
                <button class="btn-secondary btn-sm" onclick="restartService('${s.name}')">🔄 Restart</button></td></tr>`).join('') +
            '</tbody></table>';
        loading.hide();
    } catch (error) {
        loading.hide();
        toast.error('Failed to load services: ' + error.message);
    }
}

async function startService(service) {
    loading.show('Starting...');
    try {
        const response = await fetch('/api/settings.php?action=start_service', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({service})
        });
        const data = await response.json();
        loading.hide();
        if (data.success) { toast.success('Service started'); loadServices(); }
        else { toast.error('Failed: ' + data.error); }
    } catch (error) {
        loading.hide();
        toast.error('Failed: ' + error.message);
    }
}

async function stopService(service) {
    loading.show('Stopping...');
    try {
        const response = await fetch('/api/settings.php?action=stop_service', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({service})
        });
        const data = await response.json();
        loading.hide();
        if (data.success) { toast.success('Service stopped'); loadServices(); }
        else { toast.error('Failed: ' + data.error); }
    } catch (error) {
        loading.hide();
        toast.error('Failed: ' + error.message);
    }
}

async function restartService(service) {
    loading.show('Restarting...');
    try {
        const response = await fetch('/api/settings.php?action=restart_service', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({service})
        });
        const data = await response.json();
        loading.hide();
        if (data.success) { toast.success('Service restarted'); loadServices(); }
        else { toast.error('Failed: ' + data.error); }
    } catch (error) {
        loading.hide();
        toast.error('Failed: ' + error.message);
    }
}

async function refreshBackups() { await loadBackups(); }
async function refreshServices() { await loadServices(); }
