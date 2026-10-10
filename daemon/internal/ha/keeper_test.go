package ha

import "testing"

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
