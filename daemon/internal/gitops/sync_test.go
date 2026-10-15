package gitops

import "testing"

func TestFullyApplied(t *testing.T) {
	if !FullyApplied(&ApplyResult{Applied: []string{"create dataset tank/a"}}) {
		t.Error("plain apply should count as fully applied")
	}
	if FullyApplied(&ApplyResult{Applied: []string{"[DEFERRED no-quorum] create pool tank"}}) {
		t.Error("deferred pool operation must not count")
	}
	if FullyApplied(&ApplyResult{Failed: "tank/b"}) {
		t.Error("failed apply must not count")
	}
}
