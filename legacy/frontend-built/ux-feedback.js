/**
 * D-PlaneOS v1.14.0 - UX Feedback JavaScript
 * Toast notifications, loading states, and user interaction feedback
 */

// ==========================================
// TOAST NOTIFICATION SYSTEM
// ==========================================

class ToastManager {
    constructor() {
        this.container = null;
        this.init();
    }

    init() {
        // Create toast container if it doesn't exist
        if (!document.querySelector('.toast-container')) {
            this.container = document.createElement('div');
            this.container.className = 'toast-container';
            document.body.appendChild(this.container);
        } else {
            this.container = document.querySelector('.toast-container');
        }
    }

    show(message, type = 'info', duration = 5000) {
        const icons = {
            success: '✅',
            error: '❌',
            warning: '⚠️',
            info: 'ℹ️'
        };

        const toast = document.createElement('div');
        toast.className = `toast toast-${type}`;
        toast.innerHTML = `
            <div class="toast-icon">${icons[type]}</div>
            <div class="toast-content">
                <div class="toast-title">${this.getTitle(type)}</div>
                <div class="toast-message">${message}</div>
            </div>
            <button class="toast-close" onclick="this.parentElement.remove()">×</button>
        `;

        this.container.appendChild(toast);

        // Auto-remove after duration
        if (duration > 0) {
            setTimeout(() => {
                toast.style.animation = 'slideOut 0.3s ease';
                setTimeout(() => toast.remove(), 300);
            }, duration);
        }

        return toast;
    }

    getTitle(type) {
        const titles = {
            success: 'Success',
            error: 'Error',
            warning: 'Warning',
            info: 'Info'
        };
        return titles[type] || 'Notification';
    }

    success(message, duration) {
        return this.show(message, 'success', duration);
    }

    error(message, duration) {
        return this.show(message, 'error', duration);
    }

    warning(message, duration) {
        return this.show(message, 'warning', duration);
    }

    info(message, duration) {
        return this.show(message, 'info', duration);
    }

    clear() {
        if (this.container) {
            this.container.innerHTML = '';
        }
    }
}

// Global toast instance
const toast = new ToastManager();

// ==========================================
// LOADING STATE MANAGEMENT
// ==========================================

class LoadingManager {
    constructor() {
        this.overlay = null;
        this.init();
    }

    init() {
        // Create loading overlay
        this.overlay = document.createElement('div');
        this.overlay.className = 'loading-overlay';
        this.overlay.innerHTML = `
            <div style="text-align: center;">
                <div class="loading-spinner"></div>
                <div class="loading-text" id="loadingText">Loading...</div>
            </div>
        `;
        document.body.appendChild(this.overlay);
    }

    show(message = 'Loading...') {
        document.getElementById('loadingText').textContent = message;
        this.overlay.classList.add('active');
    }

    hide() {
        this.overlay.classList.remove('active');
    }

    // Button-specific loading
    setButtonLoading(button, loading = true) {
        if (loading) {
            button.disabled = true;
            button.dataset.originalText = button.innerHTML;
            button.classList.add('btn-loading');
        } else {
            button.disabled = false;
            button.classList.remove('btn-loading');
            if (button.dataset.originalText) {
                button.innerHTML = button.dataset.originalText;
            }
        }
    }
}

// Global loading instance
const loading = new LoadingManager();

// ==========================================
// BUTTON FEEDBACK
// ==========================================

// Add ripple effect to all buttons
document.addEventListener('click', function(e) {
    const button = e.target.closest('button, .btn');
    if (!button) return;

    // Add pressed class
    button.classList.add('pressed');
    setTimeout(() => button.classList.remove('pressed'), 200);

    // Haptic feedback on mobile
    if ('vibrate' in navigator) {
        navigator.vibrate(10);
    }
});

// ==========================================
// HOVER FEEDBACK
// ==========================================

// Add hover sound effect (optional, can be enabled)
let hoverSoundEnabled = false;

function playHoverSound() {
    if (!hoverSoundEnabled) return;
    // Subtle hover sound - can be implemented with Web Audio API
}

// Add hover feedback to interactive elements
document.addEventListener('DOMContentLoaded', function() {
    document.addEventListener('mouseover', function(e) {
        const interactive = e.target.closest(
            'button, .btn, a, .submenu-item, .nav-link, ' +
            '.app-card, .container-item, [onclick], [data-action]'
        );
        
        if (interactive && !interactive.disabled) {
            playHoverSound();
        }
    });
});

// ==========================================
// SELECTION MANAGEMENT
// ==========================================

