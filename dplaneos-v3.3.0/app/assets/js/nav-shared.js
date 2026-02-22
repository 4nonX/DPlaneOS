/**
 * D-PlaneOS Shared Navigation Logic
 * Single source of truth for nav behavior used across all pages.
 *
 * Nav injection: pages with <div id="nav-root"></div> get the nav HTML
 * injected here instead of having 8KB of nav duplicated in every file.
 * Pages that still have inline nav-container HTML are unaffected (graceful).
 */

(function() {
  const root = document.getElementById('nav-root');
  if (!root) return; // page has its own nav HTML — skip

  root.innerHTML = `<nav class="nav-container">
  <div class="nav-main">
    <div style="display: flex; align-items: center; gap: 16px;">
      <div class="logo">
        D-PlaneOS
        <span class="version-badge" id="nav-version-badge"></span>
      </div>
    </div>
    <div class="nav-links">
      <button class="nav-link" onclick="navigateToSection('dashboard')" data-section="dashboard">Dashboard</button>
      <button class="nav-link" onclick="toggleSubNav('storage', this)" data-section="storage">Storage</button>
      <button class="nav-link" onclick="toggleSubNav('compute', this)" data-section="compute">Compute</button>
      <button class="nav-link" onclick="toggleSubNav('network', this)" data-section="network">Network</button>
      <button class="nav-link" onclick="toggleSubNav('identity', this)" data-section="identity">Identity</button>
      <button class="nav-link" onclick="toggleSubNav('security', this)" data-section="security">Security</button>
      <button class="nav-link" onclick="toggleSubNav('system', this)" data-section="system">System</button>
    </div>
    <div class="nav-actions">
      <button class="nav-action-btn" onclick="showKeyboardHelp()" title="Keyboard Shortcuts (?)">
        <span class="material-symbols-rounded">keyboard</span>
      </button>
      <div class="connection-status online" id="navConnectionStatus">
        <span class="status-dot"></span>
        <span>Connected</span>
      </div>
    </div>
  </div>

  <div id="sub-storage" class="nav-sub"><div class="nav-sub-inner">
    <a href="pools.html" class="sub-link" data-page="pools"><span class="material-symbols-rounded">database</span> ZFS Pools</a>
    <a href="pools-advanced.html" class="sub-link" data-page="pools-advanced"><span class="material-symbols-rounded">photo_camera</span> Snapshots</a>
    <a href="shares.html" class="sub-link" data-page="shares"><span class="material-symbols-rounded">folder_shared</span> Shares</a>
    <a href="replication.html" class="sub-link" data-page="replication"><span class="material-symbols-rounded">sync</span> Replication</a>
    <a href="files.html" class="sub-link" data-page="files"><span class="material-symbols-rounded">folder_open</span> File Explorer</a>
    <a href="quotas.html" class="sub-link" data-page="quotas"><span class="material-symbols-rounded">pie_chart</span> Quotas</a>
    <a href="snapshot-scheduler.html" class="sub-link" data-page="snapshot-scheduler"><span class="material-symbols-rounded">schedule</span> Snapshot Scheduler</a>
    <a href="acl-manager.html" class="sub-link" data-page="acl-manager"><span class="material-symbols-rounded">admin_panel_settings</span> ACL Manager</a>
    <a href="zfs-encryption.html" class="sub-link" data-page="zfs-encryption"><span class="material-symbols-rounded">enhanced_encryption</span> Encryption</a>
    <a href="files-enhanced.html" class="sub-link" data-page="files-enhanced"><span class="material-symbols-rounded">upload_file</span> File Upload</a>
    <a href="cloud-sync.html" class="sub-link" data-page="cloud-sync"><span class="material-symbols-rounded">cloud_sync</span> Cloud Sync</a>
    <a href="iscsi.html" class="sub-link" data-page="iscsi"><span class="material-symbols-rounded">storage</span> iSCSI Targets</a>
  </div></div>

  <div id="sub-compute" class="nav-sub"><div class="nav-sub-inner">
    <a href="docker.html" class="sub-link" data-page="docker"><span class="material-symbols-rounded">dns</span> Docker Containers</a>
    <a href="git-sync.html" class="sub-link" data-page="git-sync"><span class="material-symbols-rounded">sync</span> Git Sync</a>
    <a href="gitops.html" class="sub-link" data-page="gitops"><span class="material-symbols-rounded">account_tree</span> GitOps State</a>
    <a href="modules.html" class="sub-link" data-page="modules"><span class="material-symbols-rounded">extension</span> App Modules</a>
  </div></div>

  <div id="sub-network" class="nav-sub"><div class="nav-sub-inner">
    <a href="network.html" class="sub-link" data-page="network"><span class="material-symbols-rounded">lan</span> Network Settings</a>
    <a href="network-interfaces.html" class="sub-link" data-page="network-interfaces"><span class="material-symbols-rounded">settings_ethernet</span> Interfaces</a>
    <a href="network-dns.html" class="sub-link" data-page="network-dns"><span class="material-symbols-rounded">dns</span> DNS</a>
    <a href="network-routing.html" class="sub-link" data-page="network-routing"><span class="material-symbols-rounded">route</span> Routing</a>
  </div></div>

  <div id="sub-identity" class="nav-sub"><div class="nav-sub-inner">
    <a href="users.html" class="sub-link" data-page="users"><span class="material-symbols-rounded">person</span> Users</a>
    <a href="groups.html" class="sub-link" data-page="groups"><span class="material-symbols-rounded">group</span> Groups</a>
    <a href="directory-service.html" class="sub-link" data-page="directory-service"><span class="material-symbols-rounded">domain</span> Directory Service</a>
    <a href="rbac-management.html" class="sub-link" data-page="rbac-management"><span class="material-symbols-rounded">admin_panel_settings</span> Roles &amp; Permissions</a>
  </div></div>

  <div id="sub-security" class="nav-sub"><div class="nav-sub-inner">
    <a href="security.html" class="sub-link" data-page="security"><span class="material-symbols-rounded">shield</span> Security Settings</a>
    <a href="audit.html" class="sub-link" data-page="audit"><span class="material-symbols-rounded">fact_check</span> Audit Logs</a>
    <a href="firewall.html" class="sub-link" data-page="firewall"><span class="material-symbols-rounded">local_fire_department</span> Firewall</a>
    <a href="certificates.html" class="sub-link" data-page="certificates"><span class="material-symbols-rounded">verified_user</span> Certificates</a>
  </div></div>

  <div id="sub-system" class="nav-sub"><div class="nav-sub-inner">
    <a href="settings.html" class="sub-link" data-page="settings"><span class="material-symbols-rounded">tune</span> General Settings</a>
    <a href="system-updates.html" class="sub-link" data-page="system-updates"><span class="material-symbols-rounded">upgrade</span> System Updates</a>
    <a href="logs.html" class="sub-link" data-page="logs"><span class="material-symbols-rounded">description</span> System Logs</a>
    <a href="ups.html" class="sub-link" data-page="ups"><span class="material-symbols-rounded">battery_charging_full</span> UPS Management</a>
    <a href="reporting.html" class="sub-link" data-page="reporting"><span class="material-symbols-rounded">monitoring</span> Reporting</a>
    <a href="power-management.html" class="sub-link" data-page="power-management"><span class="material-symbols-rounded">power_settings_new</span> Power Management</a>
    <a href="system-settings.html" class="sub-link" data-page="system-settings"><span class="material-symbols-rounded">settings_suggest</span> System Settings</a>
    <a href="system-monitoring.html" class="sub-link" data-page="system-monitoring"><span class="material-symbols-rounded">monitor_heart</span> Monitoring</a>
    <a href="hardware.html" class="sub-link" data-page="hardware"><span class="material-symbols-rounded">developer_board</span> Hardware</a>
    <a href="ipmi.html" class="sub-link" data-page="ipmi"><span class="material-symbols-rounded">memory</span> IPMI</a>
    <a href="removable-media-ui.html" class="sub-link" data-page="removable-media-ui"><span class="material-symbols-rounded">usb</span> Removable Media</a>
    <a href="ha-cluster.html" class="sub-link" data-page="ha-cluster"><span class="material-symbols-rounded">device_hub</span> HA Cluster</a>
    <a href="alerts.html" class="sub-link" data-page="alerts"><span class="material-symbols-rounded">notifications_active</span> Alerts</a>
    <a href="support.html" class="sub-link" data-page="support"><span class="material-symbols-rounded">support_agent</span> Support &amp; Diagnostics</a>
  </div></div>
</nav>`;
})();

