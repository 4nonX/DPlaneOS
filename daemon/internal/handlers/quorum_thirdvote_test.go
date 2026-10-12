package handlers

import (
	"errors"
	"strings"
	"testing"
	"time"

	"dplaned/internal/quorum"
)

// The third-vote test says what it found: reachable is not the same as
// voting, and a missing member is named.
func TestThirdVoteSteps(t *testing.T) {
	old := dialTCP
	t.Cleanup(func() { dialTCP = old })
	reach := true
	var dialled string
	dialTCP = func(addr string, _ time.Duration) error {
		dialled = addr
		if reach {
			return nil
		}
		return errors.New("connection refused")
	}
	allOK := func(steps []voteStep) bool {
		for _, s := range steps {
			if !s.OK {
				return false
			}
		}
		return len(steps) > 0
	}
	pair := []quorum.Node{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
	withQ := &quorum.Config{ClusterName: "c", Nodes: pair, QDevice: &quorum.QDevice{Host: "10.0.0.9"}}

	kind, steps := thirdVoteSteps(withQ, quorum.Status{Running: true, QDeviceAlive: true, TotalVotes: 3, ExpectedVotes: 3}, "")
	if kind != "qdevice" || !allOK(steps) || dialled != "10.0.0.9:5403" {
		t.Errorf("voting vote server: %s %+v (dialled %s)", kind, steps, dialled)
	}
	// Reachable, but not voting: not OK, and it says which of the two.
	_, steps = thirdVoteSteps(withQ, quorum.Status{Running: true, TotalVotes: 2, ExpectedVotes: 3}, "")
	if allOK(steps) || !steps[0].OK || steps[1].OK {
		t.Errorf("reachable but not voting: %+v", steps)
	}
	reach = false
	_, steps = thirdVoteSteps(withQ, quorum.Status{Running: true}, "")
	if steps[0].OK || !strings.Contains(steps[0].Detail, "connection refused") {
		t.Errorf("unreachable: %+v", steps)
	}

	three := &quorum.Config{ClusterName: "c", Nodes: append(pair, quorum.Node{ID: 3, Name: "pi", Voter: true})}
	members := []quorum.Member{{Name: "a"}, {Name: "b"}, {Name: "pi"}}
	if kind, steps := thirdVoteSteps(three, quorum.Status{Running: true, Members: members}, ""); kind != "member" || !allOK(steps) {
		t.Errorf("three members: %s %+v", kind, steps)
	}
	if _, steps := thirdVoteSteps(three, quorum.Status{Running: true, Members: members[:2]}, ""); allOK(steps) || !strings.Contains(steps[0].Detail, "pi") {
		t.Errorf("the missing voter is not named: %+v", steps)
	}
	if _, steps := thirdVoteSteps(&quorum.Config{ClusterName: "c", Nodes: pair}, quorum.Status{Running: true, Members: members[:2]}, ""); allOK(steps) {
		t.Errorf("two votes passed as a third vote: %+v", steps)
	}
	if kind, steps := thirdVoteSteps(nil, quorum.Status{}, ""); kind != "none" || allOK(steps) {
		t.Errorf("no cluster: %s %+v", kind, steps)
	}
}
