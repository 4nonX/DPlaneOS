/**
 * D-PlaneOS v5.0.0 - Progressive Web App Service Worker
 * 
 * Features:
 * - Cache-first strategy for static assets
 * - Network-first strategy for API calls
 * - Offline fallback page
 * - Cache versioning and cleanup
 */

const CACHE_NAME = 'dplaneos-v5.0.0';
const CACHE_VERSION = '5.0.0';

// Assets to cache on install
const STATIC_ASSETS = [
  '/',
  '/pages/index.html',
  '/pages/login.html',
  '/assets/css/design-tokens.css',
  '/assets/css/m3-tokens.css',
  '/assets/css/m3-components.css',
  '/assets/css/enhanced-ui.css',
  '/assets/css/m3-animations.css',
  '/assets/js/core.js',
  '/assets/js/enhanced-ui.js',
  '/assets/js/connection-monitor.js',
  '/assets/js/keyboard-shortcuts.js',
  '/assets/js/form-validator.js',
  '/pages/index.html',
  '/pages/pools.html',
  '/pages/files.html',
  '/pages/docker.html',
  '/pages/users.html',
  '/pages/groups.html',
  '/pages/security.html',
  '/pages/settings.html',
  '/pages/ipmi.html',
  '/pages/cloud-sync.html'
];

// API endpoints (network-first)
const API_PATTERNS = [
  /\/api\//,
  /\/includes\//
];

// Install event - cache static assets
self.addEventListener('install', event => {
  console.log('[SW] Installing service worker...');
  
  event.waitUntil(
    caches.open(CACHE_NAME)
      .then(cache => {
        console.log('[SW] Caching static assets');
        return cache.addAll(STATIC_ASSETS.map(url => new Request(url, { cache: 'reload' })));
      })
      .then(() => {
        console.log('[SW] Static assets cached');
        return self.skipWaiting(); // Activate immediately
      })
      .catch(error => {
        console.error('[SW] Failed to cache assets:', error);
      })
  );
});

// Activate event - cleanup old caches
self.addEventListener('activate', event => {
  console.log('[SW] Activating service worker...');
  
  event.waitUntil(
    caches.keys()
      .then(cacheNames => {
        return Promise.all(
          cacheNames
            .filter(name => name.startsWith('dplaneos-') && name !== CACHE_NAME)
            .map(name => {
              console.log('[SW] Deleting old cache:', name);
              return caches.delete(name);
            })
        );
      })
      .then(() => {
        console.log('[SW] Service worker activated');
        return self.clients.claim(); // Take control immediately
      })
  );
});

// Fetch event - serve from cache or network
self.addEventListener('fetch', event => {
  const { request } = event;
  const url = new URL(request.url);
  
  // Skip non-GET requests
  if (request.method !== 'GET') {
    return;
  }
  
  // Skip cross-origin requests
  if (url.origin !== location.origin) {
    return;
  }
  
  // Check if it's an API call
  const isAPI = API_PATTERNS.some(pattern => pattern.test(url.pathname));
  
  if (isAPI) {
    // Network-first strategy for API calls
    event.respondWith(
      fetch(request)
        .then(response => {
          // Cache successful API responses
          if (response.status === 200) {
            const responseClone = response.clone();
            caches.open(CACHE_NAME).then(cache => {
              cache.put(request, responseClone);
            });
          }
          return response;
        })
        .catch(error => {
          console.log('[SW] Network failed, trying cache:', url.pathname);
          // Try cache on network failure
          return caches.match(request)
            .then(response => {
              if (response) {
                return response;
              }
              // Return offline response
              return new Response(
                JSON.stringify({
                  success: false,
                  error: 'Network unavailable',
                  offline: true
                }),
                {
                  status: 503,
                  headers: { 'Content-Type': 'application/json' }
                }
              );
            });
        })
    );
  } else {
    // Cache-first strategy for static assets
    event.respondWith(
      caches.match(request)
        .then(response => {
          if (response) {
            // Return cached version
            return response;
          }
          
          // Fetch from network and cache
          return fetch(request)
            .then(response => {
              // Don't cache non-successful responses
              if (!response || response.status !== 200 || response.type === 'error') {
                return response;
              }
              
              const responseClone = response.clone();
              caches.open(CACHE_NAME).then(cache => {
                cache.put(request, responseClone);
              });
              
              return response;
            })
            .catch(error => {
              console.error('[SW] Fetch failed:', url.pathname, error);
              
              // Return offline page for navigation requests
              if (request.mode === 'navigate') {
                return caches.match('/offline.html')
                  .then(response => {
                    if (response) {
                      return response;
                    }
                    // Fallback offline message
                    return new Response(
                      `<!DOCTYPE html>
                      <html>
                      <head>
                        <title>Offline - D-PlaneOS</title>
                        <style>
                          body { 
                            font-family: -apple-system, system-ui, sans-serif;
                            background: #0a0a0a;
                            color: #fff;
                            display: flex;
                            align-items: center;
                            justify-content: center;
                            height: 100vh;
                            margin: 0;
                            text-align: center;
                          }
                          h1 { font-size: 48px; margin-bottom: 16px; }
                          p { font-size: 18px; color: rgba(255,255,255,0.6); }
                        </style>
                      </head>
                      <body>
                        <div>
                          <h1>🔌 Offline</h1>
                          <p>D-PlaneOS is not available right now</p>
                          <p style="font-size: 14px; margin-top: 24px;">
                            Check your connection to the NAS
                          </p>
                        </div>
                      </body>
                      </html>`,
                      {
                        status: 503,
                        headers: { 'Content-Type': 'text/html' }
                      }
                    );
                  });
              }
              
              throw error;
            });
        })
    );
  }
});

// Message event - handle commands from clients
self.addEventListener('message', event => {
  const { type, data } = event.data;
  
  switch (type) {
    case 'SKIP_WAITING':
      self.skipWaiting();
      break;
      
    case 'CLEAR_CACHE':
      event.waitUntil(
        caches.delete(CACHE_NAME)
          .then(() => {
            event.ports[0].postMessage({ success: true });
          })
      );
      break;
      
    case 'GET_VERSION':
      event.ports[0].postMessage({ version: CACHE_VERSION });
      break;
      
    default:
      console.log('[SW] Unknown message type:', type);
  }
});

// Sync event - for background sync (future enhancement)
self.addEventListener('sync', event => {
  console.log('[SW] Background sync triggered:', event.tag);
  
  if (event.tag === 'sync-data') {
    event.waitUntil(
      // Implement background sync logic here
      Promise.resolve()
    );
  }
});

// Notification click event (future enhancement)
self.addEventListener('notificationclick', event => {
  console.log('[SW] Notification clicked:', event.notification.tag);
  event.notification.close();
  
  event.waitUntil(
    clients.openWindow('/')
  );
});

console.log('[SW] Service worker loaded - D-PlaneOS v' + CACHE_VERSION);
