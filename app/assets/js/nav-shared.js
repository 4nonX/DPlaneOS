/**
 * D-PlaneOS Shared Navigation Logic
 * Single source of truth for nav behavior used across all pages.
 *
 * Nav injection: pages with <div id="nav-root"></div> get the nav HTML
 * injected here instead of having 8KB of nav duplicated in every file.
 * Pages that still have inline nav-container HTML are unaffected (graceful).
 *
 * Nav structure (consolidated — redirect stubs map to their parent page):
 *   Storage:  ZFS Pools, Shares, NFS Exports, iSCSI, Replication, File Explorer, Cloud Sync
 *   Compute:  Docker, Git Sync, GitOps, Modules
 *   Network:  Network Settings (Interfaces/DNS/Routing are tabs inside)
 *   Identity: Users & Groups (Groups/RBAC are tabs inside), Directory Service
 *   Security: Security Settings (Firewall/Certs are tabs inside), Audit Logs
 *   System:   Settings, Updates, Reporting & Hardware, UPS, HA Cluster, Alerts, Support
 */

(function() {
  var root = document.getElementById('nav-root');
  if (!root) return; // page has its own nav HTML — skip

  root.innerHTML = '<nav class="nav-container">' +
  '<div class="nav-main">' +
    '<div style="display:flex;align-items:center;gap:16px;">' +
      '<div class="logo">D-PlaneOS<span class="version-badge" id="nav-version-badge"></span></div>' +
    '</div>' +
    '<div class="nav-links">' +
      '<button class="nav-link" onclick="navigateToSection(\'dashboard\')" data-section="dashboard">Dashboard</button>' +
      '<button class="nav-link" onclick="toggleSubNav(\'storage\',this)" data-section="storage">Storage</button>' +
      '<button class="nav-link" onclick="toggleSubNav(\'compute\',this)" data-section="compute">Compute</button>' +
      '<button class="nav-link" onclick="toggleSubNav(\'network\',this)" data-section="network">Network</button>' +
      '<button class="nav-link" onclick="toggleSubNav(\'identity\',this)" data-section="identity">Identity</button>' +
      '<button class="nav-link" onclick="toggleSubNav(\'security\',this)" data-section="security">Security</button>' +
      '<button class="nav-link" onclick="toggleSubNav(\'system\',this)" data-section="system">System</button>' +
    '</div>' +
    '<div class="nav-actions">' +
      '<button class="nav-action-btn" onclick="showKeyboardHelp()" title="Keyboard Shortcuts (?)">' +
        '<span class="material-symbols-rounded">keyboard</span>' +
      '</button>' +
      '<div class="connection-status online" id="navConnectionStatus">' +
        '<span class="status-dot"></span><span>Connected</span>' +
      '</div>' +
    '</div>' +
  '</div>' +

  // ── Storage ──────────────────────────────────────────────────
  '<div id="sub-storage" class="nav-sub"><div class="nav-sub-inner">' +
    '<a href="pools.html"      class="sub-link" data-page="pools"><span class="material-symbols-rounded">database</span> ZFS Pools</a>' +
    '<a href="shares.html"     class="sub-link" data-page="shares"><span class="material-symbols-rounded">folder_shared</span> Shares</a>' +
    '<a href="nfs.html"        class="sub-link" data-page="nfs"><span class="material-symbols-rounded">dns</span> NFS Exports</a>' +
    '<a href="iscsi.html"      class="sub-link" data-page="iscsi"><span class="material-symbols-rounded">storage</span> iSCSI Targets</a>' +
    '<a href="replication.html" class="sub-link" data-page="replication"><span class="material-symbols-rounded">sync</span> Replication</a>' +
    '<a href="files.html"      class="sub-link" data-page="files"><span class="material-symbols-rounded">folder_open</span> File Explorer</a>' +
    '<a href="cloud-sync.html" class="sub-link" data-page="cloud-sync"><span class="material-symbols-rounded">cloud_sync</span> Cloud Sync</a>' +
  '</div></div>' +

  // ── Compute ──────────────────────────────────────────────────
  '<div id="sub-compute" class="nav-sub"><div class="nav-sub-inner">' +
    '<a href="docker.html"   class="sub-link" data-page="docker"><span class="material-symbols-rounded">dns</span> Docker Containers</a>' +
    '<a href="git-sync.html" class="sub-link" data-page="git-sync"><span class="material-symbols-rounded">sync</span> Git Sync</a>' +
    '<a href="gitops.html"   class="sub-link" data-page="gitops"><span class="material-symbols-rounded">account_tree</span> GitOps State</a>' +
    '<a href="modules.html"  class="sub-link" data-page="modules"><span class="material-symbols-rounded">extension</span> App Modules</a>' +
  '</div></div>' +

  // ── Network ──────────────────────────────────────────────────
  // Interfaces, DNS, Routing are tabs inside network.html
  '<div id="sub-network" class="nav-sub"><div class="nav-sub-inner">' +
    '<a href="network.html" class="sub-link" data-page="network"><span class="material-symbols-rounded">lan</span> Network Settings</a>' +
  '</div></div>' +

  // ── Identity ─────────────────────────────────────────────────
  // Groups and RBAC are tabs inside users.html
  '<div id="sub-identity" class="nav-sub"><div class="nav-sub-inner">' +
    '<a href="users.html"            class="sub-link" data-page="users"><span class="material-symbols-rounded">person</span> Users &amp; Groups</a>' +
    '<a href="directory-service.html" class="sub-link" data-page="directory-service"><span class="material-symbols-rounded">domain</span> Directory Service</a>' +
  '</div></div>' +

  // ── Security ─────────────────────────────────────────────────
  // Firewall and Certificates are tabs inside security.html
  '<div id="sub-security" class="nav-sub"><div class="nav-sub-inner">' +
    '<a href="security.html" class="sub-link" data-page="security"><span class="material-symbols-rounded">shield</span> Security &amp; Firewall</a>' +
    '<a href="audit.html"    class="sub-link" data-page="audit"><span class="material-symbols-rounded">fact_check</span> Audit Logs</a>' +
  '</div></div>' +

  // ── System ───────────────────────────────────────────────────
  // Settings hub has: General, Config, Power, Webhooks tabs
  // Reporting hub has: Metrics, Live Health, Hardware, IPMI, Removable Media, Logs, Prometheus tabs
  '<div id="sub-system" class="nav-sub"><div class="nav-sub-inner">' +
    '<a href="settings.html"      class="sub-link" data-page="settings"><span class="material-symbols-rounded">tune</span> Settings</a>' +
    '<a href="system-updates.html" class="sub-link" data-page="system-updates"><span class="material-symbols-rounded">upgrade</span> System Updates</a>' +
    '<a href="reporting.html"     class="sub-link" data-page="reporting"><span class="material-symbols-rounded">monitoring</span> Reporting &amp; Hardware</a>' +
    '<a href="ups.html"           class="sub-link" data-page="ups"><span class="material-symbols-rounded">battery_charging_full</span> UPS Management</a>' +
    '<a href="ha-cluster.html"    class="sub-link" data-page="ha-cluster"><span class="material-symbols-rounded">device_hub</span> HA Cluster</a>' +
    '<a href="alerts.html"        class="sub-link" data-page="alerts"><span class="material-symbols-rounded">notifications_active</span> Alerts</a>' +
    '<a href="support.html"       class="sub-link" data-page="support"><span class="material-symbols-rounded">support_agent</span> Support &amp; Diagnostics</a>' +
  '</div></div>' +

  '</nav>';
})();

