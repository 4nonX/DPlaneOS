package ha

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A graceful daemon stop disarms the watchdog (magic 'V') unless another node
// relies on this one resetting itself. Before, it never did: with self-fencing
// on, `systemctl stop dplaned` reset even a standalone node.
func TestKeeperStop(t *testing.T) {
	for _, c := range []struct {
		name       string
		q          ExternalQuorum
		owns       bool
		armed      bool
		wantDisarm bool
	}{
		{"no cluster", ExternalQuorum{}, true, false, true},
		{"cluster, owns nothing", ExternalQuorum{Configured: true, Quorate: true}, false, false, true},
		{"cluster, owns storage", ExternalQuorum{Configured: true, Quorate: true}, true, false, false},
		{"lost quorum, fence armed", ExternalQuorum{Configured: true}, true, true, false},
	} {
		dev := filepath.Join(t.TempDir(), "watchdog")
		if err := os.WriteFile(dev, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := openWatchdog(dev); err != nil {
			t.Fatal(err)
		}
		q, owns := c.q, c.owns
		k := NewKeeper(nil, func() ExternalQuorum { return q }, func() bool { return owns })
		k.opened, k.stop, k.armed, k.timeout = true, make(chan struct{}), c.armed, 30
		k.Stop()
		b, _ := os.ReadFile(dev)
		if disarmed := strings.HasSuffix(string(b), "V"); disarmed != c.wantDisarm {
			t.Errorf("%s: disarmed=%v, want %v", c.name, disarmed, c.wantDisarm)
		}
		closeWatchdog() // no-op when Stop closed it
	}
}

func TestKeeperMayRun(t *testing.T) {
	for _, c := range []struct {
		name string
		q    ExternalQuorum
		owns bool
		want bool
	}{
		{"no cluster", ExternalQuorum{}, true, true},
		{"quorate owner", ExternalQuorum{Configured: true, Quorate: true}, true, true},
		{"reconfiguring owner", ExternalQuorum{Configured: true, Reconfiguring: true}, true, true},
		{"isolated node without storage", ExternalQuorum{Configured: true}, false, true},
		{"isolated storage owner", ExternalQuorum{Configured: true}, true, false},
	} {
		q, owns := c.q, c.owns
		k := NewKeeper(nil, func() ExternalQuorum { return q }, func() bool { return owns })
		if got := k.mayRun(); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