// Navigation behavior
function navigateToSection(section) {
  if (section === 'dashboard') {
    window.location.href = 'index.html';
  }
}

function toggleSubNav(section, button) {
  document.querySelectorAll('.nav-link').forEach(link => {
    link.classList.remove('active');
  });
  if (button) {
    button.classList.add('active');
  }
  document.querySelectorAll('.nav-sub').forEach(sub => {
    sub.classList.remove('active');
  });
  const subNav = document.getElementById(`sub-${section}`);
  if (subNav) {
    subNav.classList.add('active');
    document.body.classList.add('has-subnav');
  }
}

// Auto-detect current section and page on load
(function() {
  const currentPage = window.location.pathname.split('/').pop() || 'index.html';
  const pageMap = {
    'acl-manager.html':           { section: 'storage',   page: 'acl-manager' },
    'audit.html':                 { section: 'security',  page: 'audit' },
    'certificates.html':          { section: 'security',  page: 'certificates' },
    'cloud-sync.html':            { section: 'storage',   page: 'cloud-sync' },
    'iscsi.html':                 { section: 'storage',   page: 'iscsi' },
    'directory-service.html':     { section: 'identity',  page: 'directory-service' },
    'docker.html':                { section: 'compute',   page: 'docker' },
    'docker-containers-ui.html':  { section: 'compute',   page: 'docker' },
    'docker-pull-ui.html':        { section: 'compute',   page: 'docker' },
    'files-enhanced.html':        { section: 'storage',   page: 'files-enhanced' },
    'files.html':                 { section: 'storage',   page: 'files' },
    'firewall.html':              { section: 'security',  page: 'firewall' },
    'groups.html':                { section: 'identity',  page: 'groups' },
    'hardware.html':              { section: 'system',    page: 'hardware' },
    'index.html':                 { section: 'dashboard', page: null },
    'ipmi.html':                  { section: 'system',    page: 'ipmi' },
    'logs.html':                  { section: 'system',    page: 'logs' },
    'modules.html':               { section: 'compute',   page: 'modules' },
    'network-dns.html':           { section: 'network',   page: 'network-dns' },
    'network-interfaces.html':    { section: 'network',   page: 'network-interfaces' },
    'network-routing.html':       { section: 'network',   page: 'network-routing' },
    'network.html':               { section: 'network',   page: 'network' },
    'pools-advanced.html':        { section: 'storage',   page: 'pools-advanced' },
    'pools.html':                 { section: 'storage',   page: 'pools' },
    'power-management.html':      { section: 'system',    page: 'power-management' },
    'quotas.html':                { section: 'storage',   page: 'quotas' },
    'rbac-management.html':       { section: 'identity',  page: 'rbac-management' },
    'removable-media-ui.html':    { section: 'system',    page: 'removable-media-ui' },
    'ha-cluster.html':            { section: 'system',    page: 'ha-cluster' },
    'alerts.html':                { section: 'system',    page: 'alerts' },
    'support.html':               { section: 'system',    page: 'support' },
    'system-updates.html':         { section: 'system',    page: 'system-updates' },
    'replication.html':           { section: 'storage',   page: 'replication' },
    'reporting.html':             { section: 'system',    page: 'reporting' },
    'security.html':              { section: 'security',  page: 'security' },
    'settings.html':              { section: 'system',    page: 'settings' },
    'shares.html':                { section: 'storage',   page: 'shares' },
    'snapshot-scheduler.html':    { section: 'storage',   page: 'snapshot-scheduler' },
    'system-monitoring.html':     { section: 'system',    page: 'system-monitoring' },
    'system-settings.html':       { section: 'system',    page: 'system-settings' },
    'ups.html':                   { section: 'system',    page: 'ups' },
    'users.html':                 { section: 'identity',  page: 'users' },
    'zfs-encryption.html':        { section: 'storage',   page: 'zfs-encryption' }
  };
  const current = pageMap[currentPage];
  if (current) {
    const sectionBtn = document.querySelector(`[data-section="${current.section}"]`);
    if (sectionBtn) sectionBtn.classList.add('active');
    if (current.section !== 'dashboard') {
      const subNav = document.getElementById(`sub-${current.section}`);
      if (subNav) {
        subNav.classList.add('active');
        document.body.classList.add('has-subnav');
        if (current.page) {
          const pageLink = subNav.querySelector(`[data-page="${current.page}"]`);
          if (pageLink) pageLink.classList.add('active');
        }
      }
    }
  }
})();

