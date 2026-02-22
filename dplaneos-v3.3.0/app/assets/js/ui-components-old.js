/**
 * D-PlaneOS UI Components
 * Modals, Context Menus, Toasts, Loading States
 */

// Global showToast function for convenience
window.showToast = function(message, type = 'info', duration = 3000) {
    if (window.ui) {
        window.ui.toast(message, type, duration);
    } else {
        // Fallback if UI not initialized yet
        console.log(`[Toast ${type}] ${message}`);
    }
};

// Global showLoading and hideLoading
window.showLoading = function(message = 'Loading...') {
    if (window.ui) {
        window.ui.showLoading(message);
    }
};

window.hideLoading = function() {
    if (window.ui) {
        window.ui.hideLoading();
    }
};

class DPlaneUI {
    constructor() {
        this.csrfToken = null;
        this.init();
    }
    
    init() {
        // Get CSRF token on load
        this.getCSRFToken();
        
        // Initialize ripple effects
        this.initRipple();
        
        // Initialize context menu system
        this.initContextMenu();
        
        // Initialize keyboard shortcuts
        this.initKeyboard();
    }
    
    /**
     * Get CSRF token from server
     */
    async getCSRFToken() {
        try {
            const response = await fetch('/api/csrf');
            const data = await response.json();
            this.csrfToken = data.token;
        } catch (error) {
            console.error('Failed to get CSRF token:', error);
        }
    }
    
    /**
     * Fetch with CSRF protection
     */
    async fetch(url, options = {}) {
        options.headers = options.headers || {};
        options.headers['X-CSRF-Token'] = this.csrfToken;
        options.credentials = 'same-origin';
        
        try {
            const response = await fetch(url, options);
            return response;
        } catch (error) {
            this.toast('Network error: ' + error.message, 'error');
            throw error;
        }
    }
    
    /**
     * Toast Notification System
     */
    toast(message, type = 'info', duration = 3000) {
        const toast = document.createElement('div');
        toast.className = `toast toast-${type}`;
        
        const icon = {
            'success': 'check_circle',
            'error': 'cancel',
            'warning': 'warning',
            'info': 'info'
        }[type] || 'info';
        
        toast.innerHTML = `
            <span class="material-symbols-rounded" style="font-size:20px;">${icon}</span>
            <span>${message}</span>
        `;
        
        document.body.appendChild(toast);
        
        // Animate in
        setTimeout(() => toast.classList.add('show'), 10);
        
        // Remove after duration
        setTimeout(() => {
            toast.classList.remove('show');
            setTimeout(() => toast.remove(), 300);
        }, duration);
    }
    
    /**
     * Modal System
     */
    modal(title, content, buttons = [], options = {}) {
        return new Promise((resolve) => {
            const modal = document.createElement('div');
            modal.className = 'modal-overlay';
            modal.innerHTML = `
                <div class="modal-container ${options.large ? 'modal-large' : ''}">
                    <div class="modal-header">
                        <h2>${title}</h2>
                        <button class="icon-btn modal-close">
                            <span class="material-symbols-rounded">close</span>
                        </button>
                    </div>
                    <div class="modal-content">
                        ${content}
                    </div>
                    <div class="modal-footer">
                        ${buttons.map((btn, i) => `
                            <button class="btn ${btn.primary ? 'btn-primary' : ''}" data-index="${i}">
                                ${btn.icon ? `<span class="material-symbols-rounded" style="font-size:16px;">${btn.icon}</span>` : ''}
                                ${btn.text}
                            </button>
                        `).join('')}
                    </div>
                </div>
            `;
            
            document.body.appendChild(modal);
            
            // Animate in
            setTimeout(() => modal.classList.add('show'), 10);
            
            // Handle button clicks
            modal.querySelectorAll('.btn').forEach(btn => {
                btn.addEventListener('click', () => {
                    const index = parseInt(btn.dataset.index);
                    const callback = buttons[index].callback;
                    
                    if (callback) {
                        const result = callback(modal);
                        if (result !== false) {
                            this.closeModal(modal, resolve, index);
                        }
                    } else {
                        this.closeModal(modal, resolve, index);
                    }
                });
            });
            
            // Handle close button
            modal.querySelector('.modal-close').addEventListener('click', () => {
                this.closeModal(modal, resolve, -1);
            });
            
            // Handle overlay click
            modal.addEventListener('click', (e) => {
                if (e.target === modal) {
                    this.closeModal(modal, resolve, -1);
                }
            });
            
            // Handle ESC key
            const escHandler = (e) => {
                if (e.key === 'Escape') {
                    this.closeModal(modal, resolve, -1);
                    document.removeEventListener('keydown', escHandler);
                }
            };
            document.addEventListener('keydown', escHandler);
        });
    }
    
