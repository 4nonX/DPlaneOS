package quorum

import (
	"strings"
	"testing"
	"time"
)

func twoNodes() Config {
	return Config{ClusterName: "dplane-a1b2", Nodes: []Node{
		{ID: 1, Name: "nas1", Addr: "10.0.0.1"},
		{ID: 2, Name: "nas2", Addr: "10.0.0.2"},
	}}
}

func TestRenderTwoNodeWithoutThirdVote(t *testing.T) {
	c := twoNodes()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	out := Render(c)
	for _, want := range []string{"cluster_name: dplane-a1b2", "transport: knet", "ring0_addr: 10.0.0.2", "two_node: 1", "crypto_cipher: aes256"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "device {") {
		t.Error("no quorum device expected")
	}
	if c.ExpectedVotes() != 2 {
		t.Errorf("expected votes %d", c.ExpectedVotes())
	}
}

func TestRenderWithThirdVote(t *testing.T) {
	c := twoNodes()
	c.QDevice = &QDevice{Host: "10.0.0.9", Algorithm: DefaultAlgorithm(2)}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	out := Render(c)
	if strings.Contains(out, "two_node") {
		t.Error("two_node must not be combined with a quorum device")
	}
	for _, want := range []string{"model: net", "votes: 1", "tls: on", "host: 10.0.0.9", "algorithm: ffsplit"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "      port:") {
		t.Error("default port should not be written")
	}
	if c.ExpectedVotes() != 3 {
		t.Errorf("expected votes %d", c.ExpectedVotes())
	}
}

func TestValidate(t *testing.T) {
	bad := []Config{
		{ClusterName: "x", Nodes: []Node{{ID: 1, Name: "a", Addr: "10.0.0.1"}}},
		{ClusterName: "bad name", Nodes: twoNodes().Nodes},
		{ClusterName: "x", Nodes: []Node{{ID: 1, Name: "a", Addr: "10.0.0.1"}, {ID: 1, Name: "b", Addr: "10.0.0.2"}}},
		{ClusterName: "x", Nodes: []Node{{ID: 1, Name: "a", Addr: "10.0.0.1"}, {ID: 2, Name: "b", Addr: "10.0.0.1"}}},
		{ClusterName: "x", Nodes: []Node{{ID: 1, Name: "a", Addr: "nas1.lan"}, {ID: 2, Name: "b", Addr: "10.0.0.2"}}},
		{ClusterName: "x", Nodes: twoNodes().Nodes, QDevice: &QDevice{Host: "h;rm -rf", Algorithm: "ffsplit"}},
		{ClusterName: "x", Nodes: twoNodes().Nodes, QDevice: &QDevice{Host: "h", Algorithm: "magic"}},
	}
	for i, c := range bad {
		if c.Validate() == nil {
			t.Errorf("case %d: invalid config accepted", i)
		}
	}
}

const quorumtoolTwoNode = `Quorum information
------------------
Date:             Mon Oct  5 21:10:11 2026
Quorum provider:  corosync_votequorum
Nodes:            2
Node ID:          1
Ring ID:          1.1d
Quorate:          Yes

Votequorum information
----------------------
Expected votes:   2
Highest expected: 2
Total votes:      2
Quorum:           1
Flags:            2Node Quorate WaitForAll

Membership information
----------------------
    Nodeid      Votes Name
         1          1 10.0.0.1 (local)
         2          1 10.0.0.2
`

const quorumtoolQdevice = `Quorum information
------------------
Date:             Mon Oct  5 21:12:40 2026
Quorum provider:  corosync_votequorum
Nodes:            1
Node ID:          1
Ring ID:          1.21
Quorate:          Yes

Votequorum information
----------------------
Expected votes:   3
Highest expected: 3
Total votes:      2
Quorum:           2
Flags:            Quorate Qdevice

Membership information
----------------------
    Nodeid      Votes    Qdevice Name
         1          1    A,V,NMW 10.0.0.1 (local)
         0          1            Qdevice
`

const quorumtoolLost = `Quorum information
------------------
Date:             Mon Oct  5 21:13:00 2026
Quorum provider:  corosync_votequorum
Nodes:            1
Node ID:          2
Ring ID:          2.22
Quorate:          No

Votequorum information
----------------------
Expected votes:   3
Highest expected: 3
Total votes:      1
Quorum:           2 Activity blocked
Flags:            Qdevice

Membership information
----------------------
    Nodeid      Votes    Qdevice Name
         2          1   A,NV,NMW 10.0.0.2 (local)
         0          0            Qdevice (votes 1)
`

