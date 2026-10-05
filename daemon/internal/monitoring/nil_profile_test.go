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
	if _, ok := c.checks["storage_monitoring"]; !ok {
		t.Error("expected the S.M.A.R.T. fallback check without a profile")
	}
	if _, ok := c.checks["bmc"]; ok {
		t.Error("bmc check registered without a BMC")
	}
	c.Check(context.Background()) // what the background loop runs every 5s
}
