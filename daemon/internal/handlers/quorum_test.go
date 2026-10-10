package handlers

import (
	"strings"
	"testing"

	"dplaned/internal/quorum"
)

func quorumEnv(t *testing.T) *QuorumHandler {
	db := testDB(t)
	fakeCommands(t, map[string]func([]string) ([]byte, error){
		"corosync_quorumtool": fail("Cannot initialize QUORUM service"),
	})
	return NewQuorumHandler(db, quorum.NewMonitor(db))
}

func TestQuorumStatusUnconfigured(t *testing.T) {
	h := quorumEnv(t)
	r := call(t, h.Status, req{})
	if !r.ok() || r.body["configured"] != false {
		t.Errorf("status: %s", r)
	}
}

func TestQuorumNeedsPairedNode(t *testing.T) {
	h := quorumEnv(t)
	if r := call(t, h.Suggest, req{path: "/api/quorum/suggest?peer_id=nope"}); r.code != 404 {
		t.Errorf("suggest unknown peer: %s", r)
	}
	if r := call(t, h.Form, req{method: "POST", body: map[string]any{"peer_id": "nope", "local_addr": "10.0.0.1", "peer_addr": "10.0.0.2"}}); r.code != 400 || !strings.Contains(r.raw, "Pair the nodes first") {
		t.Errorf("form with an unpaired node: %s", r)
	}
	if r := call(t, h.Form, req{method: "POST", body: map[string]any{}}); r.code != 400 {
		t.Errorf("form without peer: %s", r)
	}
	if r := call(t, h.AddNode, req{method: "POST", body: map[string]any{"peer_id": "nope", "peer_addr": "10.0.0.3"}}); r.code != 400 {
		t.Errorf("add unpaired node: %s", r)
	}
}

// The enrollment endpoints are reachable without a login: a wrong code is
// refused with 401 before anything changes.
func TestQuorumEnrollmentRefusesBadCodes(t *testing.T) {
	h := quorumEnv(t)
	hdr := map[string]string{quorum.HdrCode: "dpq_0000000000000000000000000000000000000000"}
	if r := call(t, h.EnrollCA, req{method: "POST", header: hdr, body: "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n"}); r.code != 401 {
		t.Errorf("enroll CA: %d %s", r.code, r.raw)
	}
	if r := call(t, h.EnrollCA, req{method: "POST", header: hdr, body: "not a certificate"}); r.code != 400 {
		t.Errorf("enroll CA without PEM: %d %s", r.code, r.raw)
	}
	if r := call(t, h.EnrollCert, req{method: "POST", path: "/api/quorum/enroll/cert?address=10.0.0.9", header: hdr, body: "aGVsbG8="}); r.code != 401 {
		t.Errorf("enroll cert: %d %s", r.code, r.raw)
	}
	if r := call(t, h.VoterJoin, req{method: "POST", path: "/api/quorum/voter/join?name=pi&address=10.0.0.9", header: hdr}); r.code != 401 {
		t.Errorf("voter join: %d %s", r.code, r.raw)
	}
	if r := call(t, h.VoterConfig, req{header: map[string]string{"X-DPlane-Voter": "pi", "X-DPlane-Voter-Token": "x"}}); r.code == 200 {
		t.Errorf("voter config without a cluster/token: %d %s", r.code, r.raw)
	}
}

func TestQuorumPeerClusterNeedsPeerAuth(t *testing.T) {
	h := quorumEnv(t)
	if r := call(t, h.PeerCluster, req{method: "POST", header: map[string]string{"X-DPlane-Node": "x", "X-DPlane-Peer-Secret": "y"}, body: map[string]any{}}); r.code != 401 {
		t.Errorf("peer update without pairing: %s", r)
	}
}

func TestQuorumWitnessScriptServed(t *testing.T) {
	h := quorumEnv(t)
	r := call(t, h.WitnessScript, req{})
	if r.code != 200 || !strings.HasPrefix(r.raw, "#!") {
		t.Errorf("witness script: %d %.40q", r.code, r.raw)
	}
}
