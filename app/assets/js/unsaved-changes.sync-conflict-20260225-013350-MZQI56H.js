/**
 * D-PlaneOS Unsaved-Changes Guard  v3.3.0
 *
 * Usage:
 *   const guard = UnsavedChanges.watch(containerEl);
 *   guard.markSaved();   // after successful save
 *   guard.isDirty();     // check state
 *   guard.destroy();     // stop watching
 *
 * v3.3.0 additions:
 *   - Intercepts in-page tab switches (switchConsolidatedTab) and warns
 *     before silently discarding dirty form state in the active tab.
 *   - Tab switch warning is a confirm dialog — user can cancel the switch
 *     and save first, or proceed and discard.
 */
(function (window) {
  'use strict';

  const _guards = new Set();
  let _banner = null;

  function _getBanner() {
    if (_banner) return _banner;
    _banner = document.createElement('div');
    _banner.id = 'unsaved-banner';
    _banner.style.cssText = [
      'display:none;position:fixed;bottom:0;left:0;right:0;',
      'z-index:var(--z-sticky,1100);',
      'background:rgba(20,20,20,0.96);',
      'border-top:1px solid rgba(245,158,11,0.4);',
      'backdrop-filter:blur(8px);-webkit-backdrop-filter:blur(8px);',
      'padding:12px 24px;',
      'align-items:center;gap:12px;',
      'font-size:13px;color:rgba(255,255,255,0.85);',
    ].join('');
    _banner.innerHTML =
      '<span style="font-family:\'Material Symbols Rounded\';font-size:18px;color:var(--warning,#ffca28);flex-shrink:0;">warning</span>' +
      '<span style="flex:1;">You have unsaved changes on this page.</span>' +
      '<button id="unsaved-banner-dismiss" aria-label="Dismiss" style="' +
        'background:none;border:1px solid rgba(255,255,255,0.15);border-radius:6px;' +
        'color:rgba(255,255,255,0.6);padding:4px 12px;font-size:12px;cursor:pointer;' +
        'transition:all .15s;' +
      '">Dismiss</button>';
    document.body.appendChild(_banner);
    document.getElementById('unsaved-banner-dismiss').addEventListener('click', () => {
      _banner.style.display = 'none';
    });
    return _banner;
  }

  function _sync() {
    const dirty = Array.from(_guards).some(g => g.isDirty());
    const b = _getBanner();
    b.style.display = dirty ? 'flex' : 'none';
  }

  // Block browser navigation when dirty
  window.addEventListener('beforeunload', e => {
    if (Array.from(_guards).some(g => g.isDirty())) {
      e.preventDefault();
      e.returnValue = 'You have unsaved changes. Leave anyway?';
    }
  });

  // ── In-page tab switch interception ─────────────────────────────────────────
  // Wrap switchConsolidatedTab once the page is ready.
  // Pages define this function inline so we patch it after DOMContentLoaded.
  function _installTabGuard() {
    if (!window.switchConsolidatedTab) return;
    if (window.switchConsolidatedTab._guarded) return; // already patched

    const _original = window.switchConsolidatedTab;
    window.switchConsolidatedTab = function(targetTab) {
      // Find the currently active tab content pane
      const activePane = document.querySelector('.consolidation-content.active');
      if (activePane) {
        // Check if any guard watches an element inside the active pane
        const paneIsDirty = Array.from(_guards).some(guard => {
          // We need to know which element is watched — expose it on the guard
          return guard._el && activePane.contains(guard._el) && guard.isDirty();
        });
        if (paneIsDirty) {
          // Use a simple confirm — ui.confirm may not be loaded here
          const proceed = window.confirm(
            'You have unsaved changes in this section.\n\nLeave without saving?'
          );
          if (!proceed) return; // cancel the tab switch
        }
      }
      _original(targetTab);
    };
    window.switchConsolidatedTab._guarded = true;
  }

  // Install after the page's own scripts have run
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => setTimeout(_installTabGuard, 0));
  } else {
    setTimeout(_installTabGuard, 0);
  }

  function watch(el, opts) {
    opts = Object.assign({ banner: true }, opts || {});
    if (opts.banner) _getBanner(); // pre-create

    let _dirty = false;

    function _set(dirty) { _dirty = dirty; _sync(); }
    function _onInput() { _set(true); }

    el.addEventListener('input', _onInput);
    el.addEventListener('change', _onInput);

    const guard = {
      _el:       el,             // exposed for tab-guard introspection
      isDirty:   () => _dirty,
      markSaved: () => _set(false),
      markDirty: () => _set(true),
      destroy: () => {
        el.removeEventListener('input', _onInput);
        el.removeEventListener('change', _onInput);
        _guards.delete(guard);
        _sync();
      },
    };
    _guards.add(guard);
    return guard;
  }

  window.UnsavedChanges = { watch };
})(window);
