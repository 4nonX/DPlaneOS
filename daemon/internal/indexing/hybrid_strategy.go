package indexing

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// IndexingStrategy defines how a path should be indexed
type IndexingStrategy int

const (
	StrategyRealtime  IndexingStrategy = iota // Inotify (for critical paths)
	StrategyPeriodic                          // Background scan (for large archives)
	StrategyOnDemand                          // Only when accessed (lazy)
	StrategyDisabled                          // Not indexed
)

// WatchConfig defines monitoring configuration for a path
type WatchConfig struct {
	Path                string
	Strategy            IndexingStrategy
	ScanInterval        time.Duration // For periodic scanning
	InotifyPriority     int           // Higher = more important (when limit reached)
	RecursiveDepth      int           // -1 = unlimited, 0 = just this dir, N = N levels deep
	ExcludePatterns     []string      // Glob patterns to exclude
	IncludeExtensions   []string      // Only these extensions (empty = all)
}

// HybridIndexer manages multiple indexing strategies
type HybridIndexer struct {
	configs          []WatchConfig
	realtimeWatches  map[string]interface{} // Inotify watches
	periodicScanners map[string]*PeriodicScanner
	inotifyLimit     int
	inotifyUsed      int
	mutex            sync.RWMutex
	ctx              context.Context
	cancel           context.CancelFunc
}

// PeriodicScanner handles background scanning
type PeriodicScanner struct {
	path       string
	interval   time.Duration
	lastScan   time.Time
	stopChan   chan bool
	onNewFile  func(path string, info os.FileInfo)
	onModified func(path string, info os.FileInfo)
}

// NewHybridIndexer creates a new hybrid indexing system
func NewHybridIndexer(inotifyLimit int) *HybridIndexer {
	ctx, cancel := context.WithCancel(context.Background())
	
	return &HybridIndexer{
		configs:          make([]WatchConfig, 0),
		realtimeWatches:  make(map[string]interface{}),
		periodicScanners: make(map[string]*PeriodicScanner),
		inotifyLimit:     inotifyLimit,
		inotifyUsed:      0,
		ctx:              ctx,
		cancel:           cancel,
	}
}

// AddPath adds a path with specified strategy
func (h *HybridIndexer) AddPath(config WatchConfig) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	
	// Validate path exists
	if _, err := os.Stat(config.Path); err != nil {
		return fmt.Errorf("path does not exist: %w", err)
	}
	
	h.configs = append(h.configs, config)
	
	switch config.Strategy {
	case StrategyRealtime:
		return h.addRealtimeWatch(config)
	case StrategyPeriodic:
		return h.addPeriodicScanner(config)
	case StrategyOnDemand:
		// No action needed, scan on demand
		log.Printf("Added on-demand indexing for: %s", config.Path)
		return nil
	case StrategyDisabled:
		log.Printf("Indexing disabled for: %s", config.Path)
		return nil
	}
	
	return fmt.Errorf("unknown strategy: %d", config.Strategy)
}

// addRealtimeWatch attempts to add inotify watch
func (h *HybridIndexer) addRealtimeWatch(config WatchConfig) error {
	// Check if we're at inotify limit
	if h.inotifyUsed >= h.inotifyLimit {
		log.Printf("WARNING: Inotify limit reached (%d/%d), falling back to periodic scan for: %s",
			h.inotifyUsed, h.inotifyLimit, config.Path)
		
		// Automatically fallback to periodic scanning
		config.Strategy = StrategyPeriodic
		if config.ScanInterval == 0 {
			config.ScanInterval = 5 * time.Minute // Default fallback interval
		}
		return h.addPeriodicScanner(config)
	}
	
	// TODO: Implement actual inotify watch
	// For now, simulate
	h.realtimeWatches[config.Path] = nil
	h.inotifyUsed++
	
	log.Printf("Added real-time inotify watch: %s (%d/%d used)",
		config.Path, h.inotifyUsed, h.inotifyLimit)
	
	return nil
}

// addPeriodicScanner creates a background scanner
func (h *HybridIndexer) addPeriodicScanner(config WatchConfig) error {
	scanner := &PeriodicScanner{
		path:     config.Path,
		interval: config.ScanInterval,
		stopChan: make(chan bool),
	}
	
	h.periodicScanners[config.Path] = scanner
	
	// Start scanner goroutine
	go scanner.run(h.ctx)
	
	log.Printf("Added periodic scanner: %s (interval: %s)",
		config.Path, config.ScanInterval)
	
	return nil
}

// GetStatus returns current indexing status
func (h *HybridIndexer) GetStatus() IndexingStatus {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	
	realtimePaths := make([]string, 0, len(h.realtimeWatches))
	for path := range h.realtimeWatches {
		realtimePaths = append(realtimePaths, path)
	}
	
	periodicPaths := make([]string, 0, len(h.periodicScanners))
	for path := range h.periodicScanners {
		periodicPaths = append(periodicPaths, path)
	}
	
	return IndexingStatus{
		InotifyUsed:    h.inotifyUsed,
		InotifyLimit:   h.inotifyLimit,
		InotifyPercent: float64(h.inotifyUsed) / float64(h.inotifyLimit) * 100.0,
		RealtimePaths:  realtimePaths,
		PeriodicPaths:  periodicPaths,
		TotalConfigs:   len(h.configs),
	}
}

// Stop gracefully shuts down all indexing
func (h *HybridIndexer) Stop() {
	h.cancel()
	
	// Stop all periodic scanners
	for _, scanner := range h.periodicScanners {
		scanner.Stop()
	}
	
	log.Println("Hybrid indexer stopped")
}

// IndexingStatus provides current state
type IndexingStatus struct {
	InotifyUsed    int      `json:"inotify_used"`
	InotifyLimit   int      `json:"inotify_limit"`
	InotifyPercent float64  `json:"inotify_percent"`
	RealtimePaths  []string `json:"realtime_paths"`
	PeriodicPaths  []string `json:"periodic_paths"`
	TotalConfigs   int      `json:"total_configs"`
}

// PeriodicScanner methods

func (s *PeriodicScanner) run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	
	// Initial scan
	s.scan()
	
	for {
		select {
		case <-ticker.C:
			s.scan()
		case <-s.stopChan:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *PeriodicScanner) scan() {
	log.Printf("Scanning: %s", s.path)
	
	// Walk directory tree
	filepath.Walk(s.path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		
		// Check if file is new or modified since last scan
		if info.ModTime().After(s.lastScan) {
			log.Printf("Found modified file: %s", path)
			if s.onModified != nil {
				s.onModified(path, info)
			}
		}
		
		return nil
	})
	
	s.lastScan = time.Now()
}

func (s *PeriodicScanner) Stop() {
	s.stopChan <- true
}
