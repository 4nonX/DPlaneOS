package handlers

import (
	"strings"
	"testing"
)

func TestCreateZvol(t *testing.T) {
	cmds := fakeCommands(t, nil)
	for name, body := range map[string]map[string]any{
		"no pool":          {"name": "lun0", "size": "10G"},
		"bad size":         {"name": "tank/lun0", "size": "10 G"},
		"bad compression":  {"name": "tank/lun0", "size": "10G", "compression": "lz4,mountpoint=/"},
		"bad block size":   {"name": "tank/lun0", "size": "10G", "blocksize": "3"},
		"option injection": {"name": "tank/lun0 -o x", "size": "10G"},
	} {
		if r := call(t, CreateZvol, req{method: "POST", body: body}); r.code != 400 {
			t.Errorf("%s: %s", name, r)
		}
	}
	if len(cmds.keys()) != 0 {
		t.Fatalf("zfs ran for invalid requests: %v", cmds.keys())
	}
	r := call(t, CreateZvol, req{method: "POST", body: map[string]any{"name": "tank/lun0", "size": "10G", "sparse": true, "compression": "zstd-3", "blocksize": "16K"}})
	if !r.ok() {
		t.Fatalf("create: %s", r)
	}
	if !cmds.ran("zfs", "create", "-s", "-V", "10G", "-b", "16K", "-o", "compression=zstd-3", "tank/lun0") {
		t.Errorf("zfs create: %v", cmds.keys())
	}
}

func TestResizeZvolRefusesShrink(t *testing.T) {
	cmds := fakeCommands(t, map[string]func([]string) ([]byte, error){
		"zfs": func(a []string) ([]byte, error) {
			if a[0] == "get" {
				return []byte("10737418240\n"), nil // 10G
			}
			return nil, nil
		},
	})
	if r := call(t, ResizeZvol, req{method: "POST", body: map[string]any{"name": "tank/lun0", "size": "5G"}}); r.code != 409 {
		t.Errorf("shrink: %s", r)
	}
	if cmds.ran("zfs", "set") {
		t.Fatal("volsize changed on a refused shrink")
	}
	if r := call(t, ResizeZvol, req{method: "POST", body: map[string]any{"name": "tank/lun0", "size": "20G"}}); !r.ok() {
		t.Errorf("grow: %s", r)
	}
	if !cmds.ran("zfs", "set", "volsize=20G", "tank/lun0") {
		t.Errorf("grow not applied: %v", cmds.keys())
	}
	if r := call(t, ResizeZvol, req{method: "POST", body: map[string]any{"name": "tank/lun0", "size": "5G", "allow_shrink": true}}); !r.ok() {
		t.Errorf("explicit shrink: %s", r)
	}
}

func TestResizeZvolUnknown(t *testing.T) {
	fakeCommands(t, map[string]func([]string) ([]byte, error){"zfs": fail("dataset does not exist")})
	if r := call(t, ResizeZvol, req{method: "POST", body: map[string]any{"name": "tank/nope", "size": "5G"}}); r.code != 404 {
		t.Errorf("unknown zvol: %s", r)
	}
}

func TestDestroyZvol(t *testing.T) {
	cmds := fakeCommands(t, nil)
	if r := call(t, DestroyZvol, req{method: "DELETE", body: map[string]any{"name": "tank"}}); r.code != 400 {
		t.Errorf("a pool is not a zvol: %s", r)
	}
	if r := call(t, DestroyZvol, req{method: "DELETE", body: map[string]any{"name": "tank/lun0"}}); !r.ok() {
		t.Fatalf("destroy: %s", r)
	}
	if !cmds.ran("zfs", "destroy", "tank/lun0") || cmds.ran("zfs", "destroy", "-f") {
		t.Errorf("destroy args: %v", cmds.keys())
	}
	busy := fakeCommands(t, map[string]func([]string) ([]byte, error){"zfs": fail("dataset is busy")})
	if r := call(t, DestroyZvol, req{method: "DELETE", body: map[string]any{"name": "tank/lun0"}}); r.code != 500 || !strings.Contains(r.raw, "Failed") {
		t.Errorf("busy zvol: %s (%v)", r, busy.keys())
	}
}
