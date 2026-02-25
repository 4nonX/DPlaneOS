/**
 * D-PlaneOS Enhanced UI Components
 * Production-Ready Modal System with TrueNAS-style Confirmations
 * Version: 2.5.3
 */

// Global convenience functions
window.showToast = function(message, type = 'info', duration = 3000) {
    if (window.ui) {
        window.ui.toast(message, type, duration);
    } else {
        console.log(`[Toast ${type}] ${message}`);
    }
};

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
        this.activeModals = [];
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
    toast(message, type = 'info', duration = 5000) {
        // Ensure toast container exists (top-right, M3-compliant)
        let container = document.getElementById('dplane-toast-container');
        if (!container) {
            container = document.createElement('div');
            container.id = 'dplane-toast-container';
            container.style.cssText = [
                'position:fixed;top:88px;right:24px;z-index:var(--z-toast,10000);',
                'display:flex;flex-direction:column;gap:10px;max-width:420px;',
                'pointer-events:none;',
            ].join('');
            document.body.appendChild(container);
        }

        const icons = {
            success: 'check_circle',
            error:   'cancel',
            warning: 'warning',
            info:    'info',
        };
        const iconName = icons[type] || 'info';

        const toast = document.createElement('div');
        toast.className = `toast toast-${type}`;
        toast.style.cssText = [
            'pointer-events:all;',
            'display:flex;align-items:flex-start;gap:12px;',
            'padding:14px 16px;border-radius:var(--md-sys-shape-corner-medium,12px);',
            'background:rgba(30,41,59,0.98);',
            'border:1px solid rgba(255,255,255,0.10);',
            'backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);',
            'box-shadow:0 8px 32px rgba(0,0,0,0.4);',
            'transform:translateX(440px);opacity:0;',
            'transition:transform 0.3s cubic-bezier(0.4,0,0.2,1),opacity 0.3s ease;',
            'min-width:280px;',
        ].join('');

        // Left accent bar color per type
        const accentMap = {
            success: '#10B981',
            error:   '#EF4444',
            warning: '#F59E0B',
            info:    '#8a9cff',
        };
        const accent = accentMap[type] || '#8a9cff';
        toast.style.borderLeft = `4px solid ${accent}`;

        toast.innerHTML = `
            <span class="material-symbols-rounded" style="font-size:20px;flex-shrink:0;color:${accent};margin-top:1px;">${iconName}</span>
            <span style="flex:1;font-size:14px;line-height:1.5;color:rgba(255,255,255,0.9);">${message}</span>
            <button aria-label="Dismiss notification" onclick="this.closest('.toast').remove()" style="background:none;border:none;cursor:pointer;color:rgba(255,255,255,0.45);padding:0;margin:-2px -2px 0 4px;font-family:'Material Symbols Rounded';font-size:18px;line-height:1;flex-shrink:0;transition:color .15s;" onmouseenter="this.style.color='rgba(255,255,255,0.9)'" onmouseleave="this.style.color='rgba(255,255,255,0.45)'">close</button>
        `;

        container.appendChild(toast);

        // Animate in
        requestAnimationFrame(() => {
            requestAnimationFrame(() => {
                toast.style.transform = 'translateX(0)';
                toast.style.opacity = '1';
            });
        });

        // Auto-dismiss
        if (duration > 0) {
            const timer = setTimeout(() => this._dismissToast(toast), duration);
            // Pause auto-dismiss on hover
            toast.addEventListener('mouseenter', () => clearTimeout(timer));
            toast.addEventListener('mouseleave', () => {
                const remaining = setTimeout(() => this._dismissToast(toast), 2000);
                toast._autoTimer = remaining;
            });
            toast._autoTimer = timer;
        }

        return toast;
    }

    _dismissToast(toast) {
        if (!toast || !toast.parentNode) return;
        toast.style.transform = 'translateX(440px)';
        toast.style.opacity = '0';
        setTimeout(() => toast.remove(), 320);
    }
    
    /**
     * Base Modal System
     */
    async modal(title, content, buttons = [], options = {}) {
        return new Promise((resolve) => {
            const modal = document.createElement('div');
            modal.className = 'modal-overlay';
            
            const size = options.large ? 'modal-large' : (options.medium ? 'modal-medium' : '');
            
            modal.innerHTML = `
                <div class="modal-box ${size}">
                    <div class="modal-header">
                        <h3 class="modal-title">${title}</h3>
                        <button class="modal-close">
                            <span class="material-symbols-rounded">close</span>
                        </button>
                    </div>
                    <div class="modal-content">
                        ${content}
                    </div>
                    <div class="modal-actions">
                        ${buttons.map((btn, i) => `
                            <button class="btn ${btn.primary ? 'btn-primary' : ''} ${btn.danger ? 'btn-danger' : ''}" data-index="${i}" ${btn.disabled ? 'disabled' : ''}>
                                ${btn.icon ? `<span class="material-symbols-rounded" style="font-size:16px;">${btn.icon}</span>` : ''}
                                ${btn.text}
                            </button>
                        `).join('')}
                    </div>
                </div>
            `;
            
            document.body.appendChild(modal);
            this.activeModals.push(modal);
            
            // Animate in
            setTimeout(() => modal.classList.add('show'), 10);
            
            // Focus first input if exists
            setTimeout(() => {
                const firstInput = modal.querySelector('input, textarea, select');
                if (firstInput) firstInput.focus();
            }, 100);
            
            // Handle button clicks
            modal.querySelectorAll('.btn').forEach(btn => {
                btn.addEventListener('click', () => {
                    if (btn.disabled) return;
                    
                    const index = parseInt(btn.dataset.index);
                    const callback = buttons[index].callback;
                    
                    if (callback) {
                        const result = callback(modal);
                        if (result !== false) {
                            this.closeModal(modal, resolve, result !== undefined ? result : index);
                        }
                    } else {
                        this.closeModal(modal, resolve, index);
                    }
                });
            });
            
            // Handle close button
            modal.querySelector('.modal-close')?.addEventListener('click', () => {
                this.closeModal(modal, resolve, -1);
            });
            
            // Handle overlay click
            if (!options.noOverlayClose) {
                modal.addEventListener('click', (e) => {
                    if (e.target === modal) {
                        this.closeModal(modal, resolve, -1);
                    }
                });
            }
            
            // Handle ESC key
            const escHandler = (e) => {
                if (e.key === 'Escape' && !options.noEscape) {
                    this.closeModal(modal, resolve, -1);
                    document.removeEventListener('keydown', escHandler);
                }
            };
            document.addEventListener('keydown', escHandler);
        });
    }
    
    closeModal(modal, resolve, result) {
        modal.classList.remove('show');
        const index = this.activeModals.indexOf(modal);
        if (index > -1) {
            this.activeModals.splice(index, 1);
        }
        setTimeout(() => {
            modal.remove();
            resolve(result);
        }, 300);
    }
    
    /**
     * Simple Confirm Dialog
     */
    async confirm(title, message, confirmText = 'Confirm', cancelText = 'Cancel') {
        const result = await this.modal(title, `<p class="text-base">${message}</p>`, [
            { text: cancelText },
            { text: confirmText, primary: true }
        ]);
        return result === 1;
    }
    
    /**
     * ENHANCED: TrueNAS-Style Confirmation with Checkbox
     * User must check "I understand" before confirming dangerous actions
     */
    async confirmWithCheckbox(title, message, warningText, confirmText = 'Confirm', cancelText = 'Cancel') {
        const checkboxId = 'confirm-checkbox-' + Date.now();
        
        const content = `
            <div class="warning-box mb-16">
                <div class="flex gap-12">
                    <span class="material-symbols-rounded text-warning" style="font-size:24px;">warning</span>
                    <p class="text-base line-height-1-6">${message}</p>
                </div>
            </div>
            <div class="checkbox-container">
                <input type="checkbox" id="${checkboxId}" />
                <label for="${checkboxId}" class="checkbox-label-text text-muted">
                    ${warningText}
                </label>
            </div>
        `;
        
        const result = await this.modal(title, content, [
            { text: cancelText },
            { 
                text: confirmText, 
                primary: true,
                danger: true,
                disabled: true,
                callback: (modal) => {
                    const checkbox = modal.querySelector(`#${checkboxId}`);
                    return checkbox.checked;
                }
            }
        ], { noOverlayClose: true });
        
        // Enable/disable confirm button based on checkbox
        const modal = this.activeModals[this.activeModals.length - 1];
        if (modal) {
            const checkbox = modal.querySelector(`#${checkboxId}`);
            const confirmBtn = modal.querySelectorAll('.btn')[1];
            
            checkbox.addEventListener('change', () => {
                confirmBtn.disabled = !checkbox.checked;
                if (checkbox.checked) {
                    confirmBtn.classList.add('btn-danger');
                }
            });
        }
        
        return result === 1 || result === true;
    }
    
    /**
     * ENHANCED: Type-to-Confirm for Dangerous Actions
     * User must type exact confirmation text (like pool/dataset name)
     */
    async confirmTyping(title, message, confirmWord, placeholder = 'Type to confirm') {
        const inputId = 'confirm-input-' + Date.now();
        
        const content = `
            <div class="error-box mb-16">
                <div class="flex gap-12">
                    <span class="material-symbols-rounded" style="font-size:24px; color:#EF4444;">dangerous</span>
                    <div>
                        <p class="text-base line-height-1-6 mb-8">${message}</p>
                        <p class="text-sm text-muted">
                            Type <code class="code-inline">${confirmWord}</code> to confirm:
                        </p>
                    </div>
                </div>
            </div>
            <input 
                type="text" 
                id="${inputId}" 
                class="form-input" 
                placeholder="${placeholder}"
                autocomplete="off"
            />
        `;
        
        const result = await this.modal(title, content, [
            { text: 'Cancel' },
            { 
                text: 'Confirm Deletion', 
                primary: true,
                danger: true,
                disabled: true,
                callback: (modal) => {
                    const input = modal.querySelector(`#${inputId}`);
                    return input.value === confirmWord;
                }
            }
        ], { noOverlayClose: true, noEscape: true });
        
        // Enable/disable confirm button based on input
        const modal = this.activeModals[this.activeModals.length - 1];
        if (modal) {
            const input = modal.querySelector(`#${inputId}`);
            const confirmBtn = modal.querySelectorAll('.btn')[1];
            
            input.addEventListener('input', () => {
                confirmBtn.disabled = input.value !== confirmWord;
            });
        }
        
        return result === 1 || result === true;
    }
    
    /**
     * Prompt Dialog
     */
    async prompt(title, message, defaultValue = '', placeholder = '') {
        const inputId = 'prompt-input-' + Date.now();
        const content = `
            <p class="text-base mb-16">${message}</p>
            <input 
                type="text" 
                id="${inputId}"
                class="form-input" 
                value="${defaultValue}" 
                placeholder="${placeholder}"
            />
        `;
        
        const result = await this.modal(title, content, [
            { text: 'Cancel' },
            { text: 'OK', primary: true, callback: (modal) => {
                return modal.querySelector(`#${inputId}`).value;
            }}
        ]);
        
        return result === 1 ? null : result;
    }
    
    /**
     * Form Modal
     */
    async form(title, fields, submitText = 'Submit', cancelText = 'Cancel') {
        const content = `
            <form class="modal-form" onsubmit="return false;">
                ${fields.map(field => this.renderFormField(field)).join('')}
            </form>
        `;
        
        const result = await this.modal(title, content, [
            { text: cancelText },
            { text: submitText, primary: true, icon: 'check', callback: (modal) => {
                const form = modal.querySelector('.modal-form');
                const data = {};
                
                // Validate required fields
                let hasError = false;
                fields.forEach(field => {
                    const input = form.querySelector(`[name="${field.name}"]`);
                    const value = input.type === 'checkbox' ? input.checked : input.value;
                    
                    if (field.required && !value) {
                        hasError = true;
                        input.classList.add('error');
                    }
                    
                    data[field.name] = value;
                });
                
                if (hasError) {
                    this.toast('Please fill all required fields', 'error');
                    return false;
                }
                
                return data;
            }}
        ], { large: true });
        
        return result === 1 ? null : result;
    }
    
    renderFormField(field) {
        const required = field.required ? '<span style="color:#EF4444;">*</span>' : '';
        
        switch (field.type) {
            case 'select':
                return `
                    <div class="form-group">
                        <label class="form-label">${field.label}${required}</label>
                        <select name="${field.name}" class="form-input" ${field.required ? 'required' : ''}>
                            ${field.options.map(opt => `
                                <option value="${opt.value}" ${opt.selected ? 'selected' : ''}>
                                    ${opt.label}
                                </option>
                            `).join('')}
                        </select>
                        ${field.help ? `<small class="form-help">${field.help}</small>` : ''}
                    </div>
                `;
            
            case 'textarea':
                return `
                    <div class="form-group">
                        <label class="form-label">${field.label}${required}</label>
                        <textarea 
                            name="${field.name}" 
                            class="form-input" 
                            placeholder="${field.placeholder || ''}" 
                            rows="${field.rows || 5}"
                            ${field.required ? 'required' : ''}
                        >${field.value || ''}</textarea>
                        ${field.help ? `<small class="form-help">${field.help}</small>` : ''}
                    </div>
                `;
            
            case 'checkbox':
                return `
                    <div class="form-group">
                        <div class="checkbox-container">
                            <input type="checkbox" name="${field.name}" id="${field.name}" ${field.checked ? 'checked' : ''} />
                            <label for="${field.name}" class="checkbox-label-text">
                                ${field.label}${required}
                                ${field.help ? `<br><small class="form-help">${field.help}</small>` : ''}
                            </label>
                        </div>
                    </div>
                `;
            
            default:
                return `
                    <div class="form-group">
                        <label class="form-label">${field.label}${required}</label>
                        <input 
                            type="${field.type || 'text'}" 
                            name="${field.name}" 
                            class="form-input" 
                            value="${field.value || ''}" 
                            placeholder="${field.placeholder || ''}"
                            ${field.required ? 'required' : ''}
                        />
                        ${field.help ? `<small class="form-help">${field.help}</small>` : ''}
                    </div>
                `;
        }
    }
    
    /**
     * Loading State
     */
    showLoading(message = 'Loading...') {
        // Remove existing loader
        this.hideLoading();
        
        const loader = document.createElement('div');
        loader.className = 'loading-overlay';
        loader.id = 'global-loader';
        loader.innerHTML = `
            <div class="text-center">
                <div class="loading-spinner mb-16"></div>
                <p class="text-base text-muted">${message}</p>
            </div>
        `;
        
        document.body.appendChild(loader);
        setTimeout(() => loader.classList.add('show'), 10);
    }
    
    hideLoading() {
        const loader = document.getElementById('global-loader');
        if (loader) {
            loader.classList.remove('show');
            setTimeout(() => loader.remove(), 300);
        }
    }
    
    /**
     * Context Menu System
     */
    initContextMenu() {
        document.addEventListener('click', (e) => {
            const contextMenu = document.querySelector('.context-menu');
            if (contextMenu && !contextMenu.contains(e.target)) {
                contextMenu.remove();
            }
        });
    }
    
    showContextMenu(x, y, items) {
        // Remove existing context menu
        const existing = document.querySelector('.context-menu');
        if (existing) existing.remove();
        
        const menu = document.createElement('div');
        menu.className = 'context-menu';
        menu.innerHTML = items.map(item => {
            if (item.separator) {
                return '<div class="context-menu-separator"></div>';
            }
            return `
                <div class="context-menu-item" data-action="${item.action || ''}">
                    ${item.icon ? `<span class="material-symbols-rounded">${item.icon}</span>` : ''}
                    <span>${item.text}</span>
                </div>
            `;
        }).join('');
        
        document.body.appendChild(menu);
        
        // Position menu
        menu.style.left = x + 'px';
        menu.style.top = y + 'px';
        
        // Adjust if off-screen
        const rect = menu.getBoundingClientRect();
        if (rect.right > window.innerWidth) {
            menu.style.left = (x - rect.width) + 'px';
        }
        if (rect.bottom > window.innerHeight) {
            menu.style.top = (y - rect.height) + 'px';
        }
        
        // Handle clicks
        menu.querySelectorAll('.context-menu-item').forEach((item, index) => {
            item.addEventListener('click', () => {
                const action = items[index].action;
                if (action && typeof action === 'function') {
                    action();
                }
                menu.remove();
            });
        });
        
        setTimeout(() => menu.classList.add('show'), 10);
    }
    
    /**
     * Ripple Effect
     */
    initRipple() {
        document.addEventListener('click', (e) => {
            const btn = e.target.closest('.btn, .icon-btn');
            if (!btn) return;
            
            const ripple = document.createElement('span');
            ripple.className = 'ripple';
            
            const rect = btn.getBoundingClientRect();
            const size = Math.max(rect.width, rect.height);
            const x = e.clientX - rect.left - size / 2;
            const y = e.clientY - rect.top - size / 2;
            
            ripple.style.width = ripple.style.height = size + 'px';
            ripple.style.left = x + 'px';
            ripple.style.top = y + 'px';
            
            btn.appendChild(ripple);
            
            setTimeout(() => ripple.remove(), 600);
        });
    }
    
    /**
     * Keyboard Shortcuts
     */
    initKeyboard() {
        // ESC to close modals/menus handled in modal/context menu code
        // Page-specific shortcuts should be handled per-page
    }
}

// Initialize UI system
window.ui = new DPlaneUI();

// Export for module use if needed
if (typeof module !== 'undefined' && module.exports) {
    module.exports = DPlaneUI;
}