// ── Navigation behavior ──────────────────────────────────────────────────────

function navigateToSection(section) {
  if (section === 'dashboard') window.location.href = 'index.html';
}

function toggleSubNav(section, button) {
  document.querySelectorAll('.nav-link').forEach(function(l){ l.classList.remove('active'); });
  if (button) button.classList.add('active');
  document.querySelectorAll('.nav-sub').forEach(function(s){ s.classList.remove('active'); });
  var subNav = document.getElementById('sub-' + section);
  if (subNav) {
    subNav.classList.add('active');
    document.body.classList.add('has-subnav');
  }
}

// ── Auto-detect current section and highlight active page ────────────────────

(function() {
  var currentPage = window.location.pathname.split('/').pop() || 'index.html';

  // Maps every page (including redirect stubs) to its nav section and the
  // data-page value of the nav link that should be highlighted.
  // Redirect stubs point to their parent/hub page so the right nav item lights up.
  var pageMap = {
    // Storage
    'acl-manager.html':        { section: 'storage',   page: 'files' },        // → files.html#acls
    'cloud-sync.html':         { section: 'storage',   page: 'cloud-sync' },
    'files-enhanced.html':     { section: 'storage',   page: 'files' },        // → files.html#enhanced
    'files.html':              { section: 'storage',   page: 'files' },
    'iscsi.html':              { section: 'storage',   page: 'iscsi' },
    'nfs.html':                { section: 'storage',   page: 'nfs' },
    'pools-advanced.html':     { section: 'storage',   page: 'pools' },        // → pools.html#advanced
    'pools.html':              { section: 'storage',   page: 'pools' },
    'quotas.html':             { section: 'storage',   page: 'pools' },        // → pools.html#quotas
    'replication.html':        { section: 'storage',   page: 'replication' },
    'shares.html':             { section: 'storage',   page: 'shares' },
    'snapshot-scheduler.html': { section: 'storage',   page: 'pools' },        // → pools.html#scheduler
    'zfs-encryption.html':     { section: 'storage',   page: 'pools' },        // → pools.html#encryption

    // Compute
    'docker.html':              { section: 'compute',  page: 'docker' },
    'docker-containers-ui.html':{ section: 'compute',  page: 'docker' },
    'docker-pull-ui.html':      { section: 'compute',  page: 'docker' },
    'git-sync.html':            { section: 'compute',  page: 'git-sync' },
    'gitops.html':              { section: 'compute',  page: 'gitops' },
    'modules.html':             { section: 'compute',  page: 'modules' },

    // Network (stubs redirect to tabs inside network.html)
    'network.html':             { section: 'network',  page: 'network' },
    'network-dns.html':         { section: 'network',  page: 'network' },      // → network.html#dns
    'network-interfaces.html':  { section: 'network',  page: 'network' },      // → network.html#interfaces
    'network-routing.html':     { section: 'network',  page: 'network' },      // → network.html#routing

    // Identity (stubs redirect to tabs inside users.html)
    'directory-service.html':   { section: 'identity', page: 'directory-service' },
    'groups.html':              { section: 'identity', page: 'users' },        // → users.html#groups
    'rbac-management.html':     { section: 'identity', page: 'users' },        // → users.html#rbac
    'users.html':               { section: 'identity', page: 'users' },

    // Security (stubs redirect to tabs inside security.html)
    'audit.html':               { section: 'security', page: 'audit' },
    'certificates.html':        { section: 'security', page: 'security' },     // → security.html#certificates
    'firewall.html':            { section: 'security', page: 'security' },     // → security.html#firewall
    'security.html':            { section: 'security', page: 'security' },

    // System (stubs redirect to tabs inside settings.html or reporting.html)
    'alerts.html':              { section: 'system',   page: 'alerts' },
    'ha-cluster.html':          { section: 'system',   page: 'ha-cluster' },
    'hardware.html':            { section: 'system',   page: 'reporting' },    // → reporting.html#hardware
    'ipmi.html':                { section: 'system',   page: 'reporting' },    // → reporting.html#ipmi
    'logs.html':                { section: 'system',   page: 'reporting' },    // → reporting.html#logs
    'power-management.html':    { section: 'system',   page: 'settings' },     // → settings.html#power
    'removable-media-ui.html':  { section: 'system',   page: 'reporting' },    // → reporting.html#media
    'reporting.html':           { section: 'system',   page: 'reporting' },
    'settings.html':            { section: 'system',   page: 'settings' },
    'support.html':             { section: 'system',   page: 'support' },
    'system-monitoring.html':   { section: 'system',   page: 'reporting' },    // → reporting.html#monitoring
    'system-settings.html':     { section: 'system',   page: 'settings' },     // → settings.html#config
    'system-updates.html':      { section: 'system',   page: 'system-updates' },
    'ups.html':                 { section: 'system',   page: 'ups' },

    // Dashboard
    'index.html':               { section: 'dashboard', page: null },
    'dashboard.html':           { section: 'dashboard', page: null },
  };

  var current = pageMap[currentPage];
  if (!current) return;

  var sectionBtn = document.querySelector('[data-section="' + current.section + '"]');
  if (sectionBtn) sectionBtn.classList.add('active');

  if (current.section !== 'dashboard') {
    var subNav = document.getElementById('sub-' + current.section);
    if (subNav) {
      subNav.classList.add('active');
      document.body.classList.add('has-subnav');
      if (current.page) {
        var pageLink = subNav.querySelector('[data-page="' + current.page + '"]');
        if (pageLink) pageLink.classList.add('active');
      }
    }
  }
})();

