/**
 * D-PlaneOS Theme Engine
 * Handles dark/light/auto theme switching with Material Design 3
 */

(function() {
    'use strict';

    const THEME_KEY = 'dplaneos-theme';
    const THEMES = ['light', 'dark', 'auto'];

    class ThemeEngine {
        constructor() {
            this.currentTheme = this.loadTheme();
            this.mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
            this.init();
        }

        init() {
            // Apply initial theme
            this.applyTheme(this.currentTheme);

            // Listen for system theme changes
            this.mediaQuery.addEventListener('change', (e) => {
                if (this.currentTheme === 'auto') {
                    this.applySystemTheme(e.matches);
                }
            });

            // Add theme toggle listeners
            this.attachToggleListeners();
        }

        loadTheme() {
            const saved = localStorage.getItem(THEME_KEY);
            return THEMES.includes(saved) ? saved : 'auto';
        }

        saveTheme(theme) {
            localStorage.setItem(THEME_KEY, theme);
        }

        applyTheme(theme) {
            const html = document.documentElement;
            
            if (theme === 'auto') {
                this.applySystemTheme(this.mediaQuery.matches);
            } else {
                html.setAttribute('data-theme', theme);
            }

            this.currentTheme = theme;
            this.saveTheme(theme);
            this.updateToggleButtons();
        }

        applySystemTheme(isDark) {
            document.documentElement.setAttribute('data-theme', isDark ? 'dark' : 'light');
        }

        toggleTheme() {
            const currentIndex = THEMES.indexOf(this.currentTheme);
            const nextTheme = THEMES[(currentIndex + 1) % THEMES.length];
            this.applyTheme(nextTheme);
        }

        setTheme(theme) {
            if (THEMES.includes(theme)) {
                this.applyTheme(theme);
            }
        }

        getTheme() {
            return this.currentTheme;
        }

        getEffectiveTheme() {
            if (this.currentTheme === 'auto') {
                return this.mediaQuery.matches ? 'dark' : 'light';
            }
            return this.currentTheme;
        }

        attachToggleListeners() {
            // Theme toggle buttons
            document.addEventListener('click', (e) => {
                if (e.target.matches('[data-theme-toggle]')) {
                    this.toggleTheme();
                }

                if (e.target.matches('[data-theme-set]')) {
                    const theme = e.target.getAttribute('data-theme-set');
                    this.setTheme(theme);
                }
            });
        }

        updateToggleButtons() {
            const buttons = document.querySelectorAll('[data-theme-toggle], [data-theme-set]');
            buttons.forEach(btn => {
                if (btn.hasAttribute('data-theme-set')) {
                    const theme = btn.getAttribute('data-theme-set');
                    btn.classList.toggle('active', theme === this.currentTheme);
                }
            });
        }
    }

    // Initialize theme engine
    const themeEngine = new ThemeEngine();

    // Export to window
    window.themeEngine = themeEngine;

    // Expose convenience methods
    window.setTheme = (theme) => themeEngine.setTheme(theme);
    window.toggleTheme = () => themeEngine.toggleTheme();
    window.getTheme = () => themeEngine.getTheme();
})();
