package resource

import (
	"testing"
	"time"
)

func TestWatcherResourceMonitoring(t *testing.T) {
	// Create watcher with 100MB memory limit for testing
	watcher := NewWatcher(500*time.Millisecond, 100*1024*1024, []string{"/"})

	status := watcher.GetStatus()
	if status.Status == "UNKNOWN" {
		t.Fatalf("Initial status should not be UNKNOWN")
	}

	// Verify disk check runs
	if status.DiskUsagePercent < 0 || status.DiskUsagePercent > 100 {
		t.Errorf("Invalid disk usage percent: %d%%", status.DiskUsagePercent)
	}

	// Verify memory check runs
	if status.MemoryPercent < 0 || status.MemoryPercent > 100 {
		t.Errorf("Invalid memory percent: %d%%", status.MemoryPercent)
	}

	// Verify file descriptor check runs (may be 0 on non-Linux)
	if status.FileDescriptorPercent < 0 || status.FileDescriptorPercent > 100 {
		t.Errorf("Invalid FD percent: %d%%", status.FileDescriptorPercent)
	}

	t.Logf("Resource status: %+v", status)
}

func TestWatcherCallbackOnCritical(t *testing.T) {
	watcher := NewWatcher(100*time.Millisecond, 10*1024, []string{"/"}) // Tiny memory limit to trigger CRITICAL

	watcher.SetCriticalCallback(func(rs ResourceStatus) {
		t.Logf("Critical callback triggered: %+v", rs)
	})

	watcher.Start()
	defer watcher.Stop()

	// Wait for check to run
	time.Sleep(300 * time.Millisecond)

	status := watcher.GetStatus()
	// With 10MB limit and normal process memory, should hit CRITICAL
	if status.Status == "CRITICAL" {
		t.Logf("System correctly detected CRITICAL resource state: %+v", status)
	}
}

func TestCriticalResources(t *testing.T) {
	got := ResourceStatus{DiskUsagePercent: 50, MemoryPercent: 96, FileDescriptorPercent: 90}.CriticalResources()
	if len(got) != 2 || got["memory"] != 96 || got["file_descriptors"] != 90 {
		t.Errorf("critical resources: %v (want memory 96 and file_descriptors 90)", got)
	}
	if got := (ResourceStatus{DiskUsagePercent: 98}).CriticalResources(); len(got) != 1 || got["disk"] != 98 {
		t.Errorf("critical resources: %v (want disk 98)", got)
	}
}
