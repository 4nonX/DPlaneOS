package quorum

import (
	"fmt"
	"strings"
	"testing"
)

func threeWithVoter() Config {
	return Config{ClusterName: "dplane-abc", Nodes: []Node{
		{ID: 1, Name: "a", Addr: "10.0.0.1", NodeKey: "ka"},
		{ID: 2, Name: "b", Addr: "10.0.0.2", NodeKey: "kb"},
		{ID: 3, Name: "pi", Addr: "10.0.0.3", Voter: true},
	}}
}

func TestMembersAndValidate(t *testing.T) {
	c := threeWithVoter()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	m := c.Members("ka")
	if len(m) != 1 || m[0].Name != "b" {
		t.Errorf("members of a: %+v (voters get no pushes)", m)
	}
	if c.DPlaneNodes() != 2 || c.nextID() != 4 || c.ExpectedVotes() != 3 {
		t.Errorf("counts: %d %d %d", c.DPlaneNodes(), c.nextID(), c.ExpectedVotes())
	}
	one := Config{ClusterName: "x", Nodes: []Node{{ID: 1, Name: "a", Addr: "10.0.0.1", NodeKey: "ka"}, {ID: 2, Name: "pi", Addr: "10.0.0.3", Voter: true}}}
	if one.Validate() == nil {
		t.Error("one DPlaneOS node and a voter is not a cluster")
	}
	bad := threeWithVoter()
	bad.Nodes[2].NodeKey = "kp"
	if bad.Validate() == nil {
		t.Error("a voter cannot be a paired node")
	}
}

func TestRenderVoterCluster(t *testing.T) {
	conf := Render(threeWithVoter())
	if strings.Contains(conf, "two_node") {
		t.Error("three voting members: no two_node")
	}
	if !strings.Contains(conf, "ring0_addr: 10.0.0.3") {
		t.Error("the voter is a corosync member")
	}
	two := threeWithVoter()
	two.Nodes = two.Nodes[:2]
	if !strings.Contains(Render(two), "two_node: 1") {
		t.Error("two nodes without a third vote: two_node")
	}
}

func TestVoterText(t *testing.T) {
	j := voterJoin(threeWithVoter(), []byte("0123456789"))
	j.Token = "dpv_x"
	txt := j.Text()
	for _, want := range []string{"cluster: dplane-abc\n", "token: dpv_x\n", "nodes: 10.0.0.1 10.0.0.2\n", "authkey: MDEyMzQ1Njc4OQ==\n", "---\n", "totem {"} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q in\n%s", want, txt)
		}
	}
	if strings.Contains(Render(threeWithVoter()), "dpv_") {
		t.Error("tokens are never rendered")
	}
}

func TestAddressFamilies(t *testing.T) {
	mixed := threeWithVoter()
	mixed.Nodes[2].Addr = "fd00::3"
	if mixed.Validate() == nil {
		t.Error("IPv4 and IPv6 members must be refused")
	}
	c := threeWithVoter()
	if a, err := voterAddr(c, "10.0.0.9", "fd00::9"); err != nil || a != "10.0.0.9" {
		t.Errorf("IPv4 cluster: %q %v", a, err)
	}
	v6 := threeWithVoter()
	for i := range v6.Nodes {
		v6.Nodes[i].Addr = fmt.Sprintf("fd00::%d", i+1)
	}
	if a, err := voterAddr(v6, "10.0.0.9", "fd00::9"); err != nil || a != "fd00::9" {
		t.Errorf("IPv6 cluster: %q %v", a, err)
	}
	if _, err := voterAddr(v6, "10.0.0.9", ""); err == nil {
		t.Error("an IPv6 cluster needs the voter's IPv6 address")
	}
}
