//go:build linux

// Package persistguard monitors durable storage (/persist) and proactively trims
// logs so etcd/journal/PostgreSQL cannot fill the partition and break quorum.
package persistguard

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

const (
	checkEvery    = 2 * time.Minute
	warnPct       = 80.0
	criticalPct   = 88.0
	journalVacuum = "256M"
)

// Start launches a background loop. Call once from main.
func Start() {
	go loop()
}

func loop() {
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()
	for range ticker.C {
		runOnce()
	}
}

func runOnce() {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/persist", &st); err != nil {
		return
	}
	bavail := uint64(st.Bavail) * uint64(st.Bsize)
	blocks := uint64(st.Blocks) * uint64(st.Bsize)
	if blocks == 0 {
		return
	}
	usedPct := 100.0 * float64(blocks-bavail) / float64(blocks)
	if usedPct < warnPct {
		return
	}

	log.Printf("persistguard: /persist %.1f%% full — running log hygiene", usedPct)

	if usedPct >= criticalPct {
		vacuumJournal()
		truncateLargestN("/var/log/dplaneos", 6)
		truncateLargestN("/persist/log", 6)
		truncateLargestN("/var/log/samba", 4)
	} else {
		vacuumJournal()
		truncateLargestN("/var/log/dplaneos", 3)
		truncateLargestN("/persist/log", 3)
	}
}

func vacuumJournal() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "journalctl", "--vacuum-size="+journalVacuum)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("persistguard: journalctl vacuum: %v %s", err, string(out))
	}
}

type fileEnt struct {
	path string
	size int64
}

// truncateLargestN zeros out up to n largest regular files in dir (frees space fast).
func truncateLargestN(dir string, n int) {
	if n <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []fileEnt
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if !fi.Mode().IsRegular() || fi.Size() == 0 {
			continue
		}
		files = append(files, fileEnt{path: p, size: fi.Size()})
	}
	if len(files) == 0 {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].size > files[j].size })
	for i := 0; i < n && i < len(files); i++ {
		f := files[i]
		if err := os.Truncate(f.path, 0); err != nil {
			log.Printf("persistguard: truncate %s: %v", f.path, err)
		} else {
			log.Printf("persistguard: truncated %s (was %d bytes)", f.path, f.size)
		}
	}
}
