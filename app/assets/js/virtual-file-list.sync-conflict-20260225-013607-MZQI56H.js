/**
 * D-PlaneOS - Virtual Scrolling File List
 * 
 * ELIMINATES UI FREEZE:
 * - Only renders visible rows (20-30 at a time)
 * - Handles 100,000+ files smoothly
 * - Constant 60fps regardless of file count
 * - Dynamic loading/unloading
 * 
 * FIXES: "Zombie-Zustand bei tausenden Dateien"
 */

class VirtualFileList {
    constructor(containerEl, options = {}) {
        this.container = containerEl;
        this.files = [];
        
        // Configuration
        this.rowHeight = options.rowHeight || 48;
        this.bufferSize = options.bufferSize || 5; // Extra rows above/below viewport
        this.visibleRows = 0;
        
        // State
        this.scrollTop = 0;
        this.startIndex = 0;
        this.endIndex = 0;
        
        // DOM elements
        this.viewport = null;
        this.contentWrapper = null;
        this.rowContainer = null;
        
        this.init();
    }
    
    init() {
        // Create virtual scroll structure
        this.viewport = document.createElement('div');
        this.viewport.className = 'virtual-scroll-viewport';
        this.viewport.style.cssText = `
            position: relative;
            height: 100%;
            overflow-y: auto;
            overflow-x: hidden;
        `;
        
        this.contentWrapper = document.createElement('div');
        this.contentWrapper.className = 'virtual-scroll-content';
        this.contentWrapper.style.cssText = `
            position: relative;
            width: 100%;
        `;
        
        this.rowContainer = document.createElement('div');
        this.rowContainer.className = 'virtual-scroll-rows';
        this.rowContainer.style.cssText = `
            position: absolute;
            top: 0;
            left: 0;
            right: 0;
        `;
        
        this.contentWrapper.appendChild(this.rowContainer);
        this.viewport.appendChild(this.contentWrapper);
        this.container.appendChild(this.viewport);
        
        // Event listeners
        this.viewport.addEventListener('scroll', this.handleScroll.bind(this));
        window.addEventListener('resize', this.handleResize.bind(this));
        
        this.calculateVisibleRows();
    }
    
    /**
     * Set file list (can be 100,000+ items)
     */
    setFiles(files) {
        this.files = files;
        
        // Set total height (creates scrollbar)
        const totalHeight = files.length * this.rowHeight;
        this.contentWrapper.style.height = totalHeight + 'px';
        
        // Reset scroll
        this.scrollTop = 0;
        this.viewport.scrollTop = 0;
        
        // Initial render
        this.render();
        
    }
    
    /**
     * Calculate how many rows fit in viewport
     */
    calculateVisibleRows() {
        const viewportHeight = this.viewport.clientHeight;
        this.visibleRows = Math.ceil(viewportHeight / this.rowHeight) + (this.bufferSize * 2);
    }
    
    /**
     * Handle scroll event (only triggered by user scroll)
     */
    handleScroll() {
        const scrollTop = this.viewport.scrollTop;
        
        // Only re-render if scrolled enough
        const scrollDelta = Math.abs(scrollTop - this.scrollTop);
        if (scrollDelta < this.rowHeight) {
            return; // Don't re-render for tiny scrolls
        }
        
        this.scrollTop = scrollTop;
        
        // Request animation frame for smooth rendering
        if (!this.rafId) {
            this.rafId = requestAnimationFrame(() => {
                this.render();
                this.rafId = null;
            });
        }
    }
    
    /**
     * Handle window resize
     */
    handleResize() {
        this.calculateVisibleRows();
        this.render();
    }
    
    /**
     * Render only visible rows
     */
    render() {
        // Calculate which rows to render
        this.startIndex = Math.floor(this.scrollTop / this.rowHeight) - this.bufferSize;
        this.startIndex = Math.max(0, this.startIndex);
        
        this.endIndex = this.startIndex + this.visibleRows;
        this.endIndex = Math.min(this.files.length, this.endIndex);
        
        // Position row container
        const offsetY = this.startIndex * this.rowHeight;
        this.rowContainer.style.transform = `translateY(${offsetY}px)`;
        
        // Clear existing rows
        this.rowContainer.innerHTML = '';
        
        // Render only visible rows
        const fragment = document.createDocumentFragment();
        
        for (let i = this.startIndex; i < this.endIndex; i++) {
            const file = this.files[i];
            const row = this.renderFileRow(file, i);
            fragment.appendChild(row);
        }
        
        this.rowContainer.appendChild(fragment);
        
        // Performance tracking
        if (this.startIndex === 0 && this.endIndex < 50) {
        }
    }
    
