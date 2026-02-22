package handlers

import (
	"net/http"
	"sync"
	"time"
)

// ═══════════════════════════════════════════════════════════════
//  LDAP CIRCUIT BREAKER
//  Prevents the web UI from hanging when LDAP is slow/unreachable
// ═══════════════════════════════════════════════════════════════

// CircuitState represents the state of the circuit breaker
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // Normal operation
	CircuitOpen                         // LDAP failed, using cache
	CircuitHalfOpen                     // Testing if LDAP recovered
)

// LDAPCircuitBreaker protects against slow/dead LDAP servers
type LDAPCircuitBreaker struct {
	mu              sync.RWMutex
	state           CircuitState
	failures        int
	lastFailure     time.Time
	lastSuccess     time.Time
	maxFailures     int           // trips to Open after this many failures
	resetTimeout    time.Duration // time before trying again (half-open)
	requestTimeout  time.Duration // max time to wait for LDAP response
}

// CachedCredential stores a hashed credential for degraded-mode auth
type CachedCredential struct {
	Username     string
	PasswordHash string // bcrypt hash
	Roles        []string
	CachedAt     time.Time
	TTL          time.Duration
}

var (
	ldapBreaker *LDAPCircuitBreaker
	credCache   map[string]*CachedCredential
	cacheMu     sync.RWMutex
)

func init() {
	ldapBreaker = &LDAPCircuitBreaker{
		state:          CircuitClosed,
		maxFailures:    3,
		resetTimeout:   30 * time.Second,
		requestTimeout: 500 * time.Millisecond,
	}
	credCache = make(map[string]*CachedCredential)
}

// State returns the current circuit state
func (cb *LDAPCircuitBreaker) State() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	if cb.state == CircuitOpen {
		// Check if enough time has passed to try again
		if time.Since(cb.lastFailure) > cb.resetTimeout {
			return CircuitHalfOpen
		}
	}
	return cb.state
}

// RecordSuccess records a successful LDAP operation
func (cb *LDAPCircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	cb.state = CircuitClosed
	cb.lastSuccess = time.Now()
}

// RecordFailure records a failed LDAP operation
func (cb *LDAPCircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	cb.lastFailure = time.Now()
	if cb.failures >= cb.maxFailures {
		cb.state = CircuitOpen
	}
}

// CacheCredential stores credentials for degraded-mode auth
func CacheCredential(username, passwordHash string, roles []string) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	credCache[username] = &CachedCredential{
		Username:     username,
		PasswordHash: passwordHash,
		Roles:        roles,
		CachedAt:     time.Now(),
		TTL:          24 * time.Hour, // cached creds valid for 24h
	}
}

// GetCachedCredential returns cached credentials if valid
func GetCachedCredential(username string) *CachedCredential {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	cred, ok := credCache[username]
	if !ok {
		return nil
	}
	if time.Since(cred.CachedAt) > cred.TTL {
		return nil // expired
	}
	return cred
}

// GetCircuitBreakerStatus returns the LDAP circuit breaker status
// GET /api/ldap/circuit-breaker
func GetCircuitBreakerStatus(w http.ResponseWriter, r *http.Request) {
	state := ldapBreaker.State()
	stateStr := "closed"
	switch state {
	case CircuitOpen:
		stateStr = "open"
	case CircuitHalfOpen:
		stateStr = "half-open"
	}

	ldapBreaker.mu.RLock()
	defer ldapBreaker.mu.RUnlock()

	respondOK(w, map[string]interface{}{
		"success":         true,
		"state":           stateStr,
		"failures":        ldapBreaker.failures,
		"max_failures":    ldapBreaker.maxFailures,
		"last_failure":    ldapBreaker.lastFailure.Format(time.RFC3339),
		"last_success":    ldapBreaker.lastSuccess.Format(time.RFC3339),
		"reset_timeout":   ldapBreaker.resetTimeout.String(),
		"request_timeout": ldapBreaker.requestTimeout.String(),
		"cached_users":    len(credCache),
		"degraded_mode":   state == CircuitOpen,
	})
}

// ResetCircuitBreaker manually resets the circuit breaker
// POST /api/ldap/circuit-breaker/reset
func ResetCircuitBreaker(w http.ResponseWriter, r *http.Request) {
	ldapBreaker.mu.Lock()
	ldapBreaker.state = CircuitClosed
	ldapBreaker.failures = 0
	ldapBreaker.mu.Unlock()

	respondOK(w, map[string]interface{}{
		"success": true,
		"message": "Circuit breaker reset to closed state",
	})
}
