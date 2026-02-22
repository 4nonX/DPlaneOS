package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MountGuard prevents writes to unmounted paths
type MountGuard struct {
	guardFiles map[string]string
	mutex      sync.RWMutex
}

const guardFileName = ".dplaneos_mount_guard"

func NewMountGuard() *MountGuard {
	return &MountGuard{
		guardFiles: make(map[string]string),
	}
}

func (g *MountGuard) RegisterPath(path string) error {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}
	
	guardPath := filepath.Join(absPath, guardFileName)
	
	content := fmt.Sprintf("D-PlaneOS Mount Guard\nCreated: %s\nPath: %s\n", 
		time.Now().Format(time.RFC3339), absPath)
	
	if err := os.WriteFile(guardPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to create guard file: %w", err)
	}
	
	g.guardFiles[absPath] = guardPath
	return nil
}

func (g *MountGuard) CheckMounted(path string) error {
	g.mutex.RLock()
	defer g.mutex.RUnlock()
	
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}
	
	guardPath, exists := g.guardFiles[absPath]
	if !exists {
		return fmt.Errorf("path not registered: %s", absPath)
	}
	
	if _, err := os.Stat(guardPath); os.IsNotExist(err) {
		return fmt.Errorf("CRITICAL: Mount guard missing - filesystem unmounted at: %s", absPath)
	}
	
	return nil
}

func (g *MountGuard) SafeWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := g.CheckMounted(dir); err != nil {
		return err
	}
	
	return os.WriteFile(path, data, perm)
}

func (g *MountGuard) VerifyAll() []error {
	g.mutex.RLock()
	defer g.mutex.RUnlock()
	
	var errors []error
	
	for path := range g.guardFiles {
		if err := g.CheckMounted(path); err != nil {
			errors = append(errors, err)
		}
	}
	
	return errors
}