    /**
     * Render a single file row
     */
    renderFileRow(file, index) {
        const row = document.createElement('div');
        row.className = 'file-row';
        row.style.cssText = `
            height: ${this.rowHeight}px;
            display: flex;
            align-items: center;
            padding: 0 16px;
            border-bottom: 1px solid var(--outline);
            cursor: pointer;
        `;
        
        // Add hover effect
        row.addEventListener('mouseenter', () => {
            row.style.background = 'var(--surface-container-high)';
        });
        row.addEventListener('mouseleave', () => {
            row.style.background = 'transparent';
        });
        
        // File icon
        const icon = document.createElement('span');
        icon.className = 'material-symbols-rounded';
        icon.textContent = file.type === 'directory' ? 'folder' : 'description';
        icon.style.cssText = `
            font-size: 24px;
            color: ${file.type === 'directory' ? '#58a6ff' : 'var(--on-surface-variant)'};
            margin-right: 12px;
        `;
        
        // File name
        const name = document.createElement('div');
        name.className = 'file-name';
        name.textContent = file.name;
        name.style.cssText = `
            flex: 1;
            font-size: 14px;
            font-weight: ${file.type === 'directory' ? '600' : '400'};
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
        `;
        
        // File size
        const size = document.createElement('div');
        size.className = 'file-size';
        size.textContent = file.type === 'directory' ? '-' : formatBytes(file.size);
        size.style.cssText = `
            width: 100px;
            text-align: right;
            font-size: 13px;
            color: var(--on-surface-variant);
            font-family: 'Courier New', monospace;
        `;
        
        // File date
        const date = document.createElement('div');
        date.className = 'file-date';
        date.textContent = formatDate(file.modified);
        date.style.cssText = `
            width: 150px;
            text-align: right;
            font-size: 13px;
            color: var(--on-surface-variant);
            margin-left: 16px;
        `;
        
        row.appendChild(icon);
        row.appendChild(name);
        row.appendChild(size);
        row.appendChild(date);
        
        // Click handler
        row.addEventListener('click', () => {
            this.handleFileClick(file);
        });
        
        return row;
    }
    
    /**
     * Handle file click
     */
    handleFileClick(file) {
        if (file.type === 'directory') {
            // Navigate to directory
            window.navigateToDirectory(file.path);
        } else {
            // Open file preview/download
            window.openFile(file.path);
        }
    }
    
    /**
     * Update a single file (without full re-render)
     */
    updateFile(index, newData) {
        if (index >= 0 && index < this.files.length) {
            this.files[index] = { ...this.files[index], ...newData };
            
            // Only re-render if visible
            if (index >= this.startIndex && index < this.endIndex) {
                this.render();
            }
        }
    }
    
    /**
     * Scroll to specific file
     */
    scrollToFile(index) {
        const scrollTop = index * this.rowHeight;
        this.viewport.scrollTop = scrollTop;
    }
    
    /**
     * Get currently visible files
     */
    getVisibleFiles() {
        return this.files.slice(this.startIndex, this.endIndex);
    }
    
    /**
     * Cleanup
     */
    destroy() {
        if (this.rafId) {
            cancelAnimationFrame(this.rafId);
        }
        window.removeEventListener('resize', this.handleResize.bind(this));
        this.container.innerHTML = '';
    }
}

// Helper functions
function formatBytes(bytes) {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return Math.round((bytes / Math.pow(k, i)) * 100) / 100 + ' ' + sizes[i];
}

function formatDate(timestamp) {
    const date = new Date(timestamp * 1000);
    return date.toLocaleDateString() + ' ' + date.toLocaleTimeString();
}

// Export
if (typeof module !== 'undefined' && module.exports) {
    module.exports = VirtualFileList;
}

// Usage example:
/*
const container = document.getElementById('file-list-container');
const virtualList = new VirtualFileList(container, {
    rowHeight: 48,
    bufferSize: 5
});

// Load 100,000 files - UI stays responsive!
fetch('/api/files?path=/massive-directory')
    .then(res => res.json())
    .then(data => {
        virtualList.setFiles(data.files); // Renders only ~30 rows
    });
*/
