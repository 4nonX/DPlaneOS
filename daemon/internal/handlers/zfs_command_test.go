package handlers

import (
	"strings"
	"testing"
)

const testSession = "abcdefghijklmnopqrstuvwxyz012345"

func zfsCommand(t *testing.T, h *ZFSHandler, command string, args ...string) resp {
	t.Helper()
	prev := checkBinary
	checkBinary = func(string) error { return nil }
	defer func() { checkBinary = prev }()
	return call(t, h.HandleCommand, req{method: "POST", user: "admin", body: map[string]any{
		"command": command, "args": args, "session_id": testSession, "user": "admin",
	}})
}

// /api/zfs/command sets properties and destroys datasets; the rest of the
// command whitelist is not reachable through it.
func TestZFSCommandOnlyDatasetCommands(t *testing.T) {
	db := testDB(t)
	cmds := fakeCommands(t, nil)
	h := NewZFSHandler(db)
	for _, c := range []struct {
		command string
		args    []string
	}{
		{"wipefs", []string{"-a", "/dev/sda"}},
		{"zpool_labelclear", []string{"labelclear", "-f", "/dev/sda"}},
		{"zpool_destroy", []string{"destroy", "tank"}},
		{"systemctl", []string{"stop", "dplaned"}},
		{"ls", []string{"/etc/passwd"}},
	} {
		if r := zfsCommand(t, h, c.command, c.args...); r.code != 403 {
			t.Errorf("%s: %s", c.command, r)
		}
	}
	if len(cmds.keys()) != 0 {
		t.Errorf("commands ran: %v", cmds.keys())
	}
}

// A dataset that a share, export, stack or target still uses is not destroyed.
func TestZFSDestroyBlockedWhileInUse(t *testing.T) {
	db := testDB(t)
	h := NewZFSHandler(db)
	cmds := fakeCommands(t, map[string]func([]string) ([]byte, error){
		"zfs": func(args []string) ([]byte, error) {
			if args[0] == "list" {
				return []byte("tank/data\t/mnt/tank/data\ntank/data/photos\t/mnt/tank/data/photos\n"), nil
			}
			return nil, nil
		},
	})
	if _, err := db.Exec(`INSERT INTO smb_shares (name, path) VALUES ('photos', '/mnt/tank/data/photos/2025')`); err != nil {
		t.Fatal(err)
	}
	r := zfsCommand(t, h, "zfs_destroy", "destroy", "-r", "tank/data")
	if r.body["success"] != false || r.body["code"] != "dataset_in_use" || !strings.Contains(r.body["error"].(string), "SMB share") {
		t.Fatalf("destroy with a share below a child dataset: %s", r)
	}
	if cmds.ran("zfs_destroy") || cmds.ran("zfs", "destroy") {
		t.Fatalf("destroy ran: %v", cmds.keys())
	}

	if _, err := db.Exec(`DELETE FROM smb_shares`); err != nil {
		t.Fatal(err)
	}
	if r := zfsCommand(t, h, "zfs_destroy", "destroy", "-r", "tank/data"); !r.ok() {
		t.Fatalf("destroy once nothing uses it: %s", r)
	}
	if !cmds.ran("zfs", "destroy", "-r", "tank/data") {
		t.Errorf("destroy did not run: %v", cmds.keys())
	}
}

// The attachment check itself failing must not let the destroy through.
func TestZFSDestroyBlockedWhenCheckFails(t *testing.T) {
	db := testDB(t)
	h := NewZFSHandler(db)
	cmds := fakeCommands(t, map[string]func([]string) ([]byte, error){
		"zfs": func(args []string) ([]byte, error) {
			if args[0] == "list" {
				return fail("cannot open 'tank/data': I/O error")(args)
			}
			return nil, nil
		},
	})
	if r := zfsCommand(t, h, "zfs_destroy", "destroy", "-r", "tank/data"); r.body["success"] != false {
		t.Fatalf("destroy when the check failed: %s", r)
	}
	if cmds.ran("zfs", "destroy") {
		t.Errorf("destroy ran: %v", cmds.keys())
	}
}

func TestDestroyTarget(t *testing.T) {
	for want, args := range map[string][]string{
		"tank/data": {"destroy", "-r", "tank/data"},
		"tank/vol":  {"destroy", "tank/vol"},
	} {
		if got := destroyTarget(args); got != want {
			t.Errorf("%v: %q", args, got)
		}
	}
	for _, args := range [][]string{{"destroy", "tank/data@snap"}, {"destroy", "tank/data#mark"}, {"set", "quota=1G", "tank/data"}, {"destroy"}} {
		if got := destroyTarget(args); got != "" {
			t.Errorf("%v: %q", args, got)
		}
	}
}
