// D-PlaneOS - Keyboard Shortcuts

class KeyboardShortcuts {
  constructor() {
    this.shortcuts = {
      // Navigation
      'g d': () => window.location.href = 'index.html',
      'g f': () => window.location.href = 'files.html',
      'g s': () => window.location.href = 'pools.html',
      'g c': () => window.location.href = 'docker.html',
      'g u': () => window.location.href = 'users.html',
      
      // Actions
      '?': () => this.showHelp(),
      '/': () => this.focusSearch(),
      'Escape': () => this.closeModals(),
      'r': () => this.refresh()
    };
    
    this.sequence = '';
    this.sequenceTimer = null;
    this.init();
  }
  
  init() {
    document.addEventListener('keydown', (e) => this.handleKeydown(e));
  }
  
  handleKeydown(e) {
    // Ignore if typing in input
    if (['INPUT', 'TEXTAREA', 'SELECT'].includes(e.target.tagName)) {
      return;
    }
    
    // Single key shortcuts
    if (this.shortcuts[e.key]) {
      e.preventDefault();
      this.shortcuts[e.key]();
      return;
    }
    
    // Sequence shortcuts (e.g., 'g d')
    this.sequence += e.key;
    
    if (this.shortcuts[this.sequence]) {
      e.preventDefault();
      this.shortcuts[this.sequence]();
      this.sequence = '';
      clearTimeout(this.sequenceTimer);
    } else {
      // Reset sequence after 1 second
      clearTimeout(this.sequenceTimer);
      this.sequenceTimer = setTimeout(() => {
        this.sequence = '';
      }, 1000);
    }
  }
  
  showHelp() {
    const helpText = `
      <h3 style="margin-bottom:16px;">Keyboard Shortcuts</h3>
      <div style="display:grid;gap:12px;font-size:14px;">
        <div style="display:grid;grid-template-columns:120px 1fr;gap:12px;">
          <div><kbd>g d</kbd></div><div>Go to Dashboard</div>
          <div><kbd>g f</kbd></div><div>Go to Files</div>
          <div><kbd>g s</kbd></div><div>Go to Storage</div>
          <div><kbd>g c</kbd></div><div>Go to Docker</div>
          <div><kbd>g u</kbd></div><div>Go to Users</div>
          <div><kbd>/</kbd></div><div>Focus search</div>
          <div><kbd>r</kbd></div><div>Refresh page</div>
          <div><kbd>Esc</kbd></div><div>Close modals</div>
          <div><kbd>?</kbd></div><div>Show this help</div>
        </div>
      </div>
      <style>
        kbd {
          background: rgba(255,255,255,0.1);
          border: 1px solid rgba(255,255,255,0.2);
          border-radius: 4px;
          padding: 4px 8px;
          font-size: 12px;
          font-family: monospace;
        }
      </style>
    `;
    
    if (window.EnhancedUI) {
      EnhancedUI.confirm('Keyboard Shortcuts', helpText, { confirmText: 'Got it', cancelText: '' });
    }
  }
  
  focusSearch() {
    const searchInput = document.querySelector('input[type="search"], input[placeholder*="Search"]');
    if (searchInput) {
      searchInput.focus();
      searchInput.select();
    }
  }
  
  closeModals() {
    // Close any open modals
    const modals = document.querySelectorAll('.modal-backdrop');
    modals.forEach(modal => modal.remove());
    
    // Close tooltips
    const tooltips = document.querySelectorAll('.tooltip');
    tooltips.forEach(tooltip => tooltip.remove());
  }
  
  refresh() {
    window.location.reload();
  }
}

// Initialize keyboard shortcuts
if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', () => {
    window.keyboardShortcuts = new KeyboardShortcuts();
  });
} else {
  window.keyboardShortcuts = new KeyboardShortcuts();
}
