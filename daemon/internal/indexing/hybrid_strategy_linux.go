//go:build linux

package indexing

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"
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
	ScanInterval      time.Duration     // For periodic scanning
	InotifyPriority   int               // Higher = more important (when limit reached)
	RecursiveDepth    int               // -1 = unlimited, 0 = just this dir, N = N levels deep
	ExcludePatterns   []string          // Glob patterns to exclude
	IncludeExtensions []string          // Only these extensions (empty = all)
	OnModified        func(path string) // Called when a change is detected (inotify path)
}

// HybridIndexer manages multiple indexing strategies
type HybridIndexer struct {
	configs          []WatchConfig
	realtimeWatches  map[string]*os.File // Inotify fd per path
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
		realtimeWatches:  make(map[string]*os.File),
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

// addRealtimeWatch attempts to add an inotify watch for the given path.
// Falls back to periodic scanning when the inotify descriptor limit is reached.
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

	// InotifyInit1 with IN_CLOEXEC | IN_NONBLOCK
	fd, errno := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if errno != nil {
		log.Printf("[indexer] inotify_init failed for %s: %v", config.Path, errno)
		return errno
	}

	mask := uint32(
		syscall.IN_CREATE |
			syscall.IN_DELETE |
			syscall.IN_MODIFY |
			syscall.IN_MOVED_FROM |
			syscall.IN_MOVED_TO |
			syscall.IN_CLOSE_WRITE,
	)
	if _, errno = syscall.InotifyAddWatch(fd, config.Path, mask); errno != nil {
		syscall.Close(fd)
		log.Printf("[indexer] inotify_add_watch %s: %v", config.Path, errno)
		return errno
	}

	f := os.NewFile(uintptr(fd), "inotify:"+config.Path)
	h.realtimeWatches[config.Path] = f
	h.inotifyUsed++

	go h.drainInotify(f, config)

	log.Printf("Added real-time inotify watch: %s (%d/%d used)",
		config.Path, h.inotifyUsed, h.inotifyLimit)

	return nil
}

// drainInotify reads inotify events from f and invokes config.OnModified on each event.
func (h *HybridIndexer) drainInotify(f *os.File, config WatchConfig) {
	// Buffer must be large enough for at least one inotify_event + NAME_MAX+1 bytes.
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		if err != nil || n < syscall.SizeofInotifyEvent {
			// f was closed (Stop/RemoveWatch) or an unrecoverable read error occurred.
			return
		}
		if config.OnModified != nil {
			config.OnModified(config.Path)
		}
		// Consume all events in the buffer so the kernel queue doesn't overflow.
		offset := 0
		for offset+syscall.SizeofInotifyEvent <= n {
			event := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			nameLen := int(event.Len)
			offset += syscall.SizeofInotifyEvent + nameLen
		}
	}
}

// RemoveWatch stops and removes the inotify watch for a path.
func (h *HybridIndexer) RemoveWatch(path string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if f, ok := h.realtimeWatches[path]; ok {
		f.Close() // causes drainInotify goroutine to return
		delete(h.realtimeWatches, path)
		if h.inotifyUsed > 0 {
			h.inotifyUsed--
		}
		log.Printf("[indexer] removed inotify watch: %s", path)
	}
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

	// Close all inotify file descriptors - causes drainInotify goroutines to exit
	h.mutex.Lock()
	for path, f := range h.realtimeWatches {
		f.Close()
		delete(h.realtimeWatches, path)
	}
	h.inotifyUsed = 0
	h.mutex.Unlock()

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

