/**
 * D-PlaneOS Nav Flyout Enhancement
 * 
 * Adds hover-intent flyout behavior to the existing top-nav.
 * This is a NON-DESTRUCTIVE overlay — the existing click handlers
 * remain intact as fallback for touch devices and accessibility.
 *
 * Behavior:
 *   Desktop (pointer:fine) → hover opens sub-nav after 120ms intent delay
 *   Touch/Mobile           → existing click behavior (unchanged)
 *   Keyboard               → existing keyboard shortcuts (unchanged)
 *
 * No HTML changes required. No CSS changes required.
 * Drop-in enhancement for all 25 pages.
 */

(function () {
  'use strict';

  // Only enhance on devices with a fine pointer (mouse/trackpad)
  // Touch devices keep the existing click behavior
  const isDesktop = window.matchMedia('(pointer: fine)').matches;
  if (!isDesktop) return;

  let intentTimer = null;   // Delay before opening (prevents accidental triggers)
  let leaveTimer = null;    // Delay before closing (lets user move to sub-nav)
  let activeSection = null; // Currently hovered section ID

  const INTENT_DELAY = 120; // ms before opening on hover
  const LEAVE_DELAY = 280;  // ms grace period when moving between nav and sub-nav

  /**
   * Open a specific sub-nav (reuses existing DOM/CSS — no new elements)
   */
  function openSubNav(section, button) {
    clearTimeout(leaveTimer);

    if (activeSection === section) return; // Already open
    activeSection = section;

    // Remove active from all nav links
    document.querySelectorAll('.nav-link').forEach(function (link) {
      link.classList.remove('active');
    });

    // Activate the hovered button
    if (button) {
      button.classList.add('active');
    }

    // Hide all sub-navs
    document.querySelectorAll('.nav-sub').forEach(function (sub) {
      sub.classList.remove('active');
    });

    // Show the target sub-nav
    var subNav = document.getElementById('sub-' + section);
    if (subNav) {
      subNav.classList.add('active');
      document.body.classList.add('has-subnav');
    } else {
      // No sub-nav (e.g. Dashboard) — remove subnav spacing
      document.body.classList.remove('has-subnav');
    }
  }

  /**
   * Close all sub-navs (with grace period)
   */
  function scheduleClose() {
    clearTimeout(leaveTimer);
    leaveTimer = setTimeout(function () {
      // Only close if not hovering over any nav element
      // Check BEFORE nulling activeSection to prevent flicker on rapid re-entry
      var hovered = document.querySelectorAll('.nav-container:hover, .nav-sub:hover, .nav-link:hover');
      if (hovered.length > 0) return;

      activeSection = null;

      document.querySelectorAll('.nav-sub').forEach(function (sub) {
        sub.classList.remove('active');
      });

      // Re-activate the section for the current page
      restoreCurrentPage();
    }, LEAVE_DELAY);
  }

  /**
   * Restore nav state to match the current page URL
   * (so leaving the nav doesn't leave it in a random state)
   */
  function restoreCurrentPage() {
    var currentPage = window.location.pathname.split('/').pop() || 'index.html';

    var pageMap = {
      'acl-manager.html': 'storage',
      'audit.html': 'security',
      'certificates.html': 'security',
      'cloud-sync.html': 'storage',
      'directory-service.html': 'identity',
      'docker.html': 'compute',
      'files-enhanced.html': 'storage',
      'files.html': 'storage',
      'firewall.html': 'security',
      'groups.html': 'identity',
      'hardware.html': 'system',
      'index.html': 'dashboard',
      'ipmi.html': 'system',
      'logs.html': 'system',
      'modules.html': 'compute',
      'network-dns.html': 'network',
      'network-interfaces.html': 'network',
      'network-routing.html': 'network',
      'network.html': 'network',
      'pools-advanced.html': 'storage',
      'pools.html': 'storage',
      'power-management.html': 'system',
      'quotas.html': 'storage',
      'rbac-management.html': 'identity',
      'removable-media-ui.html': 'system',
      'replication.html': 'storage',
      'reporting.html': 'system',
      'security.html': 'security',
      'settings.html': 'system',
      'shares.html': 'storage',
      'snapshot-scheduler.html': 'storage',
      'system-monitoring.html': 'system',
      'system-settings.html': 'system',
      'ups.html': 'system',
      'users.html': 'identity',
      'zfs-encryption.html': 'storage'
    };

    var section = pageMap[currentPage] || 'dashboard';

    // Restore active nav-link
    document.querySelectorAll('.nav-link').forEach(function (link) {
      link.classList.remove('active');
    });
    var sectionBtn = document.querySelector('[data-section="' + section + '"]');
    if (sectionBtn) {
      sectionBtn.classList.add('active');
    }

    // Restore sub-nav if the current page has one
    if (section !== 'dashboard') {
      var subNav = document.getElementById('sub-' + section);
      if (subNav) {
        subNav.classList.add('active');
        document.body.classList.add('has-subnav');
      }
    } else {
      document.body.classList.remove('has-subnav');
    }

    activeSection = section;
  }

  /**
   * Attach hover handlers to all nav-links with sub-navs
   */
  function init() {
    var navLinks = document.querySelectorAll('.nav-link[data-section]');

    navLinks.forEach(function (link) {
      var section = link.getAttribute('data-section');

      // Hover intent: open after short delay
      link.addEventListener('pointerenter', function () {
        clearTimeout(intentTimer);
        clearTimeout(leaveTimer);
        intentTimer = setTimeout(function () {
          openSubNav(section, link);
        }, INTENT_DELAY);
      });

      // Cancel intent if pointer leaves before delay expires
      link.addEventListener('pointerleave', function () {
        clearTimeout(intentTimer);
        scheduleClose();
      });
    });

    // Keep sub-nav open while hovering over it
    document.querySelectorAll('.nav-sub').forEach(function (sub) {
      sub.addEventListener('pointerenter', function () {
        clearTimeout(leaveTimer);
      });
      sub.addEventListener('pointerleave', function () {
        scheduleClose();
      });
    });

    // Also keep alive when hovering over the main nav bar
    var navMain = document.querySelector('.nav-main');
    if (navMain) {
      navMain.addEventListener('pointerleave', function () {
        scheduleClose();
      });
    }
  }

  // Initialize when DOM is ready
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
