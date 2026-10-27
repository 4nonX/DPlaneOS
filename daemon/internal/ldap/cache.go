package ldap

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// DirectoryCache is an in-memory cache of LDAP users and their group memberships,
// populated by a background refresh goroutine. It serves two purposes:
//
//  1. Performance: NSS-style lookups on every file stat() would require a round
//     trip to the directory server. The cache satisfies these from memory.
//  2. Resilience: when the directory server is temporarily unreachable (network
//     glitch, DC rebooting), authentication and role mapping continue to work
//     against the last-known-good snapshot.
//
// Mirrors TrueNAS's DSCacheFill pattern in plugins/directoryservices_/cache.py,
// but extends it with on-disk persistence so the warm cache survives daemon
// restarts and is available immediately before the first successful LDAP refresh.
type DirectoryCache struct {
	mu          sync.RWMutex
	users       map[string]*User    // key: lowercase username
	groups      map[string][]string // key: lowercase group CN, value: member usernames
	lastRefresh time.Time
	ttl         time.Duration
	stale       bool // true if last refresh failed
	persistPath string

	stop chan struct{}
}

// cacheSnapshot is the on-disk format for persistent cache storage.
type cacheSnapshot struct {
	Users       []*User             `json:"users"`
	Groups      map[string][]string `json:"groups"`
	RefreshedAt time.Time           `json:"refreshed_at"`
}

// NewDirectoryCache creates a cache with the given TTL. Call Start() to begin
// background refresh.
func NewDirectoryCache(ttl time.Duration) *DirectoryCache {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &DirectoryCache{
		users:  make(map[string]*User),
		groups: make(map[string][]string),
		ttl:    ttl,
		stop:   make(chan struct{}),
	}
}

// NewDirectoryCacheWithPersistence creates a cache that automatically saves
// successful refresh snapshots to path and preloads the last snapshot on
// creation. This means authentication remains available immediately after a
// daemon restart, even before the first LDAP refresh completes.
func NewDirectoryCacheWithPersistence(ttl time.Duration, path string) *DirectoryCache {
	c := NewDirectoryCache(ttl)
	c.persistPath = path
	if err := c.loadSnapshot(path); err != nil && !os.IsNotExist(err) {
		log.Printf("LDAP CACHE: could not load snapshot from %s: %v", path, err)
	}
	return c
}

// Start launches a background goroutine that refreshes the cache every ttl/2.
// The refresh uses the provided fetch function which should perform the actual
// LDAP query. Call Stop() to shut down the goroutine.
func (c *DirectoryCache) Start(fetch func() ([]*User, error)) {
	go func() {
		// Initial fill on startup
		c.refresh(fetch)

		// Subsequent refreshes at half the TTL so cached data never expires
		// during normal operation (refresh happens before the TTL is reached)
		ticker := time.NewTicker(c.ttl / 2)
		defer ticker.Stop()
		for {
			select {
			case <-c.stop:
				return
			case <-ticker.C:
				c.refresh(fetch)
			}
		}
	}()
}

// Stop shuts down the background refresh goroutine.
func (c *DirectoryCache) Stop() {
	close(c.stop)
}

// refresh calls fetch and atomically replaces the cached data.
func (c *DirectoryCache) refresh(fetch func() ([]*User, error)) {
	users, err := fetch()
	if err != nil {
		c.mu.Lock()
		c.stale = true
		c.mu.Unlock()
		log.Printf("LDAP CACHE: refresh failed (%v); continuing to serve stale data", err)
		return
	}

	newUsers := make(map[string]*User, len(users))
	newGroups := make(map[string][]string)

	for _, u := range users {
		key := lowerASCII(u.Username)
		newUsers[key] = u
		for _, g := range u.Groups {
			gk := lowerASCII(g)
			newGroups[gk] = append(newGroups[gk], u.Username)
		}
	}

	now := time.Now()
	c.mu.Lock()
	c.users = newUsers
	c.groups = newGroups
	c.lastRefresh = now
	c.stale = false
	c.mu.Unlock()

	log.Printf("LDAP CACHE: refreshed %d users, %d groups", len(newUsers), len(newGroups))

	if c.persistPath != "" {
		if err := c.persistSnapshot(users, newGroups, now); err != nil {
			log.Printf("LDAP CACHE: snapshot write failed: %v", err)
		}
	}
}

// persistSnapshot writes a JSON snapshot of the current cache to disk.
// Uses atomic temp-file + rename so the on-disk snapshot is always either
// the previous complete version or the new complete version - never a partial
// write. This matters on NixOS where /var/lib/dplaneos/ may be a ZFS dataset:
// if a pool export races with this write, the rename will fail with ENOENT or
// EXDEV (if the mount was torn down), which is handled gracefully - we log and
// continue serving from the in-memory cache.
func (c *DirectoryCache) persistSnapshot(users []*User, groups map[string][]string, at time.Time) error {
	snap := cacheSnapshot{Users: users, Groups: groups, RefreshedAt: at}
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	tmp := c.persistPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.persistPath); err != nil {
		// Clean up the temp file on rename failure.
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	return nil
}

// loadSnapshot reads a previously persisted snapshot and preloads the cache.
// This is called only from NewDirectoryCacheWithPersistence before the first refresh.
func (c *DirectoryCache) loadSnapshot(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var snap cacheSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return err
	}

	newUsers := make(map[string]*User, len(snap.Users))
	for _, u := range snap.Users {
		newUsers[lowerASCII(u.Username)] = u
	}
	groups := snap.Groups
	if groups == nil {
		groups = make(map[string][]string)
	}

	c.mu.Lock()
	c.users = newUsers
	c.groups = groups
	c.lastRefresh = snap.RefreshedAt
	c.stale = true // still stale until next live refresh confirms the directory is reachable
	c.mu.Unlock()

	log.Printf("LDAP CACHE: preloaded snapshot: %d users, %d groups (from %s)",
		len(newUsers), len(groups), snap.RefreshedAt.Format(time.RFC3339))
	return nil
}

// GetUser returns a cached user by username. Returns nil, false if not cached.
func (c *DirectoryCache) GetUser(username string) (*User, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	u, ok := c.users[lowerASCII(username)]
	return u, ok
}

// GetGroupMembers returns the cached member list for a group.
func (c *DirectoryCache) GetGroupMembers(groupCN string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	members, ok := c.groups[lowerASCII(groupCN)]
	return members, ok
}

// AllUsers returns a snapshot of all cached users.
func (c *DirectoryCache) AllUsers() []*User {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*User, 0, len(c.users))
	for _, u := range c.users {
		out = append(out, u)
	}
	return out
}

// IsStale returns true if the last refresh failed (directory unreachable).
// Callers can surface this as a warning without blocking authentication.
func (c *DirectoryCache) IsStale() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stale
}

// LastRefresh returns the time of the last successful cache fill.
func (c *DirectoryCache) LastRefresh() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastRefresh
}

// Expired returns true if the cache has never been filled or the TTL has elapsed
// since the last successful fill. When expired, callers should attempt a live query
// instead of serving cached data.
func (c *DirectoryCache) Expired() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.lastRefresh.IsZero() {
		return true
	}
	return time.Since(c.lastRefresh) > c.ttl
}

// lowerASCII returns a lowercase version of s restricted to ASCII.
// Avoids importing unicode packages for simple case folding on identifiers.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
