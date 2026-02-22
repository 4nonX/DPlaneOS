// D-PlaneOS Navigation System
// Dynamically builds navigation menu based on user permissions

class Navigation {
    constructor() {
        this.menuItems = [
            {
                id: 'dashboard',
                label: 'Dashboard',
                icon: 'dashboard',
                url: '/index.html',
                permission: null // No permission required
            },
            {
                id: 'storage',
                label: 'Storage',
                icon: 'storage',
                permission: 'storage:read',
                children: [
                    {
                        id: 'pools',
                        label: 'Pools & Datasets',
                        url: '/pages/storage.html',
                        permission: 'storage:read'
                    },
                    {
                        id: 'disks',
                        label: 'Disks',
                        url: '/pages/disks.html',
                        permission: 'storage:read'
                    },
                    {
                        id: 'snapshots',
                        label: 'Snapshots',
                        url: '/pages/snapshots.html',
                        permission: 'storage:read'
                    }
                ]
            },
            {
                id: 'files',
                label: 'Files',
                icon: 'folder',
                url: '/pages/files.html',
                permission: 'files:read'
            },
            {
                id: 'docker',
                label: 'Containers',
                icon: 'widgets',
                permission: 'docker:read',
                children: [
                    {
                        id: 'containers',
                        label: 'Containers',
                        url: '/pages/containers.html',
                        permission: 'docker:read'
                    },
                    {
                        id: 'images',
                        label: 'Images',
                        url: '/pages/images.html',
                        permission: 'docker:read'
                    }
                ]
            },
            {
                id: 'monitoring',
                label: 'Monitoring',
                icon: 'monitor_heart',
                url: '/pages/monitoring.html',
                permission: 'monitoring:read'
            },
            {
                id: 'settings',
                label: 'Settings',
                icon: 'settings',
                permission: null, // Show to all
                children: [
                    {
                        id: 'system',
                        label: 'System Settings',
                        url: '/pages/system-settings.html',
                        permission: 'system:read'
                    },
                    {
                        id: 'network',
                        label: 'Network',
                        url: '/pages/network.html',
                        permission: 'network:read'
                    },
                    {
                        id: 'users',
                        label: 'Users',
                        url: '/pages/users.html',
                        permission: 'users:read'
                    },
                    {
                        id: 'rbac',
                        label: 'Roles & Permissions',
                        url: '/pages/rbac-management.html',
                        permission: 'roles:read'
                    }
                ]
            }
        ];
    }

    // Build navigation HTML
    async build() {
        // Wait for permissions to load
        if (window.permissions && !window.permissions.isEnabled()) {
            await window.permissions.load();
        }

        const nav = document.getElementById('main-navigation');
        if (!nav) return;

        nav.innerHTML = this.menuItems
            .filter(item => this.hasAccess(item))
            .map(item => this.renderItem(item))
            .join('');

        // Add click handlers
        this.attachHandlers();
    }

    // Check if user has access to menu item
    hasAccess(item) {
        if (!item.permission) return true;
        
        const [resource, action] = item.permission.split(':');
        return window.permissions.can(resource, action);
    }

    // Render menu item
    renderItem(item) {
        const hasChildren = item.children && item.children.length > 0;
        const currentPath = window.location.pathname;
        
        if (hasChildren) {
            // Render parent with children
            const visibleChildren = item.children.filter(child => this.hasAccess(child));
            
            if (visibleChildren.length === 0) return '';

            return `
                <div class="nav-item parent">
                    <div class="nav-link" data-toggle="submenu" data-id="${item.id}">
                        <span class="material-icons">${item.icon}</span>
                        <span class="nav-label">${item.label}</span>
                        <span class="material-icons expand-icon">expand_more</span>
                    </div>
                    <div class="nav-submenu" id="submenu-${item.id}">
                        ${visibleChildren.map(child => `
                            <a href="${child.url}" class="nav-link submenu-link ${currentPath.endsWith(child.url) ? 'active' : ''}">
                                <span class="nav-label">${child.label}</span>
                            </a>
                        `).join('')}
                    </div>
                </div>
            `;
        } else {
            // Render simple link
            return `
                <a href="${item.url}" class="nav-item nav-link ${currentPath.endsWith(item.url) ? 'active' : ''}">
                    <span class="material-icons">${item.icon}</span>
                    <span class="nav-label">${item.label}</span>
                </a>
            `;
        }
    }

    // Attach event handlers
    attachHandlers() {
        document.querySelectorAll('[data-toggle="submenu"]').forEach(toggle => {
            toggle.addEventListener('click', (e) => {
                const submenuId = 'submenu-' + toggle.dataset.id;
                const submenu = document.getElementById(submenuId);
                const expandIcon = toggle.querySelector('.expand-icon');
                
                if (submenu) {
                    submenu.classList.toggle('open');
                    expandIcon.textContent = submenu.classList.contains('open') ? 'expand_less' : 'expand_more';
                }
            });
        });
    }
}

// Initialize navigation when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
    const nav = new Navigation();
    
    // Wait for permissions to load
    window.addEventListener('permissions-loaded', () => {
        nav.build();
    });
});