class SelectionManager {
    constructor() {
        this.selected = new Set();
    }

    toggle(element, id) {
        if (this.selected.has(id)) {
            this.selected.delete(id);
            element.classList.remove('selected');
        } else {
            this.selected.add(id);
            element.classList.add('selected');
        }
        this.onSelectionChange();
    }

    selectAll(elements, ids) {
        elements.forEach((el, i) => {
            el.classList.add('selected');
            this.selected.add(ids[i]);
        });
        this.onSelectionChange();
    }

    deselectAll(elements) {
        elements.forEach(el => el.classList.remove('selected'));
        this.selected.clear();
        this.onSelectionChange();
    }

    getSelected() {
        return Array.from(this.selected);
    }

    onSelectionChange() {
        // Override this in implementations
        console.log('Selected items:', this.getSelected());
    }
}

// ==========================================
// CONFIRMATION DIALOGS
// ==========================================

function confirmAction(message, onConfirm, onCancel) {
    const confirmed = confirm(message);
    if (confirmed && onConfirm) {
        onConfirm();
    } else if (!confirmed && onCancel) {
        onCancel();
    }
    return confirmed;
}

// Better custom confirm dialog
function showConfirmDialog(options) {
    const {
        title = 'Confirm Action',
        message,
        confirmText = 'Confirm',
        cancelText = 'Cancel',
        onConfirm,
        onCancel,
        type = 'warning'
    } = options;

    // For now use native confirm, but this can be enhanced
    const result = confirm(`${title}\n\n${message}`);
    
    if (result && onConfirm) {
        onConfirm();
    } else if (!result && onCancel) {
        onCancel();
    }
    
    return result;
}

// ==========================================
// PROGRESS TRACKING
// ==========================================

function updateProgress(elementId, percent) {
    const progressBar = document.querySelector(`#${elementId} .progress-bar`);
    if (progressBar) {
        progressBar.style.width = percent + '%';
    }
}

// ==========================================
// FORM VALIDATION FEEDBACK
// ==========================================

function showFieldError(input, message) {
    input.classList.add('error');
    
    // Remove existing error message
    const existingError = input.parentElement.querySelector('.field-error');
    if (existingError) existingError.remove();
    
    // Add error message
    const error = document.createElement('div');
    error.className = 'field-error';
    error.style.color = 'var(--color-error)';
    error.style.fontSize = '13px';
    error.style.marginTop = '4px';
    error.textContent = message;
    input.parentElement.appendChild(error);
}

function clearFieldError(input) {
    input.classList.remove('error');
    const error = input.parentElement.querySelector('.field-error');
    if (error) error.remove();
}

// ==========================================
// KEYBOARD SHORTCUTS
// ==========================================

document.addEventListener('keydown', function(e) {
    // Ctrl/Cmd + S = Save (prevent default browser save)
    if ((e.ctrlKey || e.metaKey) && e.key === 's') {
        e.preventDefault();
        const saveButton = document.querySelector('[data-action="save"]');
        if (saveButton) saveButton.click();
        toast.info('Save shortcut triggered');
    }
    
    // Escape = Close modal/cancel
    if (e.key === 'Escape') {
        const activeModal = document.querySelector('.modal.active');
        if (activeModal) {
            activeModal.classList.remove('active');
        }
    }
    
    // Ctrl/Cmd + R = Refresh (with our custom refresh)
    if ((e.ctrlKey || e.metaKey) && e.key === 'r') {
        e.preventDefault();
        const refreshButton = document.querySelector('[onclick*="refresh"]');
        if (refreshButton) {
            refreshButton.click();
            toast.info('Refreshing...');
        }
    }
});

// ==========================================
// COPY TO CLIPBOARD WITH FEEDBACK
// ==========================================

async function copyToClipboard(text, successMessage = 'Copied to clipboard') {
    try {
        await navigator.clipboard.writeText(text);
        toast.success(successMessage);
        return true;
    } catch (err) {
        toast.error('Failed to copy to clipboard');
        return false;
    }
}

// ==========================================
// EXPORT FUNCTIONS
// ==========================================

window.toast = toast;
window.loading = loading;
window.SelectionManager = SelectionManager;
window.confirmAction = confirmAction;
window.showConfirmDialog = showConfirmDialog;
window.updateProgress = updateProgress;
window.showFieldError = showFieldError;
window.clearFieldError = clearFieldError;
window.copyToClipboard = copyToClipboard;

// ==========================================
// INITIALIZATION
// ==========================================

console.log('✅ UX Feedback System Initialized');
console.log('Available: toast, loading, SelectionManager, confirmAction, etc.');
