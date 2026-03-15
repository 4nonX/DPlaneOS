//go:build !linux

package indexing

import (
	"context"
	"log"
	"time"
)

// IndexingStrategy defines how a path should be indexed
type IndexingStrategy int

const (
	StrategyRealtime IndexingStrategy = iota // Inotify (for critical paths)
	StrategyPeriodic                         // Background scan (for large archives)
	StrategyOnDemand                         // Only when accessed (lazy)
	StrategyDisabled                         // Not indexed
)

// WatchConfig defines monitoring configuration for a path
type WatchConfig struct {
	Path              string
	Strategy          IndexingStrategy
	ScanInterval      time.Duration
	InotifyPriority   int
	RecursiveDepth    int
	ExcludePatterns   []string
	IncludeExtensions []string
	OnModified        func(path string)
}

// HybridIndexer manages multiple indexing strategies
type HybridIndexer struct {
	configs          []WatchConfig
	inotifyLimit     int
	inotifyUsed      int
	ctx              context.Context
	cancel           context.CancelFunc
}

// NewHybridIndexer creates a new hybrid indexing system
func NewHybridIndexer(inotifyLimit int) *HybridIndexer {
	ctx, cancel := context.WithCancel(context.Background())
	return &HybridIndexer{
		configs:      make([]WatchConfig, 0),
		inotifyLimit: inotifyLimit,
		inotifyUsed:  0,
		ctx:          ctx,
		cancel:       cancel,
	}
}

// AddPath adds a path with specified strategy
func (h *HybridIndexer) AddPath(config WatchConfig) error {
	// Automatically fallback to periodic scanning on non-Linux
	if config.Strategy == StrategyRealtime {
		log.Printf("[indexer] Inotify not supported on this platform, falling back to periodic scan for: %s", config.Path)
		config.Strategy = StrategyPeriodic
		if config.ScanInterval == 0 {
			config.ScanInterval = 5 * time.Minute
		}
	}

	h.configs = append(h.configs, config)

	if config.Strategy == StrategyPeriodic {
		log.Printf("[indexer] Periodic scanner started for: %s", config.Path)
	}

	return nil
}

// RemoveWatch stops and removes the watch for a path.
func (h *HybridIndexer) RemoveWatch(path string) {
	// Stub
}

// GetStatus returns current indexing status
func (h *HybridIndexer) GetStatus() IndexingStatus {
	return IndexingStatus{
		InotifyUsed:    0,
		InotifyLimit:   h.inotifyLimit,
		InotifyPercent: 0,
		RealtimePaths:  []string{},
		PeriodicPaths:  []string{},
		TotalConfigs:   len(h.configs),
	}
}

// Stop gracefully shuts down all indexing
func (h *HybridIndexer) Stop() {
	h.cancel()
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
