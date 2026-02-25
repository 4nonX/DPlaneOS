/**
 * D-PlaneOS Core - API Bridge
 * Connects M3 Frontend with Backend
 */

(function() {
    'use strict';

    // CSRF Token Storage
    let csrfToken = null;

    // Fetch CSRF token from server
    async function fetchCSRFToken() {
        try {
            const response = await fetch('/api/csrf');
            const data = await response.json();
            if (data.success && data.csrf_token) {
                csrfToken = data.csrf_token;
                return csrfToken;
            }
        } catch (error) {
            console.error('Failed to fetch CSRF token:', error);
        }
        return null;
    }

    // Get CSRF token (fetch if not cached)
    async function getCSRFToken() {
        if (!csrfToken) {
            await fetchCSRFToken();
        }
        return csrfToken;
    }

    // Enhanced fetch with CSRF protection, loading states, and error handling
    window.csrfFetch = async function(url, options = {}) {
        const showLoading = options.showLoading !== false;
        const showError = options.showError !== false;
        
        if (showLoading && window.EnhancedUI) {
            EnhancedUI.showLoading(options.loadingText || 'Loading...');
        }
        
        const token = await getCSRFToken();
        
        options.headers = {
            'X-CSRF-Token': token || '',
        'X-Session-ID': sessionStorage.getItem('session_id') || '',
        'X-User': sessionStorage.getItem('username') || '',
            ...options.headers
        };

        // Handle JSON bodies
        if (options.body && typeof options.body === 'object' && !(options.body instanceof FormData)) {
            options.headers['Content-Type'] = 'application/json';
            options.body = JSON.stringify(options.body);
        }

        try {
            const response = await fetch(url, options);
            
            if (showLoading && window.EnhancedUI) {
                EnhancedUI.hideLoading();
            }
            
            // Handle auth errors
            if (response.status === 401) {
                if (showError && window.EnhancedUI) {
                    EnhancedUI.toast('Session expired. Please login again.', 'error');
                }
                setTimeout(() => window.location.href = '/pages/login.html', 1000);
                throw new Error('Authentication required');
            }
            
            // Handle permission errors
            if (response.status === 403) {
                if (showError && window.EnhancedUI) {
                    EnhancedUI.toast('Access denied. Insufficient permissions.', 'error');
                }
                throw new Error('Access denied');
            }
            
            // Handle server errors
            if (response.status >= 500) {
                if (showError && window.EnhancedUI) {
                    EnhancedUI.toast('Server error. Please try again later.', 'error');
                }
                throw new Error('Server error');
            }

            return response;
        } catch (error) {
            if (showLoading && window.EnhancedUI) {
                EnhancedUI.hideLoading();
            }
            
            if (showError && window.EnhancedUI && !error.message.includes('Authentication')) {
                EnhancedUI.toast(error.message || 'Network error. Please check your connection.', 'error');
            }
            
            console.error('API Error:', error);
            throw error;
        }
    };

    // Global API wrapper for convenience
    window.DPlane = {
        async api(endpoint, options = {}) {
            if (!endpoint.startsWith('/')) {
                endpoint = '/api/' + endpoint;
            }
            const response = await csrfFetch(endpoint, options);
            return await response.json();
        }
    };

    // Authentication check
    async function checkAuth() {
        const page = window.location.pathname;
        if (page.includes('login.html') || page.includes('setup-wizard.html') || page.includes('reset-password')) {
            return true;
        }

        try {
            const response = await fetch('/api/auth/check', {
                headers: {
                    'X-Session-ID': sessionStorage.getItem('session_id') || '',
                    'X-User': sessionStorage.getItem('username') || ''
                }
            });
            const data = await response.json();
            
            if (!data.authenticated) {
                window.location.href = '/pages/login.html';
                return false;
            }
            return true;
        } catch (error) {
            console.error('Auth check failed:', error);
            window.location.href = '/pages/login.html';
            return false;
        }
    }

    // Sidebar management
    function initSidebar() {
        const sidebar = document.getElementById('sidebar');
        if (!sidebar) return;

        // Restore state
        const isCollapsed = localStorage.getItem('sidebar-collapsed') === 'true';
        if (isCollapsed) sidebar.classList.add('collapsed');

        // Toggle handler
        const toggle = document.getElementById('sidebar-toggle');
        if (toggle) {
            toggle.addEventListener('click', () => {
                sidebar.classList.toggle('collapsed');
                localStorage.setItem('sidebar-collapsed', sidebar.classList.contains('collapsed'));
            });
        }

        // Set active page
        const currentPage = window.location.pathname.split('/').pop() || 'index.html';
        sidebar.querySelectorAll('.nav-link').forEach(link => {
            const href = link.getAttribute('href');
            if (href === currentPage || href === './' + currentPage) {
                link.classList.add('active');
            }
        });

        // Logout handler
        const logoutBtn = document.getElementById('logout-btn');
        if (logoutBtn) {
            logoutBtn.addEventListener('click', async (e) => {
                e.preventDefault();
                try {
                    await csrfFetch('/api/auth/logout', { method: 'POST' });
                    sessionStorage.clear();
                } catch (error) {
                    console.error('Logout error:', error);
                }
                window.location.href = '/pages/login.html';
            });
        }
    }

    // Global error handler
    window.handleError = function(error, context = '') {
        console.error(`Error${context ? ' in ' + context : ''}:`, error);
        if (window.showToast) {
            window.showToast(`Error: ${error.message}`, 'error');
        }
    };

    // Initialize on load
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', async () => {
            // Fetch CSRF token first
            await fetchCSRFToken();
            
            if (await checkAuth()) {
                initSidebar();
            }
        });
    } else {
        (async () => {
            // Fetch CSRF token first
            await fetchCSRFToken();
            
            if (await checkAuth()) {
                initSidebar();
            }
        })();
    }
})();
