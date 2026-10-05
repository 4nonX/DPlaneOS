// Package features provides feature flag system for runtime feature control.
// Experimental/beta features can be safely disabled without data loss.
// Phase 2.3: Foundation for all subsystem readiness gating.
package features

import (
	"sort"
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

// FeatureState represents the readiness level of a feature.
type FeatureState string

const (
	StateDisabled FeatureState = "disabled"
	StateBeta    FeatureState = "beta"
	StateStable  FeatureState = "stable"
	StateDeprecated FeatureState = "deprecated"
)

// Feature represents a feature that can be enabled/disabled.
type Feature struct {
	ID          string       `json:"id"`   // e.g., "ha_clustering", "nvmeof_support"
	Name        string       `json:"name"` // e.g., "HA Clustering"
	Description string       `json:"description"`
	State       FeatureState `json:"state"`
	EnabledAt   *time.Time   `json:"enabled_at,omitempty"`
	DisabledAt  *time.Time   `json:"disabled_at,omitempty"`
	Error       string       `json:"error,omitempty"` // Last error if feature failed
}

// Manager manages feature flags with database persistence.
type Manager struct {
	db        *sql.DB
	mu        sync.RWMutex
	features  map[string]*Feature
	callbacks map[string][]func(Feature) // state change callbacks per feature
	log       func(string, ...interface{})
}

// NewManager creates a feature flag manager.
func NewManager(db *sql.DB) *Manager {
	return &Manager{
		db:        db,
		features:  make(map[string]*Feature),
		callbacks: make(map[string][]func(Feature)),
		log: func(msg string, args ...interface{}) {
			log.Printf("[FEATURES] "+msg, args...)
		},
	}
}

// Register registers a feature with its default state.
func (m *Manager) Register(feature Feature) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.features[feature.ID]; exists {
		return fmt.Errorf("feature %s already registered", feature.ID)
	}

	m.features[feature.ID] = &feature
	m.log("Registered feature %s (state=%s)", feature.ID, feature.State)
	return nil
}

// Get returns the current state of a feature.
func (m *Manager) Get(featureID string) (Feature, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	f, exists := m.features[featureID]
	if !exists {
		return Feature{}, fmt.Errorf("unknown feature: %s", featureID)
	}
	return *f, nil
}

// IsEnabled returns true if feature is enabled (beta or stable).
func (m *Manager) IsEnabled(featureID string) bool {
	f, err := m.Get(featureID)
	if err != nil {
		return false
	}
	return f.State == StateBeta || f.State == StateStable
}

// Enable enables a feature (changes state to beta or stable).
func (m *Manager) Enable(ctx context.Context, featureID, newState string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	f, exists := m.features[featureID]
	if !exists {
		return fmt.Errorf("unknown feature: %s", featureID)
	}

	if newState != string(StateBeta) && newState != string(StateStable) {
		return fmt.Errorf("invalid state for enable: %s", newState)
	}

	now := time.Now()
	f.State = FeatureState(newState)
	f.EnabledAt = &now
	f.DisabledAt = nil
	f.Error = ""

	// Persist to database
	if err := m.persistFeature(ctx, *f); err != nil {
		return fmt.Errorf("failed to persist feature state: %w", err)
	}

	m.log("Enabled feature %s (state=%s)", featureID, newState)
	m.triggerCallbacks(featureID, *f)
	return nil
}

// Disable disables a feature (changes state to disabled).
// Data is never lost; feature can be re-enabled.
func (m *Manager) Disable(ctx context.Context, featureID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	f, exists := m.features[featureID]
	if !exists {
		return fmt.Errorf("unknown feature: %s", featureID)
	}

	now := time.Now()
	f.State = StateDisabled
	f.DisabledAt = &now

	// Persist to database
	if err := m.persistFeature(ctx, *f); err != nil {
		return fmt.Errorf("failed to persist feature state: %w", err)
	}

	m.log("Disabled feature %s", featureID)
	m.triggerCallbacks(featureID, *f)
	return nil
}

// OnStateChange registers a callback when feature state changes.
func (m *Manager) OnStateChange(featureID string, callback func(Feature)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbacks[featureID] = append(m.callbacks[featureID], callback)
}

// LoadFromDB loads feature states from database.
func (m *Manager) LoadFromDB(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, name, COALESCE(description, ''), state, enabled_at, disabled_at, COALESCE(error_msg, '')
		FROM feature_flags
		ORDER BY id
	`)
	if err != nil {
		// Table doesn't exist yet; that's OK (first run)
		if err.Error() == "no such table: feature_flags" {
			m.log("feature_flags table not yet created")
			return nil
		}
		return fmt.Errorf("query feature_flags failed: %w", err)
	}
	defer rows.Close()

	m.mu.Lock()
	defer m.mu.Unlock()

	for rows.Next() {
		var f Feature
		var state string
		if err := rows.Scan(&f.ID, &f.Name, &f.Description, &state, &f.EnabledAt, &f.DisabledAt, &f.Error); err != nil {
			m.log("Error scanning feature row: %v", err)
			continue
		}
		f.State = FeatureState(state)
		m.features[f.ID] = &f
	}

	m.log("Loaded %d features from database", len(m.features))
	return nil
}

func (m *Manager) persistFeature(ctx context.Context, f Feature) error {
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO feature_flags (id, name, description, state, enabled_at, disabled_at, error_msg, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT(id) DO UPDATE SET
			state = $4,
			enabled_at = $5,
			disabled_at = $6,
			error_msg = $7,
			updated_at = $8
	`, f.ID, f.Name, f.Description, f.State, f.EnabledAt, f.DisabledAt, f.Error, time.Now())
	return err
}

func (m *Manager) triggerCallbacks(featureID string, f Feature) {
	cbs, exists := m.callbacks[featureID]
	if !exists {
		return
	}
	for _, cb := range cbs {
		go cb(f)
	}
}

// List returns all registered features.
func (m *Manager) List() []Feature {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Feature, 0, len(m.features))
	for _, f := range m.features {
		result = append(result, *f)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// RegisterBuiltIns registers the built-in optional features (default:
// disabled). States already loaded from the database take precedence.
func RegisterBuiltIns(m *Manager) {
	builtins := []Feature{
		{ID: "ha_clustering", Name: "HA Clustering", Description: "High-availability failover between multiple appliances (requires BMC)", State: StateDisabled},
		{ID: "nvmeof_support", Name: "NVMe-oF Support", Description: "NVMe over Fabrics for remote storage (requires NVMe controllers)", State: StateDisabled},
		{ID: "ses_enclosure", Name: "SES Enclosure Monitoring", Description: "Drive bay temperature and LED control via SES (requires SES hardware)", State: StateDisabled},
		{ID: "ad_integration", Name: "Active Directory Integration", Description: "User/group authentication via Active Directory", State: StateDisabled},
		{ID: "oidc_sso", Name: "OIDC Single Sign-On", Description: "OpenID Connect provider for federation", State: StateDisabled},
		{ID: "lacp_bonding", Name: "LACP Network Bonding", Description: "Link aggregation for redundant network paths", State: StateDisabled},
	}
	for _, f := range builtins {
		_ = m.Register(f) // already loaded from the database: keep the stored state
	}
}