func TestParseQuorumtool(t *testing.T) {
	s := ParseQuorumtool(quorumtoolTwoNode)
	if !s.Running || !s.Quorate || s.ExpectedVotes != 2 || s.TotalVotes != 2 || s.QuorumVotes != 1 || !s.HasFlag("2Node") {
		t.Errorf("two-node: %+v", s)
	}
	if len(s.Members) != 2 || !s.Members[0].Local || s.Members[1].Name != "10.0.0.2" || s.QDeviceAlive {
		t.Errorf("two-node members: %+v", s.Members)
	}

	q := ParseQuorumtool(quorumtoolQdevice)
	if !q.Quorate || q.ExpectedVotes != 3 || q.TotalVotes != 2 || !q.QDeviceAlive || q.QDeviceVotes != 1 {
		t.Errorf("qdevice: %+v", q)
	}
	if len(q.Members) != 1 || q.Members[0].Qdevice != "A,V,NMW" || q.Members[0].Name != "10.0.0.1" {
		t.Errorf("qdevice members: %+v", q.Members)
	}

	l := ParseQuorumtool(quorumtoolLost)
	if l.Quorate || l.QuorumVotes != 2 || l.QDeviceAlive || l.LocalNodeID != 2 {
		t.Errorf("lost: %+v", l)
	}
	// Real corosync output (VM test): the non-voting device row is not a member.
	if len(l.Members) != 1 || l.Members[0].NodeID != 2 {
		t.Errorf("lost members: %+v", l.Members)
	}

	if ParseQuorumtool("Cannot initialize QUORUM service").Running {
		t.Error("not running expected")
	}
}

// Certificate requests and certificates from NSS are binary (DER).
func TestEnrollmentEncodingIsBinarySafe(t *testing.T) {
	der := []byte{0x30, 0x82, 0x01, 0x0a, 0x00, 0xff, '\n', 'B', 'E', 'G', 'I', 'N'}
	resp := FormatCAResponse("dplane-abc123", der)
	first, rest, _ := strings.Cut(resp, "\n")
	if first != "cluster: dplane-abc123" {
		t.Fatalf("first line %q", first)
	}
	got, err := DecodeBase64([]byte(rest))
	if err != nil || string(got) != string(der) {
		t.Fatalf("round trip: %v %x", err, got)
	}
	// base64(1) wraps lines at 76 characters.
	wrapped := "MIIBCgD/\nCkJFR0lO\n"
	if b, err := DecodeBase64([]byte(wrapped)); err != nil || len(b) != 12 {
		t.Fatalf("wrapped: %v %x", err, b)
	}
	if _, err := DecodeBase64([]byte("\n")); err == nil {
		t.Error("empty input must fail")
	}
}

func TestReconfigurationEndsWhenSettled(t *testing.T) {
	c := twoNodes()
	c.Nodes[0].NodeKey, c.Nodes[1].NodeKey = "ka", "kb"
	c.QDevice = &QDevice{Host: "w", Algorithm: "ffsplit"}
	m := &Monitor{cfg: &c, st: Status{Running: true, Quorate: true, ExpectedVotes: 3, QDeviceAlive: false,
		Members: []Member{{NodeID: 1, Name: "nas1", Local: true}}}}
	markReconfiguring()
	if in := m.Info(); !in.Reconfiguring {
		t.Fatal("third vote not voting yet: still reconfiguring")
	}
	m.st.QDeviceAlive = true
	if in := m.Info(); in.Reconfiguring || !in.AutoFailover {
		t.Fatalf("settled: %+v", in)
	}
	if in := m.Info(); len(in.Online) != 1 || in.Online[0] != "ka" {
		t.Fatalf("online: %+v", in.Online)
	}
}

func TestReconfiguringBlocksAutoFailover(t *testing.T) {
	m := &Monitor{cfg: func() *Config { c := twoNodes(); c.QDevice = &QDevice{Host: "w", Algorithm: "ffsplit"}; return &c }(),
		st: Status{Running: true, Quorate: true, ExpectedVotes: 3}}
	if in := m.Info(); !in.AutoFailover {
		t.Fatalf("expected auto failover: %+v", in)
	}
	markReconfiguring()
	defer func() { reconfigMu.Lock(); reconfigUntil = time.Time{}; reconfigMu.Unlock() }()
	if in := m.Info(); in.AutoFailover || !in.Reconfiguring || !strings.Contains(in.AutoFailoverNo, "being changed") {
		t.Fatalf("reconfiguration must block automatic failover: %+v", in)
	}
	// Two votes: the lasting reason is shown, not the restart.
	two := &Monitor{cfg: func() *Config { c := twoNodes(); return &c }(), st: Status{Running: true, Quorate: true, ExpectedVotes: 2}}
	if in := two.Info(); !strings.Contains(in.AutoFailoverNo, "third vote") {
		t.Fatalf("reason: %+v", in)
	}
}
