/**
 * D-PlaneOS Daemon API Client
 * Maps UI calls to Go daemon REST endpoints
 */

class DaemonAPI {
  constructor() {
    this.baseURL = '';
  }

  // Helper for CSRF-protected fetch with retry logic
  async fetch(url, options = {}) {
    const defaultOptions = {
      headers: {
        'Content-Type': 'application/json',
        ...options.headers
      },
      ...options
    };
    
    // Retry logic for SQLite busy/FTS5 collisions
    // Critical for large systems with heavy concurrent access
    const maxRetries = 3;
    const retryDelay = 100; // ms
    
    for (let attempt = 1; attempt <= maxRetries; attempt++) {
      try {
        // Use existing csrfFetch if available
        const fetchFn = typeof csrfFetch === 'function' ? csrfFetch : fetch;
        const response = await fetchFn(url, defaultOptions);
        
        // If response is OK, return it
        if (response.ok) {
          return response;
        }
        
        // If server error (500-599), retry
        if (response.status >= 500 && attempt < maxRetries) {
          console.warn(`API call failed (${response.status}), retrying (${attempt}/${maxRetries})...`);
          await new Promise(resolve => setTimeout(resolve, retryDelay * attempt));
          continue;
        }
        
        // Otherwise return the response (let caller handle)
        return response;
        
      } catch (error) {
        // Network error - retry
        if (attempt < maxRetries) {
          console.warn(`Network error, retrying (${attempt}/${maxRetries}):`, error.message);
          await new Promise(resolve => setTimeout(resolve, retryDelay * attempt));
          continue;
        }
        
        // Max retries exhausted
        throw error;
      }
    }
    
    // Should never reach here, but just in case
    throw new Error('Max retries exceeded');
  }

  // Docker API
  async docker_list() {
    const response = await this.fetch('/api/docker/containers');
    return response.json();
  }

  async docker_action(containerId, action) {
    const response = await this.fetch('/api/docker/action', {
      method: 'POST',
      body: JSON.stringify({ container_id: containerId, action: action })
    });
    return response.json();
  }

  // ZFS API
  async zfs_pools() {
    const response = await this.fetch('/api/zfs/pools');
    return response.json();
  }

  async zfs_datasets() {
    const response = await this.fetch('/api/zfs/datasets');
    return response.json();
  }

  async zfs_command(command, args) {
    const response = await this.fetch('/api/zfs/command', {
      method: 'POST',
      body: JSON.stringify({ command: command, args: args })
    });
    return response.json();
  }

  // ZFS Encryption API
  async zfs_encryption_list() {
    const response = await this.fetch('/api/zfs/encryption/list');
    return response.json();
  }

  async zfs_encryption_unlock(dataset, key) {
    const response = await this.fetch('/api/zfs/encryption/unlock', {
      method: 'POST',
      body: JSON.stringify({ dataset, key })
    });
    return response.json();
  }

  async zfs_encryption_lock(dataset) {
    const response = await this.fetch('/api/zfs/encryption/lock', {
      method: 'POST',
      body: JSON.stringify({ dataset })
    });
    return response.json();
  }

  async zfs_encryption_create(name, encryption, key) {
    const response = await this.fetch('/api/zfs/encryption/create', {
      method: 'POST',
      body: JSON.stringify({ name, encryption, key })
    });
    return response.json();
  }

  async zfs_encryption_change_key(dataset, old_key, new_key) {
    const response = await this.fetch('/api/zfs/encryption/change-key', {
      method: 'POST',
      body: JSON.stringify({ dataset, old_key, new_key })
    });
    return response.json();
  }

  // System API
  async system_ups() {
    const response = await this.fetch('/api/system/ups');
    return response.json();
  }

  async system_network() {
    const response = await this.fetch('/api/system/network');
    return response.json();
  }

  async system_logs(lines = 100) {
    const response = await this.fetch(`/api/system/logs?lines=${lines}`);
    return response.json();
  }

  // Files API
  async files_list(path = '/') {
    const response = await this.fetch(`/api/files/list?path=${encodeURIComponent(path)}`);
    return response.json();
  }

  async files_properties(path) {
    const response = await this.fetch(`/api/files/properties?path=${encodeURIComponent(path)}`);
    return response.json();
  }

