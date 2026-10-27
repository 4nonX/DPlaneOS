// Package resilience provides fault tolerance patterns.
// Phase 2.2: Circuit breakers for external system failures.
package resilience

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// State represents the circuit breaker state.
type State string

const (
	StateClosed   State = "closed"    // Normal operation
	StateOpen     State = "open"      // Failing; rejecting requests
	StateHalfOpen State = "half-open" // Testing recovery
)

// CircuitBreaker prevents cascading failures from external services.
// Transitions: Closed → Open (after failures) → HalfOpen (after timeout) → Closed (if recovery succeeds)
type CircuitBreaker struct {
	mu              sync.RWMutex
	state           State
	failureCount    int32
	successCount    int32
	lastFailureTime time.Time
	lastOpenTime    time.Time

	// Configuration
	name             string
	failureThreshold int32         // Open after N failures
	successThreshold int32         // Close after N successes in HalfOpen
	timeout          time.Duration // Transition from Open to HalfOpen
	onStateChange    func(State)
	log              func(string, ...interface{})
}

// NewCircuitBreaker creates a new circuit breaker.
func NewCircuitBreaker(name string, failureThreshold int32, successThreshold int32, timeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		name:             name,
		state:            StateClosed,
		failureThreshold: failureThreshold,
		successThreshold: successThreshold,
		timeout:          timeout,
		log: func(msg string, args ...interface{}) {
			log.Printf("[CIRCUIT-BREAKER:%s] "+msg, append([]interface{}{name}, args...)...)
		},
	}
}

// SetStateChangeCallback sets a callback when state changes.
func (cb *CircuitBreaker) SetStateChangeCallback(fn func(State)) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.onStateChange = fn
}

// Call executes a function through the circuit breaker.
// Returns error if circuit is Open or if the function fails.
func (cb *CircuitBreaker) Call(fn func() error) error {
	state := cb.GetState()

	switch state {
	case StateOpen:
		// Check if timeout has elapsed; if so, transition to HalfOpen
		if time.Since(cb.lastOpenTime) > cb.timeout {
			cb.setState(StateHalfOpen)
			cb.log("Transitioning to HalfOpen after timeout")
			// Fall through to HalfOpen logic
		} else {
			return fmt.Errorf("circuit breaker open; service unavailable")
		}
		fallthrough

	case StateHalfOpen:
		// In half-open state: allow one request through
		err := fn()
		if err != nil {
			cb.recordFailure()
			return err
		}
		// Request succeeded; increment success counter
		successes := atomic.AddInt32(&cb.successCount, 1)
		if successes >= cb.successThreshold {
			cb.setState(StateClosed)
			atomic.StoreInt32(&cb.failureCount, 0)
			atomic.StoreInt32(&cb.successCount, 0)
			cb.log("Circuit closed; service recovered")
		}
		return nil

	case StateClosed:
		// Normal operation
		err := fn()
		if err != nil {
			cb.recordFailure()
			return err
		}
		// Reset failure counter on success
		atomic.StoreInt32(&cb.failureCount, 0)
		return nil

	default:
		return fmt.Errorf("unknown circuit breaker state: %s", state)
	}
}

// recordFailure increments failure count and opens circuit if threshold reached.
func (cb *CircuitBreaker) recordFailure() {
	failures := atomic.AddInt32(&cb.failureCount, 1)
	atomic.StoreInt32(&cb.successCount, 0) // Reset success counter

	cb.mu.Lock()
	cb.lastFailureTime = time.Now()
	cb.mu.Unlock()

	if failures >= cb.failureThreshold {
		cb.setState(StateOpen)
		cb.log("Circuit opened after %d failures", failures)
	}
}

// GetState returns the current state.
func (cb *CircuitBreaker) GetState() State {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// GetStats returns circuit breaker statistics.
func (cb *CircuitBreaker) GetStats() map[string]interface{} {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return map[string]interface{}{
		"state":               cb.state,
		"failure_count":       atomic.LoadInt32(&cb.failureCount),
		"success_count":       atomic.LoadInt32(&cb.successCount),
		"last_failure_time":   cb.lastFailureTime,
		"last_open_time":      cb.lastOpenTime,
		"time_until_recovery": cb.timeout - time.Since(cb.lastOpenTime),
	}
}

// setState transitions to a new state and triggers callback.
func (cb *CircuitBreaker) setState(newState State) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == newState {
		return // No state change
	}

	oldState := cb.state
	cb.state = newState
	if newState == StateOpen {
		cb.lastOpenTime = time.Now()
	}

	cb.log("State transition: %s → %s", oldState, newState)
	if cb.onStateChange != nil {
		go cb.onStateChange(newState)
	}
}

// Reset manually resets the circuit breaker to Closed state.
func (cb *CircuitBreaker) Reset() {
	cb.setState(StateClosed)
	atomic.StoreInt32(&cb.failureCount, 0)
	atomic.StoreInt32(&cb.successCount, 0)
	cb.log("Circuit breaker reset")
}

// Pool manages multiple circuit breakers for different external services.
type Pool struct {
	mu       sync.RWMutex
	breakers map[string]*CircuitBreaker
	log      func(string, ...interface{})
}

// NewPool creates a new circuit breaker pool.
func NewPool() *Pool {
	return &Pool{
		breakers: make(map[string]*CircuitBreaker),
		log: func(msg string, args ...interface{}) {
			log.Printf("[CIRCUIT-BREAKER-POOL] "+msg, args...)
		},
	}
}

// Register registers a circuit breaker in the pool.
func (p *Pool) Register(name string, breaker *CircuitBreaker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.breakers[name] = breaker
	p.log("Registered circuit breaker: %s", name)
}

// Get returns a circuit breaker by name, or nil if not found.
func (p *Pool) Get(name string) *CircuitBreaker {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.breakers[name]
}

// GetAll returns all circuit breaker stats.
func (p *Pool) GetAll() map[string]map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make(map[string]map[string]interface{})
	for name, breaker := range p.breakers {
		result[name] = breaker.GetStats()
	}
	return result
}

// Reset resets all circuit breakers.
func (p *Pool) Reset() {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, breaker := range p.breakers {
		breaker.Reset()
	}
	p.log("Reset all circuit breakers")
}
