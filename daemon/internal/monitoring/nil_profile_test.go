package monitoring

import (
	"context"
	"testing"

	"dplaned/internal/resilience"
)

// The bootstrap creates the checker without a hardware profile; this panicked
// at daemon start (nil dereference in InitializeDefaultChecks).
func TestInitializeDefaultChecksWithoutProfile(t *testing.T) {
	c := NewChecker(nil, resilience.NewPool(), nil, nil)
	c.InitializeDefaultChecks()
	for _, name := range []string{"resources", "zfs", "docker", "postgres", "network"} {
		if _, ok := c.checks[name]; !ok {
			t.Errorf("check %s not registered", name)
		}
	}
	// No placeholders that can only ever report "unknown".
	for _, name := range []string{"bmc", "enclosure", "storage_monitoring", "vlan"} {
		if _, ok := c.checks[name]; ok {
			t.Errorf("placeholder check %s registered", name)
		}
	}
	c.Check(context.Background()) // what the background loop runs every 5s
}
