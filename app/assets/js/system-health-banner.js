/**
 * D-PlaneOS System Health Banner  v3.3.0
 *
 * Polls /api/system/health every 60s and surfaces critical issues
 * via a dismissible M3-styled banner below the nav:
 *
 *   - Filesystem read-only (storage hardware failure)
 *   - NTP not synced (causes JWT login failures)
 *
 * Loaded automatically by nav-shared.js or included in head.
 */
(function (window) {
  'use strict';

  const POLL_INTERVAL = 60000; // 60s
  let _timer = null;
  let _dismissed = {}; // { key: timestamp } — suppress for session

  function _key(type) { return 'health_dismiss_' + type; }

  function _isDismissed(type) {
    return sessionStorage.getItem(_key(type)) === '1';
  }

  function _dismiss(type) {
    sessionStorage.setItem(_key(type), '1');
    const el = document.getElementById('health-banner-' + type);
    if (el) el.remove();
  }

  function _ensureContainer() {
    let c = document.getElementById('health-banners');
    if (!c) {
      c = document.createElement('div');
      c.id = 'health-banners';
      c.style.cssText = [
        'position:fixed;top:72px;left:0;right:0;z-index:var(--z-sticky,1100);',
        'pointer-events:none;',
      ].join('');
      document.body.appendChild(c);
    }
    return c;
  }

  function _showBanner(type, icon, color, message) {
    if (_isDismissed(type)) return;
    const existing = document.getElementById('health-banner-' + type);
    if (existing) return; // already shown

    const container = _ensureContainer();
    const banner = document.createElement('div');
    banner.id = 'health-banner-' + type;
    banner.style.cssText = [
      'pointer-events:all;',
      'display:flex;align-items:center;gap:12px;',
      'padding:10px 24px;',
      'background:rgba(20,20,20,0.97);',
      `border-bottom:2px solid ${color};`,
      'backdrop-filter:blur(8px);-webkit-backdrop-filter:blur(8px);',
      'font-size:13px;color:rgba(255,255,255,0.9);',
      'animation:slideDown .25s ease;',
    ].join('');

    banner.innerHTML = [
      `<span style="font-family:'Material Symbols Rounded';font-size:18px;color:${color};flex-shrink:0;">${icon}</span>`,
      `<span style="flex:1;">${message}</span>`,
      `<button aria-label="Dismiss" onclick="SystemHealthBanner.dismiss('${type}')" style="`,
        'background:none;border:1px solid rgba(255,255,255,0.15);border-radius:6px;',
        'color:rgba(255,255,255,0.5);padding:3px 10px;font-size:12px;cursor:pointer;',
        'pointer-events:all;transition:all .15s;',
      `'">Dismiss</button>`,
    ].join('');

    container.appendChild(banner);
  }

  function _removeBanner(type) {
    const el = document.getElementById('health-banner-' + type);
    if (el) el.remove();
  }

  async function _poll() {
    try {
      const r = await fetch('/api/system/health', { credentials: 'same-origin' });
      if (!r.ok) return; // not logged in yet, skip
      const d = await r.json();
      if (!d.success) return;

      // Read-only filesystem alert — this is an ERROR, not just a warning:
      // any Save action will silently fail while the filesystem is RO.
      if (d.filesystem_ro && d.ro_partitions && d.ro_partitions.length > 0) {
        const parts = d.ro_partitions.join(', ');
        _showBanner(
          'fs-ro',
          'drive_file_rename_outline',
          '#EF4444',
          `<strong>Storage Error — Filesystem is Read-Only:</strong> <code style="background:rgba(255,255,255,0.08);padding:1px 5px;border-radius:4px;">${parts}</code> — All save operations will silently fail until this is resolved. Open the Hardware page to check disk health.`
        );
      } else {
        _removeBanner('fs-ro');
      }

      // NTP not synced alert
      if (d.ntp_synced === false) {
        _showBanner(
          'ntp',
          'schedule',
          '#F59E0B',
          '<strong>Warning:</strong> System clock is not NTP-synchronized — login tokens may be rejected as expired until sync completes.'
        );
      } else {
        _removeBanner('ntp');
      }
    } catch (_) {
      // Network error during poll — don't show banners, connection-monitor handles this
    }
  }

  function start() {
    _poll(); // immediate first check
    if (_timer) clearInterval(_timer);
    _timer = setInterval(_poll, POLL_INTERVAL);
  }

  function stop() {
    if (_timer) { clearInterval(_timer); _timer = null; }
  }

  // Inject keyframe animation if not present
  if (!document.getElementById('health-banner-keyframes')) {
    const s = document.createElement('style');
    s.id = 'health-banner-keyframes';
    s.textContent = '@keyframes slideDown{from{transform:translateY(-100%);opacity:0}to{transform:translateY(0);opacity:1}}';
    document.head.appendChild(s);
  }

  // Auto-start once DOM is ready
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }

  window.SystemHealthBanner = { start, stop, dismiss: _dismiss };
})(window);
