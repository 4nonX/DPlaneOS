// D-PlaneOS - Enhanced UI Library
(function() {
  'use strict';
  
  const EnhancedUI = {
    toastContainer: null,
    toasts: [],
    
    init() {
      if (!this.toastContainer) {
        this.toastContainer = document.getElementById('dplane-toast-container');
        if (!this.toastContainer) {
          this.toastContainer = document.createElement('div');
          this.toastContainer.id = 'dplane-toast-container';
          this.toastContainer.className = 'toast-container';
          this.toastContainer.style.cssText = [
            'position:fixed;top:88px;right:24px;z-index:10000;',
            'display:flex;flex-direction:column;gap:10px;max-width:420px;',
            'pointer-events:none;',
          ].join('');
          document.body.appendChild(this.toastContainer);
        }
      }
    },
    
    toast(message, type = 'info', duration = 5000) {
      this.init();

      const icons = { success: 'check_circle', error: 'cancel', warning: 'warning', info: 'info' };
      const accentMap = { success: '#10B981', error: '#EF4444', warning: '#F59E0B', info: '#8a9cff' };
      const icon = icons[type] || 'info';
      const accent = accentMap[type] || '#8a9cff';

      const toast = document.createElement('div');
      toast.className = `toast toast-${type}`;
      toast.style.cssText = [
        'display:flex;align-items:flex-start;gap:12px;',
        'padding:14px 16px;border-radius:12px;',
        'background:rgba(30,41,59,0.98);',
        'border:1px solid rgba(255,255,255,0.10);',
        `border-left:4px solid ${accent};`,
        'backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);',
        'box-shadow:0 8px 32px rgba(0,0,0,0.4);',
        'transform:translateX(440px);opacity:0;',
        'transition:transform 0.3s cubic-bezier(0.4,0,0.2,1),opacity 0.3s ease;',
        'min-width:280px;max-width:400px;pointer-events:all;',
      ].join('');

      toast.innerHTML = `
        <span class="material-symbols-rounded" style="font-size:20px;flex-shrink:0;color:${accent};margin-top:1px;">${icon}</span>
        <span style="flex:1;font-size:14px;line-height:1.5;color:rgba(255,255,255,0.9);">${message}</span>
        <button aria-label="Dismiss" onclick="EnhancedUI.removeToast(this.parentElement)" style="background:none;border:none;cursor:pointer;color:rgba(255,255,255,0.45);padding:0;margin:-2px -2px 0 4px;font-family:'Material Symbols Rounded';font-size:18px;line-height:1;flex-shrink:0;transition:color .15s;" onmouseenter="this.style.color='rgba(255,255,255,0.9)'" onmouseleave="this.style.color='rgba(255,255,255,0.45)'">close</button>
      `;

      this.toastContainer.appendChild(toast);
      this.toasts.push(toast);

      // Animate in
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          toast.style.transform = 'translateX(0)';
          toast.style.opacity = '1';
        });
      });

      if (duration > 0) {
        let timer = setTimeout(() => this.removeToast(toast), duration);
        // Pause on hover
        toast.addEventListener('mouseenter', () => clearTimeout(timer));
        toast.addEventListener('mouseleave', () => {
          timer = setTimeout(() => this.removeToast(toast), 2000);
        });
      }

      return toast;
    },
    
    removeToast(toast) {
      toast.classList.add('removing');
      setTimeout(() => {
        toast.remove();
        this.toasts = this.toasts.filter(t => t !== toast);
      }, 300);
    },
    
    showLoading(text = 'Loading...') {
      let overlay = document.getElementById('loadingOverlay');
      if (!overlay) {
        overlay = document.createElement('div');
        overlay.id = 'loadingOverlay';
        overlay.className = 'loading-overlay';
        overlay.innerHTML = `
          <div class="loading-content">
            <div class="spinner spinner-lg"></div>
            <div style="margin-top:16px;color:rgba(255,255,255,0.8);">${text}</div>
          </div>
        `;
        document.body.appendChild(overlay);
      }
      overlay.style.display = 'flex';
    },
    
    hideLoading() {
      const overlay = document.getElementById('loadingOverlay');
      if (overlay) overlay.style.display = 'none';
    },
    
    async confirm(title, message) {
      return new Promise((resolve) => {
        const backdrop = document.createElement('div');
        backdrop.className = 'modal-backdrop';
        backdrop.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.7);backdrop-filter:blur(4px);display:flex;align-items:center;justify-content:center;z-index:9998;';
        
        const modal = document.createElement('div');
        modal.className = 'modal-content';
        modal.style.cssText = 'background:rgba(30,41,59,0.98);border-radius:16px;border:1px solid rgba(255,255,255,0.1);max-width:480px;width:90%;backdrop-filter:blur(10px);';
        modal.innerHTML = `
          <div style="padding:24px;border-bottom:1px solid rgba(255,255,255,0.1);">
            <h3 style="margin:0;font-size:18px;font-weight:600;">${title}</h3>
          </div>
          <div style="padding:24px;color:rgba(255,255,255,0.8);line-height:1.6;">${message}</div>
          <div style="padding:16px 24px;border-top:1px solid rgba(255,255,255,0.1);display:flex;gap:12px;justify-content:flex-end;">
            <button class="btn" data-action="cancel">Cancel</button>
            <button class="btn btn-primary" data-action="confirm">Confirm</button>
          </div>
        `;
        
        backdrop.appendChild(modal);
        document.body.appendChild(backdrop);
        
        backdrop.addEventListener('click', (e) => {
          if (e.target === backdrop) {
            backdrop.remove();
            resolve(false);
          }
        });
        
        modal.querySelectorAll('button').forEach(btn => {
          btn.addEventListener('click', () => {
            backdrop.remove();
            resolve(btn.dataset.action === 'confirm');
          });
        });
      });
    },

    async prompt(title, message, defaultValue = '', placeholder = '') {
      return new Promise((resolve) => {
        const backdrop = document.createElement('div');
        backdrop.className = 'modal-backdrop';
        backdrop.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.6);display:flex;align-items:center;justify-content:center;z-index:10000;backdrop-filter:blur(4px);';
        const modal = document.createElement('div');
        modal.style.cssText = 'background:rgba(30,41,59,0.98);border-radius:16px;border:1px solid rgba(255,255,255,0.1);max-width:480px;width:90%;backdrop-filter:blur(10px);';
        const inputId = 'ui-prompt-' + Date.now();
        modal.innerHTML = `
          <div style="padding:24px 24px 0;font-size:18px;font-weight:700;">${title}</div>
          <div style="padding:12px 24px;color:rgba(255,255,255,0.7);font-size:14px;">${message}</div>
          <div style="padding:0 24px 16px;">
            <input id="${inputId}" type="text" value="${defaultValue}" placeholder="${placeholder}"
              style="width:100%;background:rgba(255,255,255,0.05);border:1px solid rgba(255,255,255,0.15);border-radius:12px;padding:12px 16px;color:#fff;font-size:15px;outline:none;">
          </div>
          <div style="padding:0 24px 24px;display:flex;gap:8px;justify-content:flex-end;">
            <button class="btn" data-action="cancel" style="background:rgba(255,255,255,0.08);border:none;padding:10px 20px;border-radius:12px;color:rgba(255,255,255,0.7);cursor:pointer;">Cancel</button>
            <button class="btn btn-primary" data-action="ok" style="border:none;padding:10px 20px;border-radius:12px;cursor:pointer;">OK</button>
          </div>
        `;
        backdrop.appendChild(modal);
        document.body.appendChild(backdrop);
        const input = document.getElementById(inputId);
        input.focus();
        input.select();
        input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { backdrop.remove(); resolve(input.value); } if (e.key === 'Escape') { backdrop.remove(); resolve(null); } });
        modal.querySelectorAll('button').forEach(btn => {
          btn.addEventListener('click', () => { backdrop.remove(); resolve(btn.dataset.action === 'ok' ? input.value : null); });
        });
      });
    },

    async modal(title, content, buttons = [{text:'Close',primary:true}]) {
      return new Promise((resolve) => {
        const backdrop = document.createElement('div');
        backdrop.className = 'modal-backdrop';
        backdrop.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.6);display:flex;align-items:center;justify-content:center;z-index:10000;backdrop-filter:blur(4px);';
        const modal = document.createElement('div');
        modal.style.cssText = 'background:rgba(30,41,59,0.98);border-radius:16px;border:1px solid rgba(255,255,255,0.1);max-width:640px;width:90%;max-height:80vh;overflow-y:auto;backdrop-filter:blur(10px);';
        const btnHtml = buttons.map((b,i) => `<button class="btn${b.primary?' btn-primary':''}" data-idx="${i}" style="border:none;padding:10px 20px;border-radius:12px;cursor:pointer;${b.primary?'':'background:rgba(255,255,255,0.08);color:rgba(255,255,255,0.7);'}">${b.text}</button>`).join('');
        modal.innerHTML = `
          <div style="padding:24px 24px 0;font-size:18px;font-weight:700;">${title}</div>
          <div style="padding:16px 24px;">${content}</div>
          <div style="padding:0 24px 24px;display:flex;gap:8px;justify-content:flex-end;">${btnHtml}</div>
        `;
        backdrop.appendChild(modal);
        document.body.appendChild(backdrop);
        modal.querySelectorAll('button').forEach(btn => {
          btn.addEventListener('click', () => { backdrop.remove(); resolve(parseInt(btn.dataset.idx)); });
        });
      });
    },
    
    emptyState(options) {
      const div = document.createElement('div');
      div.className = 'empty-state';
      div.innerHTML = `
        ${options.icon ? `<div class="empty-state-icon">${options.icon}</div>` : ''}
        <div class="empty-state-title">${options.title || 'No data'}</div>
        <div class="empty-state-message">${options.message || 'Nothing to show here'}</div>
        ${options.action ? `<button class="empty-state-action" onclick="${options.action.onclick}">${options.action.label}</button>` : ''}
      `;
      return div;
    },
    
    skeleton(count = 3) {
      const container = document.createElement('div');
      for (let i = 0; i < count; i++) {
        const sk = document.createElement('div');
        sk.className = 'skeleton skeleton-card';
        sk.style.marginBottom = '16px';
        container.appendChild(sk);
      }
      return container;
    },
    
    progress(value, max = 100) {
      const container = document.createElement('div');
      container.className = 'progress-container';
      const bar = document.createElement('div');
      bar.className = 'progress-bar';
      bar.style.width = `${(value / max) * 100}%`;
      container.appendChild(bar);
      return container;
    }
  };
  
  // Expose globally
  window.EnhancedUI = EnhancedUI;
  window.ui = EnhancedUI; // Alias for backwards compatibility
  
  // Init on load
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => EnhancedUI.init());
  } else {
    EnhancedUI.init();
  }
  
  // Enhance csrfFetch
  const originalFetch = window.csrfFetch;
  if (originalFetch) {
    window.csrfFetch = async function(url, options = {}) {
      const showLoading = options.showLoading !== false;
      const showError = options.showError !== false;
      
      if (showLoading) EnhancedUI.showLoading(options.loadingText);
      
      try {
        const response = await originalFetch(url, options);
        
        if (showLoading) EnhancedUI.hideLoading();
        
        if (!response.ok && showError) {
          if (response.status === 403) {
            EnhancedUI.toast('Access denied', 'error');
          } else if (response.status >= 500) {
            EnhancedUI.toast('Server error', 'error');
          }
        }
        
        return response;
      } catch (error) {
        if (showLoading) EnhancedUI.hideLoading();
        if (showError) EnhancedUI.toast(error.message || 'Network error', 'error');
        throw error;
      }
    };
  }
  
  // Connection monitor
  let isOnline = navigator.onLine;
  let statusEl = null;
  
  function updateConnectionStatus(online) {
    if (online === isOnline) return;
    isOnline = online;
    
    if (!statusEl) {
      statusEl = document.createElement('div');
      statusEl.className = 'connection-status';
      document.body.appendChild(statusEl);
    }
    
    if (online) {
      statusEl.className = 'connection-status online';
      statusEl.innerHTML = '<span class="status-dot"></span> Connected';
      setTimeout(() => statusEl.style.display = 'none', 3000);
    } else {
      statusEl.className = 'connection-status';
      statusEl.innerHTML = '<span class="status-dot"></span> Connection lost';
      statusEl.style.display = 'flex';
    }
  }
  
  window.addEventListener('online', () => updateConnectionStatus(true));
  window.addEventListener('offline', () => updateConnectionStatus(false));
  
  // Keyboard shortcuts
  let keySequence = '';
  let keyTimer = null;
  
  document.addEventListener('keydown', (e) => {
    if (['INPUT', 'TEXTAREA', 'SELECT'].includes(e.target.tagName)) return;
    
    keySequence += e.key;
    clearTimeout(keyTimer);
    
    // Navigation shortcuts
    const shortcuts = {
      'gd': 'index.html',
      'gf': 'files.html',
      'gs': 'pools.html',
      'gc': 'docker.html',
      'gu': 'users.html'
    };
    
    if (shortcuts[keySequence]) {
      e.preventDefault();
      window.location.href = shortcuts[keySequence];
      keySequence = '';
    } else if (e.key === '?') {
      e.preventDefault();
      EnhancedUI.toast('Shortcuts: g+d (Dashboard), g+f (Files), g+s (Storage), g+c (Docker), g+u (Users)', 'info', 6000);
    } else if (e.key === 'Escape') {
      document.querySelectorAll('.modal-backdrop').forEach(m => m.remove());
    } else {
      keyTimer = setTimeout(() => keySequence = '', 1000);
    }
  });
  
  // Form validation helper
  window.validateForm = function(form) {
    const inputs = form.querySelectorAll('[required]');
    let isValid = true;
    
    inputs.forEach(input => {
      const group = input.closest('.form-group');
      if (!group) return;
      
      if (!input.value.trim()) {
        group.classList.add('has-error');
        isValid = false;
      } else {
        group.classList.remove('has-error');
        group.classList.add('has-success');
      }
    });
    
    if (!isValid) {
      EnhancedUI.toast('Please fill all required fields', 'error');
    }
    
    return isValid;
  };
  
})();