// Keyboard shortcuts
function showKeyboardHelp() {
  if (window.EnhancedUI) {
    const shortcuts = `Keyboard Shortcuts:\n\ng + d → Dashboard\ng + s → Storage\ng + c → Compute\ng + n → Network\ng + u → Identity\ng + e → Security\ng + y → System\n\nr → Refresh page\nEsc → Close modals\n? → Show this help`;
    EnhancedUI.toast(shortcuts, 'info', 8000);
  }
}

// Enhanced keyboard shortcuts (registered once)
(function() {
  if (window.__navSharedKeydownRegistered) return;
  window.__navSharedKeydownRegistered = true;
  let keySequence = '';
  let keyTimeout;
  document.addEventListener('keydown', (e) => {
    if (['INPUT', 'TEXTAREA', 'SELECT'].includes(e.target.tagName)) return;
    keySequence += e.key;
    clearTimeout(keyTimeout);
    const shortcuts = {
      'gd': 'index.html',
      'gs': () => toggleSubNav('storage',   document.querySelector('[data-section="storage"]')),
      'gc': () => toggleSubNav('compute',   document.querySelector('[data-section="compute"]')),
      'gn': () => toggleSubNav('network',   document.querySelector('[data-section="network"]')),
      'gu': () => toggleSubNav('identity',  document.querySelector('[data-section="identity"]')),
      'ge': () => toggleSubNav('security',  document.querySelector('[data-section="security"]')),
      'gy': () => toggleSubNav('system',    document.querySelector('[data-section="system"]'))
    };
    if (shortcuts[keySequence]) {
      e.preventDefault();
      const action = shortcuts[keySequence];
      if (typeof action === 'function') action();
      else window.location.href = action;
      keySequence = '';
    } else if (e.key === '?') {
      e.preventDefault();
      showKeyboardHelp();
    }
    keyTimeout = setTimeout(() => keySequence = '', 1000);
  });
})();