// ── Keyboard shortcuts ───────────────────────────────────────────────────────

function showKeyboardHelp() {
  if (window.EnhancedUI) {
    EnhancedUI.toast(
      'Keyboard Shortcuts:\n\ng + d → Dashboard\ng + s → Storage\ng + c → Compute\ng + n → Network\ng + u → Identity\ng + e → Security\ng + y → System\n\nr → Refresh page\nEsc → Close modals\n? → Show this help',
      'info', 8000
    );
  }
}

(function() {
  if (window.__navSharedKeydownRegistered) return;
  window.__navSharedKeydownRegistered = true;
  var keySequence = '';
  var keyTimeout;
  document.addEventListener('keydown', function(e) {
    if (['INPUT','TEXTAREA','SELECT'].includes(e.target.tagName)) return;
    keySequence += e.key;
    clearTimeout(keyTimeout);
    var shortcuts = {
      'gd': 'index.html',
      'gs': function(){ toggleSubNav('storage',  document.querySelector('[data-section="storage"]')); },
      'gc': function(){ toggleSubNav('compute',  document.querySelector('[data-section="compute"]')); },
      'gn': function(){ toggleSubNav('network',  document.querySelector('[data-section="network"]')); },
      'gu': function(){ toggleSubNav('identity', document.querySelector('[data-section="identity"]')); },
      'ge': function(){ toggleSubNav('security', document.querySelector('[data-section="security"]')); },
      'gy': function(){ toggleSubNav('system',   document.querySelector('[data-section="system"]')); }
    };
    if (shortcuts[keySequence]) {
      e.preventDefault();
      var action = shortcuts[keySequence];
      if (typeof action === 'function') action(); else window.location.href = action;
      keySequence = '';
    } else if (e.key === '?') {
      e.preventDefault();
      showKeyboardHelp();
    }
    keyTimeout = setTimeout(function(){ keySequence = ''; }, 1000);
  });
})();

// ── Connection status monitoring ─────────────────────────────────────────────

(function() {
  if (window.__navSharedCMRegistered) return;
  window.__navSharedCMRegistered = true;
  if (window.ConnectionMonitor) {
    window.ConnectionMonitor.on('statusChange', function(online) {
      var status = document.getElementById('navConnectionStatus');
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

// ── Version badge ────────────────────────────────────────────────────────────

(function() {
  if (window.__navVersionFetched) return;
  window.__navVersionFetched = true;
  fetch('/health').then(function(r){ return r.json(); }).then(function(d){
    var el = document.getElementById('nav-version-badge');
    if (el && d.version) el.textContent = 'v' + d.version;
  }).catch(function(){});
})();

// ── System health banner (lazy, after nav) ───────────────────────────────────

(function() {
  if (window.__healthBannerLoaded) return;
  window.__healthBannerLoaded = true;
  var path = window.location.pathname;
  if (path.includes('login') || path.includes('setup-wizard')) return;
  var s = document.createElement('script');
  s.src = '/assets/js/system-health-banner.js';
  document.head.appendChild(s);
})();
