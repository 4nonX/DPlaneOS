package groups

import (
	"errors"
	"reflect"
	"testing"
)

func TestLatestCommon(t *testing.T) {
	local := []string{"dplane-1-1", "dplane-1-2", "dplane-1-3"}
	if got := latestCommon(local, []string{"dplane-1-1", "dplane-1-2"}); got != "dplane-1-2" {
		t.Errorf("got %q", got)
	}
	if got := latestCommon(local, nil); got != "" {
		t.Errorf("no common: %q", got)
	}
	if got := latestCommon(local, []string{"dplane-9-9"}); got != "" {
		t.Errorf("unrelated: %q", got)
	}
}

func TestPruneList(t *testing.T) {
	local := []string{"s1", "s2", "s3", "s4", "s5"}
	if got := pruneList(local, 3, nil); !reflect.DeepEqual(got, []string{"s1", "s2"}) {
		t.Errorf("got %v", got)
	}
	// s1 is still the base of a lagging target: kept.
	if got := pruneList(local, 3, map[string]bool{"s1": true}); !reflect.DeepEqual(got, []string{"s2"}) {
		t.Errorf("needed: %v", got)
	}
}

func TestCheckReceive(t *testing.T) {
	g := Group{Name: "data", Topology: Replicated, Pools: []PoolRef{{Name: "tank"}}, Candidates: []string{"a", "b"},
		Owner: "a", Epoch: 4, Version: 1}
	ok := func(err error) bool { return err == nil }
	cases := []struct {
		name               string
		self, sender       string
		epoch              int64
		base               string
		written            int64
		datasets           int
		discard, want      bool
		diverged, notOwner bool
	}{
		{name: "normal incremental", self: "b", sender: "a", epoch: 4, base: "dplane-4-1", want: true},
		{name: "stale owner", self: "b", sender: "a", epoch: 3, base: "dplane-3-1", notOwner: true},
		{name: "not the owner", self: "b", sender: "c", epoch: 4, notOwner: true},
		{name: "owner does not receive", self: "a", sender: "b", epoch: 4},
		{name: "newer epoch not learned yet", self: "b", sender: "a", epoch: 5},
		{name: "diverged", self: "b", sender: "a", epoch: 4, base: "dplane-4-1", written: 4096, diverged: true},
		{name: "diverged, operator discards", self: "b", sender: "a", epoch: 4, base: "dplane-4-1", written: 4096, discard: true, want: true},
		{name: "first full send into empty copy", self: "b", sender: "a", epoch: 4, datasets: 1, want: true},
		{name: "first full send would replace data", self: "b", sender: "a", epoch: 4, datasets: 3, diverged: true},
	}
	for _, c := range cases {
		if c.datasets == 0 {
			c.datasets = 1
		}
		err := checkReceive(g, c.self, c.sender, c.epoch, c.base, c.written, c.datasets, c.discard)
		if ok(err) != c.want {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.want)
		}
		if c.diverged && !errors.Is(err, ErrDiverged) {
			t.Errorf("%s: want ErrDiverged, got %v", c.name, err)
		}
		if c.notOwner && !errors.Is(err, ErrNotOwnerSender) {
			t.Errorf("%s: want ErrNotOwnerSender, got %v", c.name, err)
		}
	}
}