// Connection status monitoring (registered once)
(function() {
  if (window.__navSharedCMRegistered) return;
  window.__navSharedCMRegistered = true;
  if (window.ConnectionMonitor) {
    window.ConnectionMonitor.on('statusChange', (online) => {
      const status = document.getElementById('navConnectionStatus');
      if (!status) return;
      if (online) {
        status.classList.remove('offline');
        status.classList.add('online');
        status.innerHTML = '<span class="status-dot"></span><span>Connected</span>';
      } else {
        status.classList.remove('online');
        status.classList.add('offline');
        status.innerHTML = '<span class="status-dot"></span><span>Offline</span>';
      }
    });
  }
})();

// Populate nav version badge from /health (no auth required, cached across pages)
(function() {
  if (window.__navVersionFetched) return;
  window.__navVersionFetched = true;
  fetch('/health').then(r => r.json()).then(d => {
    const el = document.getElementById('nav-version-badge');
    if (el && d.version) el.textContent = 'v' + d.version;
  }).catch(() => {});
})();

// Lazy-load system health banner after nav is ready (avoids auth timing issues)
(function() {
  if (window.__healthBannerLoaded) return;
  window.__healthBannerLoaded = true;
  // Only load on authenticated pages (not login/setup-wizard)
  const path = window.location.pathname;
  if (path.includes('login') || path.includes('setup-wizard')) return;
  const s = document.createElement('script');
  s.src = '/assets/js/system-health-banner.js';
  document.head.appendChild(s);
})();
