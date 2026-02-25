// First-Run Detection
// Checks if system is configured and redirects to wizard if needed

(function() {
    // Don't run on wizard page itself
    if (window.location.pathname.includes('setup-wizard')) {
        return;
    }

    // Check if setup was completed (persistent flag)
    async function checkFirstRun() {
        try {
            // Check for setup completion flag in localStorage
            const setupCompleted = localStorage.getItem('dplaneos_setup_completed');
            
            if (setupCompleted === 'true') {
                // Setup already completed, don't redirect
                return;
            }
            
            // If no flag, check with daemon
            const response = await fetch('/api/system/status');
            const data = await response.json();
            
            // Use server-side fields (setup_complete = system_config key, has_users = user count > 0)
            const isConfigured = data.setup_complete || (data.has_users && data.has_pools);
            if (!isConfigured) {
                window.location.href = '/pages/setup-wizard.html';
            } else {
                localStorage.setItem('dplaneos_setup_completed', 'true');
            }
            
        } catch (error) {
            console.error('Failed to check system status:', error);
            
            // Only redirect if we've never completed setup before
            const setupCompleted = localStorage.getItem('dplaneos_setup_completed');
            if (setupCompleted !== 'true') {
                window.location.href = '/pages/setup-wizard.html';
            }
        }
    }

    // Run check after page load
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', checkFirstRun);
    } else {
        checkFirstRun();
    }
})();
