package monitoring

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// InotifyStats represents current inotify usage
type InotifyStats struct {
	Used      int     `json:"used"`
	Limit     int     `json:"limit"`
	Percent   float64 `json:"percent"`
	Warning   bool    `json:"warning"`   // true if >= 90%
	Critical  bool    `json:"critical"`  // true if >= 95%
}

// GetInotifyStats returns current inotify watch usage
func GetInotifyStats() (*InotifyStats, error) {
	// Get system limit
	limitData, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches")
	if err != nil {
		return nil, fmt.Errorf("failed to read max_user_watches: %w", err)
	}
	
	limit, err := strconv.Atoi(strings.TrimSpace(string(limitData)))
	if err != nil {
		return nil, fmt.Errorf("failed to parse limit: %w", err)
	}
	
	// Count active watches across all processes
	// This is an approximation - actual usage may vary
	used := 0
	
	// Try to count inotify instances from /proc/*/fd
	entries, err := os.ReadDir("/proc")
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			
			fdPath := fmt.Sprintf("/proc/%s/fd", entry.Name())
			fds, err := os.ReadDir(fdPath)
			if err != nil {
				continue
			}
			
			for _, fd := range fds {
				linkPath := fmt.Sprintf("%s/%s", fdPath, fd.Name())
				link, err := os.Readlink(linkPath)
				if err != nil {
					continue
				}
				
				if strings.Contains(link, "inotify") {
					used++
				}
			}
		}
	}
	
	percent := float64(used) / float64(limit) * 100.0
	
	stats := &InotifyStats{
		Used:     used,
		Limit:    limit,
		Percent:  percent,
		Warning:  percent >= 90.0,
		Critical: percent >= 95.0,
	}
	
	return stats, nil
}