    closeModal(modal, resolve, result) {
        modal.classList.remove('show');
        setTimeout(() => {
            modal.remove();
            resolve(result);
        }, 300);
    }
    
    /**
     * Confirm Dialog
     */
    async confirm(title, message, confirmText = 'Confirm', cancelText = 'Cancel') {
        const result = await this.modal(title, `<p>${message}</p>`, [
            { text: cancelText, callback: () => false },
            { text: confirmText, primary: true, callback: () => true }
        ]);
        return result === 1;
    }
    
    /**
     * Prompt Dialog
     */
    async prompt(title, message, defaultValue = '', placeholder = '') {
        const content = `
            <p>${message}</p>
            <input type="text" class="modal-input" value="${defaultValue}" placeholder="${placeholder}" />
        `;
        
        const result = await this.modal(title, content, [
            { text: 'Cancel', callback: () => null },
            { text: 'OK', primary: true, callback: (modal) => {
                return modal.querySelector('.modal-input').value;
            }}
        ]);
        
        return result === 1 ? null : result;
    }
    
    /**
     * Form Modal
     */
    async form(title, fields, submitText = 'Submit') {
        const content = `
            <form class="modal-form">
                ${fields.map(field => `
                    <div class="form-group">
                        <label>${field.label}</label>
                        ${this.renderFormField(field)}
                        ${field.help ? `<small>${field.help}</small>` : ''}
                    </div>
                `).join('')}
            </form>
        `;
        
        const result = await this.modal(title, content, [
            { text: 'Cancel' },
            { text: submitText, primary: true, icon: 'check', callback: (modal) => {
                const form = modal.querySelector('.modal-form');
                const data = {};
                fields.forEach(field => {
                    const input = form.querySelector(`[name="${field.name}"]`);
                    data[field.name] = input.value;
                });
                return data;
            }}
        ], { large: true });
        
        return result === 1 ? null : result;
    }
    
    renderFormField(field) {
        switch (field.type) {
            case 'select':
                return `
                    <select name="${field.name}" class="modal-input">
                        ${field.options.map(opt => `
                            <option value="${opt.value}" ${opt.selected ? 'selected' : ''}>
                                ${opt.label}
                            </option>
                        `).join('')}
                    </select>
                `;
            
            case 'textarea':
                return `
                    <textarea name="${field.name}" class="modal-input" 
                        placeholder="${field.placeholder || ''}" 
                        rows="${field.rows || 5}">${field.value || ''}</textarea>
                `;
            
            case 'checkbox':
                return `
                    <label class="checkbox-label">
                        <input type="checkbox" name="${field.name}" 
                            ${field.checked ? 'checked' : ''} />
                        <span>${field.text}</span>
                    </label>
                `;
            
            default:
                return `
                    <input type="${field.type || 'text'}" 
                        name="${field.name}" 
                        class="modal-input" 
                        placeholder="${field.placeholder || ''}" 
                        value="${field.value || ''}" 
                        ${field.required ? 'required' : ''} />
                `;
        }
    }
    
    /**
     * Context Menu System
     */
    initContextMenu() {
        document.addEventListener('contextmenu', (e) => {
            // Check if element has context menu
            const target = e.target.closest('[data-context-menu]');
            if (target) {
                e.preventDefault();
                const menuType = target.dataset.contextMenu;
                const menuData = JSON.parse(target.dataset.contextData || '{}');
                this.showContextMenu(e.clientX, e.clientY, menuType, menuData);
            }
        });
        
        // Close on click outside
        document.addEventListener('click', () => {
            this.closeContextMenu();
        });
    }
    
    showContextMenu(x, y, type, data) {
        this.closeContextMenu();
        
        const menu = document.createElement('div');
        menu.className = 'context-menu';
        menu.style.left = x + 'px';
        menu.style.top = y + 'px';
        
        const items = this.getContextMenuItems(type, data);
        menu.innerHTML = items.map(item => {
            if (item.divider) {
                return '<div class="context-menu-divider"></div>';
            }
            return `
                <div class="context-menu-item" data-action="${item.action}">
                    ${item.icon ? `<span class="material-symbols-rounded">${item.icon}</span>` : ''}
                    <span>${item.label}</span>
                    ${item.shortcut ? `<span class="context-menu-shortcut">${item.shortcut}</span>` : ''}
                </div>
            `;
        }).join('');
        
        document.body.appendChild(menu);
        
        // Adjust position if off screen
        const rect = menu.getBoundingClientRect();
        if (rect.right > window.innerWidth) {
            menu.style.left = (x - rect.width) + 'px';
        }
        if (rect.bottom > window.innerHeight) {
            menu.style.top = (y - rect.height) + 'px';
        }
        
        // Handle clicks
        menu.querySelectorAll('.context-menu-item').forEach(item => {
            item.addEventListener('click', () => {
                const action = item.dataset.action;
                this.handleContextAction(action, data);
                this.closeContextMenu();
            });
        });
        
        setTimeout(() => menu.classList.add('show'), 10);
    }
    
