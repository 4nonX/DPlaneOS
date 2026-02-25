/**
 * D-PlaneOS Sidebar Component
 * Reusable navigation for all pages
 */

(function() {
    const navigation = [
        { id: 'dashboard', label: 'Dashboard', icon: '📊', href: '/index.html' },
        { id: 'storage', label: 'Storage', icon: '💾', href: '/storage.html' },
        { id: 'docker', label: 'Docker', icon: '🐋', href: '/docker.html' },
        { id: 'files', label: 'Files', icon: '📁', href: '/files.html' },
        { id: 'shares', label: 'Shares', icon: '🔗', href: '/shares.html' },
        { id: 'backup', label: 'Backup', icon: '💼', href: '/backup.html' },
        { id: 'users', label: 'Users', icon: '👥', href: '/users.html' },
        { id: 'network', label: 'Network', icon: '📡', href: '/network.html' },
        { id: 'customize', label: 'Customize', icon: '🎨', href: '/customize.html' },
        { id: 'settings', label: 'Settings', icon: '⚙️', href: '/settings.html' }
    ];

    const sidebarHTML = `
        <aside class="sidebar" style="width: 280px; height: 100vh; display: flex; flex-direction: column;">
            <!-- Logo -->
            <div style="height: 64px; display: flex; align-items: center; justify-content: space-between; padding: 0 16px; border-bottom: 1px solid var(--color-border);">
                <div style="display: flex; align-items: center; gap: 12px;">
                    <div style="width: 40px; height: 40px; border-radius: 12px; background: var(--gradient-primary); display: flex; align-items: center; justify-content: center; box-shadow: 0 4px 12px rgba(124, 58, 237, 0.3);">
                        <span style="color: white; font-weight: bold; font-size: 18px;">D</span>
                    </div>
                    <div>
                        <div class="font-bold" style="font-size: 18px; color: var(--color-text-primary);">D-PlaneOS</div>
                        <div style="font-size: 12px; color: var(--color-text-muted);">v1.14.0 FINAL</div>
                    </div>
                </div>
            </div>

            <!-- Navigation -->
            <nav style="flex: 1; overflow-y: auto; padding: 12px; display: flex; flex-direction: column; gap: 4px;" class="scrollbar-thin">
                ${navigation.map(item => `
                    <a href="${item.href}" class="sidebar-item" data-page="${item.id}">
                        <span style="font-size: 20px;">${item.icon}</span>
                        <span style="font-weight: 500;">${item.label}</span>
                    </a>
                `).join('')}
            </nav>

            <!-- User Profile -->
            <div style="height: 64px; border-top: 1px solid var(--color-border); padding: 12px;">
                <div style="display: flex; align-items: center; gap: 12px;">
                    <div style="width: 40px; height: 40px; border-radius: 50%; background: var(--gradient-primary); display: flex; align-items: center; justify-content: center; box-shadow: 0 4px 12px rgba(124, 58, 237, 0.3);">
                        <span style="color: white; font-weight: 600; font-size: 14px;">A</span>
                    </div>
                    <div style="flex: 1; min-width: 0;">
                        <div style="font-size: 14px; font-weight: 500; color: var(--color-text-primary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis;">
                            admin
                        </div>
                        <div style="font-size: 12px; color: var(--color-text-muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis;">
                            Administrator
                        </div>
                    </div>
                </div>
            </div>
        </aside>
    `;

    // Inject sidebar
    document.addEventListener('DOMContentLoaded', function() {
        const container = document.getElementById('sidebar-container');
        if (container) {
            container.innerHTML = sidebarHTML;
            
            // Highlight active page
            const currentPage = window.location.pathname.split('/').pop().replace('.html', '') || 'index';
            const activePage = currentPage === 'index' ? 'dashboard' : currentPage;
            const activeItem = document.querySelector(`[data-page="${activePage}"]`);
            if (activeItem) {
                activeItem.classList.add('sidebar-item-active');
            }
        }
    });
})();
