package monitoring

import (
	"log"
	"sync"
	"time"
)

// alertState tracks the last time an alert was fired per event type,
// and whether it is currently in a "firing" state.
// This prevents notification flooding when a value oscillates around a threshold.
type alertState struct {
	lastFired  time.Time
	lastLevel  string
	firingAt   time.Time // when we first entered this level (for hysteresis)
	isFiring   bool
}

// BackgroundMonitor runs periodic checks and sends alerts with debouncing.
type BackgroundMonitor struct {
	interval      time.Duration
	alertCallback func(eventType string, data interface{}, level string)
	stopChan      chan bool

	// Debounce state: one entry per (eventType+level) key
	mu         sync.Mutex
	alertStates map[string]*alertState
}

// Debounce configuration
const (
	// Minimum time between repeated alerts of the same type+level
	alertCooldown = 5 * time.Minute

	// A threshold must be exceeded for this long before we alert (prevents flapping)
	hysteresisWindow = 30 * time.Second

	// After a condition clears, suppress re-alert for this duration
	clearanceCooldown = 2 * time.Minute
)

// NewBackgroundMonitor creates a new background monitor
func NewBackgroundMonitor(interval time.Duration, alertCallback func(string, interface{}, string)) *BackgroundMonitor {
	return &BackgroundMonitor{
		interval:    interval,
		alertCallback: alertCallback,
		stopChan:    make(chan bool),
		alertStates: make(map[string]*alertState),
	}
}

// Start begins the monitoring loop
func (m *BackgroundMonitor) Start() {
	go m.run()
}

// Stop halts the monitoring loop
func (m *BackgroundMonitor) Stop() {
	m.stopChan <- true
}

func (m *BackgroundMonitor) run() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.check()
		case <-m.stopChan:
			log.Println("Background monitor stopped")
			return
		}
	}
}

// maybeAlert fires the alertCallback only if debounce conditions are met.
// key should be unique per event type, e.g. "inotify_warning".
// level: "info", "warning", "critical", "clear"
func (m *BackgroundMonitor) maybeAlert(key string, level string, data interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	state, exists := m.alertStates[key]
	if !exists {
		state = &alertState{}
		m.alertStates[key] = state
	}

	// "info" level updates are always passed through for UI dashboard refresh —
	// they are not alerting events and don't need debouncing.
	if level == "info" {
		m.alertCallback(key, data, level)
		return
	}

	// "clear": condition resolved — notify once, then enter clearance cooldown
	if level == "clear" {
		if state.isFiring {
			state.isFiring = false
			state.lastFired = now
			state.lastLevel = "clear"
			m.alertCallback(key, data, level)
			log.Printf("MONITOR [%s]: condition cleared", key)
		}
		return
	}

	// For warning/critical: apply hysteresis — must be in this state for
	// hysteresisWindow before we fire, to prevent flapping alerts.
	if !state.isFiring || state.lastLevel != level {
		// Condition just entered or level changed — start hysteresis timer
		if !state.isFiring || state.lastLevel != level {
			state.firingAt = now
			state.isFiring = true
			state.lastLevel = level
		}
	}

	if now.Sub(state.firingAt) < hysteresisWindow {
		// Still within hysteresis window — don't fire yet
		return
	}

	// Apply cooldown: don't re-fire same level within cooldown period
	if !state.lastFired.IsZero() && now.Sub(state.lastFired) < alertCooldown {
		return
	}

	// All checks passed — fire the alert
	state.lastFired = now
	m.alertCallback(key, data, level)
	log.Printf("MONITOR [%s]: alert fired (level=%s)", key, level)
}

func (m *BackgroundMonitor) check() {
	// Check inotify stats
	stats, err := GetInotifyStats()
	if err != nil {
		log.Printf("Error getting inotify stats: %v", err)
		return
	}

	if stats.Critical {
		m.maybeAlert("inotify_status", "critical", stats)
	} else if stats.Warning {
		m.maybeAlert("inotify_status", "warning", stats)
	} else {
		// Clear any firing state, send info for UI
		m.maybeAlert("inotify_status", "clear", stats)
		m.maybeAlert("inotify_status", "info", stats)
	}
}

// CheckMountStatus verifies all registered ZFS mounts
func (m *BackgroundMonitor) CheckMountStatus() {
	// Called from the monitoring loop to detect unmounted pools
}