  async files_rename(oldPath, newPath) {
    const response = await this.fetch('/api/files/rename', {
      method: 'POST',
      body: JSON.stringify({ old_path: oldPath, new_path: newPath })
    });
    return response.json();
  }

  async files_copy(source, destination) {
    const response = await this.fetch('/api/files/copy', {
      method: 'POST',
      body: JSON.stringify({ source, destination })
    });
    return response.json();
  }

  async files_mkdir(path) {
    const response = await this.fetch('/api/files/mkdir', {
      method: 'POST',
      body: JSON.stringify({ path: path })
    });
    return response.json();
  }

  async files_delete(path) {
    const response = await this.fetch('/api/files/delete', {
      method: 'POST',
      body: JSON.stringify({ path: path })
    });
    return response.json();
  }

  async files_chmod(path, mode) {
    const response = await this.fetch('/api/files/chmod', {
      method: 'POST',
      body: JSON.stringify({ path: path, mode: mode })
    });
    return response.json();
  }

  async files_chown(path, uid, gid) {
    const response = await this.fetch('/api/files/chown', {
      method: 'POST',
      body: JSON.stringify({ path: path, uid: uid, gid: gid })
    });
    return response.json();
  }

  // Shares API
  async shares_smb_reload() {
    const response = await this.fetch('/api/shares/smb/reload', {
      method: 'POST'
    });
    return response.json();
  }

  async shares_smb_test() {
    const response = await this.fetch('/api/shares/smb/test', {
      method: 'POST'
    });
    return response.json();
  }

  async shares_nfs_list() {
    const response = await this.fetch('/api/shares/nfs/list');
    return response.json();
  }

  async shares_nfs_reload() {
    const response = await this.fetch('/api/shares/nfs/reload', {
      method: 'POST'
    });
    return response.json();
  }

  // Backup API
  async backup_rsync(source, destination, options = {}) {
    const response = await this.fetch('/api/backup/rsync', {
      method: 'POST',
      body: JSON.stringify({ source, destination, ...options })
    });
    return response.json();
  }

  // Replication API
  async replication_send(dataset, target) {
    const response = await this.fetch('/api/replication/send', {
      method: 'POST',
      body: JSON.stringify({ dataset, target })
    });
    return response.json();
  }

  async replication_send_incremental(dataset, snapshot, target) {
    const response = await this.fetch('/api/replication/send-incremental', {
      method: 'POST',
      body: JSON.stringify({ dataset, snapshot, target })
    });
    return response.json();
  }

  async replication_receive(stream) {
    const response = await this.fetch('/api/replication/receive', {
      method: 'POST',
      body: JSON.stringify({ stream })
    });
    return response.json();
  }

  // Settings API (Telegram)
  async settings_telegram_get() {
    const response = await this.fetch('/api/settings/telegram');
    return response.json();
  }

  async settings_telegram_save(config) {
    const response = await this.fetch('/api/settings/telegram', {
      method: 'POST',
      body: JSON.stringify(config)
    });
    return response.json();
  }

  async settings_telegram_test(config) {
    const response = await this.fetch('/api/settings/telegram/test', {
      method: 'POST',
      body: JSON.stringify(config)
    });
    return response.json();
  }

  // Removable Media API
  async removable_list() {
    const response = await this.fetch('/api/removable/list');
    return response.json();
  }

  async removable_mount(device, mountPoint) {
    const response = await this.fetch('/api/removable/mount', {
      method: 'POST',
      body: JSON.stringify({ device, mount_point: mountPoint })
    });
    return response.json();
  }

  async removable_unmount(device) {
    const response = await this.fetch('/api/removable/unmount', {
      method: 'POST',
      body: JSON.stringify({ device })
    });
    return response.json();
  }

  async removable_eject(device) {
    const response = await this.fetch('/api/removable/eject', {
      method: 'POST',
      body: JSON.stringify({ device })
    });
    return response.json();
  }
  
  // Monitoring API
  async monitoring_inotify() {
    const response = await this.fetch('/api/monitoring/inotify');
    return response.json();
  }
}

// Global instance
const daemonAPI = new DaemonAPI();
