/**
 * D-PlaneOS Hybrid Router
 * 
 * Turns multi-page navigation into SPA-like instant transitions
 * for sub-nav links within the same section.
 *
 * How it works:
 *   1. Intercepts clicks on .sub-link elements
 *   2. Fetches the target page via fetch()
 *   3. Extracts .main content from the response
 *   4. Swaps it into the current page with fadeIn
 *   5. Executes inline <script> blocks from the fetched page
 *   6. Updates URL via history.pushState
 *
 * Fallback: If fetch fails or JS is off, normal <a href> works.
 * No changes to existing HTML required.
 */

(function () {
  'use strict';

  // Don't run on browsers without fetch/pushState
  if (!window.fetch || !window.history.pushState) return;

  var TRANSITION_MS = 200;
  var activeFetch = null; // Cancel stale fetches

  /**
   * Swap .main content with fade transition
   */
  function swapContent(newHTML, scripts, url) {
    var main = document.querySelector('.main');
    if (!main) return false;

    // Fade out + micro-slide down
    main.style.transition = 'opacity ' + TRANSITION_MS + 'ms cubic-bezier(0.4, 0.0, 0.2, 1), transform ' + TRANSITION_MS + 'ms cubic-bezier(0.4, 0.0, 0.2, 1)';
    main.style.opacity = '0';
    main.style.transform = 'translateY(4px)';

    setTimeout(function () {
      // Replace content
      main.innerHTML = newHTML;

      // Fade in + slide back to origin
      main.style.opacity = '1';
      main.style.transform = 'translateY(0)';

      // Execute fetched page scripts (page-specific init code)
      scripts.forEach(function (scriptContent) {
        try {
          var s = document.createElement('script');
          s.textContent = scriptContent;
          document.body.appendChild(s);
          // Clean up — script has already executed
          document.body.removeChild(s);
        } catch (e) {
          console.warn('[Router] Script exec error:', e);
        }
      });

      // Update URL
      history.pushState({ path: url }, '', url);

      // Update active sub-link
      updateActiveSubLink(url);

      // Scroll to top
      window.scrollTo(0, 0);

      // Dispatch event so other scripts know content changed
      window.dispatchEvent(new CustomEvent('router:load', { detail: { url: url } }));
    }, TRANSITION_MS);

    return true;
  }

  /**
   * Update .sub-link active states after navigation
   */
  function updateActiveSubLink(url) {
    var page = url.split('/').pop();

    document.querySelectorAll('.nav-sub.active .sub-link').forEach(function (link) {
      var href = link.getAttribute('href') || '';
      if (href === page || href.endsWith('/' + page)) {
        link.classList.add('active');
      } else {
        link.classList.remove('active');
      }
    });
  }

  /**
   * Fetch a page and extract .main content + inline scripts
   */
  function loadPage(url) {
    // Abort previous in-flight fetch
    if (activeFetch) {
      activeFetch.aborted = true;
    }

    var thisRequest = { aborted: false };
    activeFetch = thisRequest;

    return fetch(url).then(function (response) {
      if (!response.ok) throw new Error(response.status);
      return response.text();
    }).then(function (html) {
      // If a newer fetch started, discard this one
      if (thisRequest.aborted) return null;

      // Parse the fetched HTML
      var parser = new DOMParser();
      var doc = parser.parseFromString(html, 'text/html');
      var newMain = doc.querySelector('.main');

      if (!newMain) throw new Error('No .main found in ' + url);

      // Collect inline scripts from inside .main and after it
      // These are page-specific init scripts (loadPools, loadContainers, etc.)
      var scripts = [];
      var allScripts = doc.querySelectorAll('script:not([src])');
      allScripts.forEach(function (s) {
        var text = s.textContent.trim();
        // Skip empty and skip the shared nav/toggleSubNav code
        if (text && text.indexOf('function toggleSubNav') === -1
                  && text.indexOf('function navigateToSection') === -1
                  && text.indexOf('new Navigation') === -1) {
          scripts.push(text);
        }
      });

      return {
        html: newMain.innerHTML,
        scripts: scripts
      };
    });
  }

  /**
   * Handle sub-link click
   */
  function handleClick(e) {
    var link = e.target.closest('.sub-link');
    if (!link) return;

    var href = link.getAttribute('href');
    if (!href || href.startsWith('http') || href.startsWith('#')) return;

    // Don't intercept if modifier keys are held (open in new tab)
    if (e.ctrlKey || e.metaKey || e.shiftKey) return;

    e.preventDefault();

    // Resolve relative URL
    var url = new URL(href, window.location.href).pathname;
    var page = url.split('/').pop();

    // Skip if already on this page
    if (window.location.pathname.endsWith(page)) return;

    // Show loading state
    link.style.opacity = '0.6';

    loadPage(href).then(function (result) {
      link.style.opacity = '';
      if (result) {
        swapContent(result.html, result.scripts, url);
      }
    }).catch(function (err) {
      // Fallback: normal navigation
      console.warn('[Router] Fetch failed, falling back:', err);
      link.style.opacity = '';
      window.location.href = href;
    });
  }

  /**
   * Handle browser back/forward
   */
  window.addEventListener('popstate', function (e) {
    var url = window.location.pathname;
    var page = url.split('/').pop();

    if (!page || page === '') {
      window.location.reload();
      return;
    }

    loadPage(page).then(function (result) {
      if (result) {
        var main = document.querySelector('.main');
        if (main) {
          main.innerHTML = result.html;
          result.scripts.forEach(function (scriptContent) {
            try {
              var s = document.createElement('script');
              s.textContent = scriptContent;
              document.body.appendChild(s);
              document.body.removeChild(s);
            } catch (e) { /* ignore */ }
          });
          updateActiveSubLink(url);
        }
      }
    }).catch(function () {
      window.location.reload();
    });
  });

  /**
   * Prefetch sub-links on hover for instant loading
   */
  var prefetchCache = {};

  function handlePrefetch(e) {
    var link = e.target.closest('.sub-link');
    if (!link) return;

    var href = link.getAttribute('href');
    if (!href || prefetchCache[href]) return;

    // Mark as prefetching to avoid duplicates
    prefetchCache[href] = true;

    // Low-priority prefetch
    var prefetchLink = document.createElement('link');
    prefetchLink.rel = 'prefetch';
    prefetchLink.href = href;
    document.head.appendChild(prefetchLink);
  }

  /**
   * Initialize — attach to all sub-nav containers
   */
  function init() {
    // Click handler on nav-sub (delegation)
    document.querySelectorAll('.nav-sub').forEach(function (sub) {
      sub.addEventListener('click', handleClick);
      sub.addEventListener('pointerenter', handlePrefetch, true);
    });

    // Also intercept top-nav section clicks that navigate (like Dashboard)
    // These remain normal page loads — only sub-links get the SPA treatment
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
