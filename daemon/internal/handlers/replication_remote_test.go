package handlers

import (
	"testing"
)

func TestSSHHostAndUserValidation(t *testing.T) {
	for _, ok := range []string{"backup.lan", "10.0.0.5", "fd00::5", "[fd00::5]", "nas-2"} {
		if !isValidSSHHost(ok) {
			t.Errorf("host %q rejected", ok)
		}
	}
	for _, bad := range []string{"-oProxyCommand=sh", "a b", "host;id", "", "$(id)", ".hidden"} {
		if isValidSSHHost(bad) {
			t.Errorf("host %q accepted", bad)
		}
	}
	if isValidSSHUser("-oProxyCommand") || isValidSSHUser(".x") || !isValidSSHUser("backup") {
		t.Error("ssh user validation")
	}
}

func TestReplicateToRemoteValidation(t *testing.T) {
	h := NewReplicationHandler()
	cmds := fakeCommands(t, nil)
	base := func(extra map[string]any) map[string]any {
		b := map[string]any{"snapshot": "tank/data@daily-1", "remote_host": "backup.lan", "remote_pool": "backup"}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	for name, body := range map[string]map[string]any{
		"option host":        base(map[string]any{"remote_host": "-oProxyCommand=touch /tmp/x"}),
		"option user":        base(map[string]any{"remote_user": "-oProxyCommand"}),
		"bad base snapshot":  base(map[string]any{"incremental": true, "base_snapshot": "-R"}),
		"other dataset base": base(map[string]any{"incremental": true, "base_snapshot": "tank/other@x"}),
		"bad rate limit":     base(map[string]any{"rate_limit": "10M -q"}),
		"bad key path":       base(map[string]any{"ssh_key_path": "-oProxyCommand=sh"}),
		"relative key":       base(map[string]any{"ssh_key_path": "id_rsa"}),
		"bad snapshot":       base(map[string]any{"snapshot": "-R@x"}),
		"bad pool":           base(map[string]any{"remote_pool": "-o"}),
		"bad port":           base(map[string]any{"remote_port": 70000}),
	} {
		if r := call(t, h.ReplicateToRemote, req{method: "POST", body: body}); r.code != 400 {
			t.Errorf("%s: %s", name, r)
		}
	}
	if len(cmds.keys()) != 0 {
		t.Errorf("commands ran for invalid requests: %v", cmds.keys())
	}
}

// Without a snapshot the latest one of the dataset itself is used, not the
// newest snapshot of a child dataset.
func TestReplicateToRemoteLatestSnapshotOfDatasetOnly(t *testing.T) {
	h := NewReplicationHandler()
	cmds := fakeCommands(t, map[string]func([]string) ([]byte, error){
		"zfs": out("tank/data@daily-1\ntank/data@daily-2\n"),
	})
	// Port 1 on localhost: the (real) transfer job fails fast in the background.
	r := call(t, h.ReplicateToRemote, req{method: "POST", body: map[string]any{
		"source_dataset": "tank/data", "remote_host": "127.0.0.1", "remote_port": 1, "remote_pool": "backup"}})
	if !r.ok() {
		t.Fatalf("replicate: %s", r)
	}
	if !cmds.ran("zfs", "list", "-t", "snapshot", "-H", "-o", "name", "-s", "creation", "-d", "1", "tank/data") {
		t.Errorf("snapshot lookup: %v", cmds.keys())
	}
}

func TestApplyNetworkWithRollbackPath(t *testing.T) {
	for _, p := range []string{"/etc/shadow", "/etc/netplan/../shadow.yaml", "/etc/netplan/x.conf", ""} {
		if r := call(t, ApplyNetworkWithRollback, req{method: "POST", body: map[string]any{"config_path": p, "new_config": "x"}}); r.code != 400 {
			t.Errorf("%q: %s", p, r)
		}
	}
}