    closeContextMenu() {
        document.querySelectorAll('.context-menu').forEach(menu => menu.remove());
    }
    
    getContextMenuItems(type, data) {
        const menus = {
            'file': [
                { icon: 'folder_open', label: 'Open', action: 'open' },
                { icon: 'download', label: 'Download', action: 'download', shortcut: 'Ctrl+D' },
                { divider: true },
                { icon: 'edit', label: 'Rename', action: 'rename', shortcut: 'F2' },
                { icon: 'content_copy', label: 'Copy', action: 'copy', shortcut: 'Ctrl+C' },
                { icon: 'content_cut', label: 'Cut', action: 'cut', shortcut: 'Ctrl+X' },
                { divider: true },
                { icon: 'delete', label: 'Delete', action: 'delete', shortcut: 'Del' },
                { icon: 'info', label: 'Properties', action: 'properties' }
            ],
            'container': [
                { icon: 'play_arrow', label: 'Start', action: 'start' },
                { icon: 'stop', label: 'Stop', action: 'stop' },
                { icon: 'restart_alt', label: 'Restart', action: 'restart' },
                { divider: true },
                { icon: 'description', label: 'View Logs', action: 'logs' },
                { icon: 'terminal', label: 'Exec Shell', action: 'exec' },
                { divider: true },
                { icon: 'delete', label: 'Remove', action: 'remove' }
            ],
            'pool': [
                { icon: 'settings', label: 'Properties', action: 'properties' },
                { icon: 'cleaning_services', label: 'Scrub', action: 'scrub' },
                { icon: 'cloud_upload', label: 'Export', action: 'export' },
                { divider: true },
                { icon: 'delete', label: 'Destroy', action: 'destroy' }
            ]
        };
        
        return menus[type] || [];
    }
    
    handleContextAction(action, data) {
        // This will be overridden by page-specific handlers
        console.log('Context action:', action, data);
    }
    
    /**
     * Loading State
     */
    showLoading(text = 'Loading...') {
        const loading = document.createElement('div');
        loading.className = 'loading-overlay';
        loading.id = 'loading-overlay';
        loading.innerHTML = `
            <div class="loading-container">
                <div class="spinner"></div>
                <p>${text}</p>
            </div>
        `;
        document.body.appendChild(loading);
        setTimeout(() => loading.classList.add('show'), 10);
    }
    
    hideLoading() {
        const loading = document.getElementById('loading-overlay');
        if (loading) {
            loading.classList.remove('show');
            setTimeout(() => loading.remove(), 300);
        }
    }
    
    /**
     * Ripple Effect
     */
    initRipple() {
        document.addEventListener('click', (e) => {
            const rippleTarget = e.target.closest('.btn, .icon-btn, .fab, .ripple');
            if (rippleTarget && !rippleTarget.classList.contains('no-ripple')) {
                this.createRipple(e, rippleTarget);
            }
        });
    }
    
    createRipple(e, element) {
        const ripple = document.createElement('span');
        ripple.className = 'ripple-effect';
        
        const rect = element.getBoundingClientRect();
        const size = Math.max(rect.width, rect.height);
        const x = e.clientX - rect.left - size / 2;
        const y = e.clientY - rect.top - size / 2;
        
        ripple.style.width = ripple.style.height = size + 'px';
        ripple.style.left = x + 'px';
        ripple.style.top = y + 'px';
        
        element.appendChild(ripple);
        
        setTimeout(() => ripple.remove(), 600);
    }
    
    /**
     * Keyboard Shortcuts
     */
    initKeyboard() {
        document.addEventListener('keydown', (e) => {
            // Ctrl/Cmd + K - Global search
            if ((e.ctrlKey || e.metaKey) && e.key === 'k') {
                e.preventDefault();
                this.showGlobalSearch();
            }
        });
    }
    
    showGlobalSearch() {
        this.toast('Global search coming soon', 'info');
    }
}

// Initialize on load and make globally accessible
window.ui = new DPlaneUI();
