package hasplit

import (
	"net"
	"testing"
)

const cluster = `{"members":[
 {"name":"dplaneos-192.168.1.1","role":"leader","state":"running","host":"192.168.1.1","port":5432,"timeline":3},
 {"name":"dplaneos-192.168.1.2","role":"replica","state":"streaming","host":"192.168.1.2","port":5432,"timeline":3,"lag":0}],
 "scope":"dplaneos"}`

func TestParseClusterAndPreflight(t *testing.T) {
	ms, err := ParseCluster([]byte(cluster))
	if err != nil || len(ms) != 2 {
		t.Fatalf("%v %v", ms, err)
	}
	me, other, problems := Preflight(ms, "dplaneos-192.168.1.2")
	if len(problems) != 0 || me.Leader() || !other.Leader() || other.Host != "192.168.1.1" {
		t.Fatalf("%+v %+v %v", me, other, problems)
	}
	if !CaughtUp(ms) {
		t.Error("lag 0 is caught up")
	}
	ms[1].Lag = 4096
	if CaughtUp(ms) {
		t.Error("lag 4096 is not caught up")
	}
	lagged, _ := ParseCluster([]byte(`{"members":[{"name":"a","role":"leader","state":"running","host":"10.0.0.1"},{"name":"b","role":"replica","state":"streaming","host":"10.0.0.2","lag":"unknown"}]}`))
	if CaughtUp(lagged) {
		t.Error("unknown lag is not caught up")
	}
	_, _, problems = Preflight(ms[:1], "dplaneos-192.168.1.1")
	if len(problems) == 0 {
		t.Error("one member must be refused")
	}
	stopped, _ := ParseCluster([]byte(`{"members":[{"name":"a","role":"leader","state":"running","host":"10.0.0.1"},{"name":"b","role":"replica","state":"stopped","host":"10.0.0.2"}]}`))
	if _, _, p := Preflight(stopped, "a"); len(p) == 0 {
		t.Error("a stopped replica must be refused")
	}
}

func TestMaySwitch(t *testing.T) {
	ms, _ := ParseCluster([]byte(cluster))
	if ok, _ := MaySwitch(false, ms, "ready"); !ok {
		t.Error("caught-up replica switches first")
	}
	if ok, _ := MaySwitch(true, ms, "ready"); ok {
		t.Error("leader waits for the replica")
	}
	if ok, _ := MaySwitch(true, ms, "switching"); !ok {
		t.Error("leader switches once the replica is switching")
	}
	ms[1].Lag = 10
	if ok, _ := MaySwitch(false, ms, "ready"); ok {
		t.Error("replica waits until caught up")
	}
}

func TestKeepalived(t *testing.T) {
	conf := `vrrp_script check_dplaneos { script "curl" }
vrrp_instance dplaneos_vip {
  interface enp1s0
  state MASTER
  virtual_router_id 51
  priority 100
  virtual_ipaddress {
    192.168.10.50
  }
}`
	vip, iface := ParseKeepalived(conf)
	if vip != "192.168.10.50" || iface != "enp1s0" {
		t.Fatalf("%q %q", vip, iface)
	}
	_, n, _ := net.ParseCIDR("192.168.10.7/23")
	if got := WithPrefix(vip, []*net.IPNet{n}); got != "192.168.10.50/23" {
		t.Errorf("got %q", got)
	}
	if got := WithPrefix("10.1.1.1/16", nil); got != "10.1.1.1/16" {
		t.Errorf("got %q", got)
	}
	if v, i := ParseKeepalived("global_defs {}"); v != "" || i != "" {
		t.Errorf("no instance: %q %q", v, i)
	}
}

func TestTopology(t *testing.T) {
	if Topology("etcd3:\n  hosts: 10.0.0.1:2379,10.0.0.2:2379,10.0.0.1:2381") != "shared" {
		t.Error("co-located witness is shared storage")
	}
	if Topology("etcd3:\n  hosts: 10.0.0.1:2379,10.0.0.2:2379,10.0.0.3:2379") != "replicated" {
		t.Error("separate witness is replicated")
	}
}
