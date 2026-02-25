// Permission Checker Utility
// Provides frontend permission checking and UI element hiding/showing

class PermissionChecker {
    constructor() {
        this.permissions = new Map();
        this.roles = [];
        this.loaded = false;
    }

    // Load current user's permissions
    async load() {
        try {
            // Use csrfFetch so X-Session-ID + X-User headers are sent automatically
            const fetchFn = typeof csrfFetch === 'function' ? csrfFetch : fetch;
            const response = await fetchFn('/api/rbac/me/permissions');

            if (!response.ok) {
                throw new Error('Failed to load permissions');
            }

            const data = await response.json();
            
            // Store permissions in Map for fast lookup
            if (data.permissions) {
                data.permissions.forEach(perm => {
                    const key = `${perm.resource}:${perm.action}`;
                    this.permissions.set(key, true);
                });
            }

            // Also store the 'can' map if provided
            if (data.can) {
                Object.entries(data.can).forEach(([key, value]) => {
                    this.permissions.set(key, value);
                });
            }

            this.loaded = true;
            
            // Trigger permission-loaded event
            window.dispatchEvent(new CustomEvent('permissions-loaded', { 
                detail: { permissions: data.permissions || [] } 
            }));

            return data.permissions || [];

        } catch (error) {
            console.error('Failed to load permissions:', error);
            this.loaded = false;
            return [];
        }
    }

    // Check if user has a specific permission
    can(resource, action) {
        // Wildcard: if user has system:admin, they can do anything
        if (this.permissions.get('system:admin') === true) return true;
        if (this.permissions.get('*:*') === true) return true;
        const key = `${resource}:${action}`;
        return this.permissions.get(key) === true;
    }

    // Check if user has any of the permissions
    canAny(...permissions) {
        return permissions.some(perm => {
            const [resource, action] = perm.split(':');
            return this.can(resource, action);
        });
    }

    // Check if user has all of the permissions
    canAll(...permissions) {
        return permissions.every(perm => {
            const [resource, action] = perm.split(':');
            return this.can(resource, action);
        });
    }

    // Hide element if user doesn't have permission
    hideIfCannot(element, resource, action) {
        if (!this.can(resource, action)) {
            if (typeof element === 'string') element = document.querySelector(element);
            if (element) element.style.display = 'none';
        }
    }

    // Show element only if user has permission
    showIfCan(element, resource, action) {
        if (typeof element === 'string') element = document.querySelector(element);
        if (element) element.style.display = this.can(resource, action) ? '' : 'none';
    }

    // Disable element if user doesn't have permission
    disableIfCannot(element, resource, action) {
        if (!this.can(resource, action)) {
            if (typeof element === 'string') element = document.querySelector(element);
            if (element) {
                element.disabled = true;
                element.style.opacity = '0.5';
                element.style.cursor = 'not-allowed';
                element.title = `Requires ${resource}:${action} permission`;
            }
        }
    }

    // Apply permissions to all elements with data-permission attribute
    applyToPage() {
        document.querySelectorAll('[data-permission]').forEach(element => {
            const permission = element.getAttribute('data-permission');
            const [resource, action] = permission.split(':');
            
            if (!this.can(resource, action)) {
                const hideMode = element.getAttribute('data-permission-mode') || 'hide';
                if (hideMode === 'hide') {
                    element.style.display = 'none';
                } else if (hideMode === 'disable') {
                    element.disabled = true;
                    element.style.opacity = '0.5';
                    element.style.cursor = 'not-allowed';
                }
            }
        });

        document.querySelectorAll('[data-permission-any]').forEach(element => {
            const permissions = element.getAttribute('data-permission-any').split(',');
            if (!this.canAny(...permissions)) {
                const hideMode = element.getAttribute('data-permission-mode') || 'hide';
                if (hideMode === 'hide') element.style.display = 'none';
                else if (hideMode === 'disable') element.disabled = true;
            }
        });

        document.querySelectorAll('[data-permission-all]').forEach(element => {
            const permissions = element.getAttribute('data-permission-all').split(',');
            if (!this.canAll(...permissions)) {
                const hideMode = element.getAttribute('data-permission-mode') || 'hide';
                if (hideMode === 'hide') element.style.display = 'none';
                else if (hideMode === 'disable') element.disabled = true;
            }
        });
    }

    isEnabled() { return this.loaded; }
}

// Create global instance
window.permissions = new PermissionChecker();

// Auto-load permissions when DOM is ready
document.addEventListener('DOMContentLoaded', async () => {
    try {
        await window.permissions.load();
        window.permissions.applyToPage();
    } catch (error) {
        console.warn('Permission checking disabled:', error);
    }
});

// Provide helper functions for inline checks
function can(resource, action) { return window.permissions.can(resource, action); }
function canAny(...permissions) { return window.permissions.canAny(...permissions); }
function canAll(...permissions) { return window.permissions.canAll(...permissions); }
