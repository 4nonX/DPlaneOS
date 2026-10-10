package handlers

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestMayManage(t *testing.T) {
	for _, c := range []struct {
		req, target string
		want        bool
	}{
		{"admin", "admin", true},
		{"admin", "user", true},
		{"user", "user", false},
		{"user", "admin", false},
		{"", "user", false},
	} {
		if got := mayManage(c.req, c.target); got != c.want {
			t.Errorf("mayManage(%q, %q) = %v", c.req, c.target, got)
		}
	}
}

// userEnv: an admin (the requester by default) and a regular user.
type userEnv struct {
	db    *sql.DB
	h     *UserGroupHandler
	admin int
	user  int
}

func newUserEnv(t *testing.T) userEnv {
	db := testDB(t)
	e := userEnv{db: db, h: NewUserGroupHandler(db)}
	e.admin = addUser(t, db, "root-admin", "Admin-Passw0rd", "admin", true)
	e.user = addUser(t, db, "regular", "Regular-Passw0rd", "user", true)
	return e
}

func (e userEnv) action(t *testing.T, as string, body map[string]any) resp {
	return call(t, e.h.HandleUsers, req{method: "POST", user: as, body: body})
}

func TestUserCreate(t *testing.T) {
	e := newUserEnv(t)
	base := func(extra map[string]any) map[string]any {
		b := map[string]any{"action": "create", "username": "newbie", "password": "Newbie-Passw0rd", "confirm_password": "Admin-Passw0rd"}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	if r := e.action(t, "root-admin", base(map[string]any{"confirm_password": ""})); r.code != 400 {
		t.Errorf("without confirmation: %s", r)
	}
	if r := e.action(t, "root-admin", base(map[string]any{"confirm_password": "wrong"})); r.code != 403 {
		t.Errorf("wrong confirmation: %s", r)
	}
	if r := e.action(t, "root-admin", base(map[string]any{"password": "weak"})); r.code != 400 {
		t.Errorf("weak password: %s", r)
	}
	if r := e.action(t, "root-admin", base(map[string]any{"username": "bad name;"})); r.code != 400 {
		t.Errorf("bad username: %s", r)
	}
	if r := e.action(t, "root-admin", base(map[string]any{"role": "superuser"})); r.code != 400 {
		t.Errorf("unknown role: %s", r)
	}
	if r := e.action(t, "root-admin", base(nil)); !r.ok() {
		t.Fatalf("create: %s", r)
	}
	var hash, salt, role string
	_ = e.db.QueryRow(`SELECT password_hash, scram_salt, role FROM users WHERE username = 'newbie'`).Scan(&hash, &salt, &role)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("Newbie-Passw0rd")) != nil || salt == "" || role != "user" {
		t.Errorf("stored user: role %q, scram %v", role, salt != "")
	}
	if r := e.action(t, "root-admin", base(nil)); r.code != 409 {
		t.Errorf("duplicate: %s", r)
	}
}

func TestUserHierarchy(t *testing.T) {
	e := newUserEnv(t)
	// A non-admin cannot create an admin (or an account of its own rank).
	r := e.action(t, "regular", map[string]any{"action": "create", "username": "sneaky", "password": "Sneaky-Passw0rd", "role": "admin", "confirm_password": "Regular-Passw0rd"})
	if r.code != 403 {
		t.Errorf("non-admin creating an admin: %s", r)
	}
	// ... cannot change an admin ...
	r = e.action(t, "regular", map[string]any{"action": "update", "id": e.admin, "active": false, "confirm_password": "Regular-Passw0rd"})
	if r.code != 403 {
		t.Errorf("non-admin deactivating an admin: %s", r)
	}
	// ... and cannot raise its own role.
	r = e.action(t, "regular", map[string]any{"action": "update", "id": e.user, "role": "admin", "confirm_password": "Regular-Passw0rd"})
	if r.code != 403 {
		t.Errorf("raising own role: %s", r)
	}
	var role string
	_ = e.db.QueryRow(`SELECT role FROM users WHERE id = $1`, e.user).Scan(&role)
	if role != "user" {
		t.Fatalf("role changed to %q", role)
	}
}

func TestLastAdminIsProtected(t *testing.T) {
	e := newUserEnv(t)
	// Only root-admin is an active admin (plus the seeded admin, if any:
	// deactivate the others so root-admin is the last one).
	if _, err := e.db.Exec(`UPDATE users SET active = 0 WHERE role = 'admin' AND id <> $1`, e.admin); err != nil {
		t.Fatal(err)
	}
	r := e.action(t, "root-admin", map[string]any{"action": "update", "id": e.admin, "active": false, "confirm_password": "Admin-Passw0rd"})
	if r.code != 403 {
		t.Errorf("deactivating the last admin: %s", r)
	}
}

func TestUserDelete(t *testing.T) {
	e := newUserEnv(t)
	if _, err := e.db.Exec(`INSERT INTO sessions (session_id, user_id, username, created_at, expires_at) VALUES ('s1', $1, 'regular', 0, 9999999999)`, e.user); err != nil {
		t.Fatal(err)
	}
	if r := e.action(t, "root-admin", map[string]any{"action": "delete", "id": e.user, "confirm_password": "Admin-Passw0rd"}); !r.ok() {
		t.Fatalf("delete: %s", r)
	}
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE username = 'regular'`).Scan(&n)
	if n != 0 {
		t.Error("sessions of the deleted user remain")
	}
	if r := e.action(t, "root-admin", map[string]any{"action": "delete", "id": 999999, "confirm_password": "Admin-Passw0rd"}); r.code == 200 {
		t.Errorf("unknown user: %s", r)
	}
	protected := addUser(t, e.db, "admin", "Admin-Passw0rd", "user", true)
	if r := e.action(t, "root-admin", map[string]any{"action": "delete", "id": protected, "confirm_password": "Admin-Passw0rd"}); r.code != 403 {
		t.Errorf("protected account name: %s", r)
	}
	daemon := addUser(t, e.db, "www-data", "Admin-Passw0rd", "user", true)
	if r := e.action(t, "root-admin", map[string]any{"action": "delete", "id": daemon, "confirm_password": "Admin-Passw0rd"}); r.code != 403 {
		t.Errorf("system account: %s", r)
	}
}

func TestResetUserPassword(t *testing.T) {
	e := newUserEnv(t)
	reset := func(as string, id int, pw string) resp {
		return call(t, e.h.ResetUserPassword, req{method: "POST", user: as, vars: map[string]string{"id": strconv.Itoa(id)}, body: map[string]any{"temp_password": pw}})
	}
	if r := reset("root-admin", e.user, "weak"); r.code != 400 {
		t.Errorf("weak temp password: %s", r)
	}
	// Taking over an admin account through a reset is refused for non-admins.
	if r := reset("regular", e.admin, "Temp-Passw0rd!"); r.code != 403 {
		t.Errorf("non-admin resetting an admin's password: %s", r)
	}
	if r := reset("root-admin", e.user, "Temp-Passw0rd!"); !r.ok() {
		t.Fatalf("reset: %s", r)
	}
	var mustChange int
	_ = e.db.QueryRow(`SELECT must_change_password FROM users WHERE id = $1`, e.user).Scan(&mustChange)
	if mustChange != 1 {
		t.Error("a reset password must be changed on next login")
	}
	if r := call(t, e.h.ResetUserPassword, req{method: "POST", vars: map[string]string{"id": "x"}, body: map[string]any{}}); r.code != 400 {
		t.Errorf("bad id: %s", r)
	}
}

func TestGroups(t *testing.T) {
	e := newUserEnv(t)
	g := func(body map[string]any) resp {
		body["confirm_password"] = "Admin-Passw0rd"
		return call(t, e.h.HandleGroups, req{method: "POST", user: "root-admin", body: body})
	}
	if r := g(map[string]any{"action": "create", "name": "../etc"}); r.code != 400 {
		t.Errorf("bad group name: %s", r)
	}
	r := g(map[string]any{"action": "create", "name": "media", "description": "Media team"})
	if !r.ok() {
		t.Fatalf("create group: %s", r)
	}
	id := int(r.body["id"].(float64))
	if r := g(map[string]any{"action": "create", "name": "media"}); r.code != 409 {
		t.Errorf("duplicate group: %s", r)
	}
	if r := g(map[string]any{"action": "update", "id": id, "members": []int{e.user}}); !r.ok() {
		t.Fatalf("members: %s", r)
	}
	list := call(t, e.h.HandleGroups, req{user: "root-admin"})
	if !list.ok() || !strings.Contains(list.raw, "media") {
		t.Errorf("list groups: %s", list)
	}
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM group_members WHERE group_name = 'media' AND username = 'regular'`).Scan(&n)
	if n != 1 {
		t.Error("member not stored")
	}
	if r := g(map[string]any{"action": "delete", "id": id}); !r.ok() {
		t.Fatalf("delete group: %s", r)
	}
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM group_members WHERE group_name = 'media'`).Scan(&n)
	if n != 0 {
		t.Error("members of a deleted group remain")
	}
	if r := call(t, e.h.HandleGroups, req{method: "POST", user: "root-admin", body: map[string]any{"action": "create", "name": "x"}}); r.code != 400 {
		t.Errorf("group change without confirmation: %s", r)
	}
}

// What the Users page sends: member usernames, groups identified by name.
func TestGroupsWebUIShape(t *testing.T) {
	e := newUserEnv(t)
	g := func(body map[string]any) resp {
		body["confirm_password"] = "Admin-Passw0rd"
		return call(t, e.h.HandleGroups, req{method: "POST", user: "root-admin", body: body})
	}
	if r := g(map[string]any{"action": "create", "name": "media", "members": []string{"regular", "nobody-here"}}); r.code != 400 {
		t.Errorf("unknown member: %s", r)
	}
	if r := g(map[string]any{"action": "create", "name": "media", "gid": 3000, "members": []string{"regular"}}); !r.ok() {
		t.Fatalf("create with members: %s", r)
	}
	list := call(t, e.h.HandleGroups, req{user: "root-admin"})
	if !strings.Contains(list.raw, `"members":["regular"]`) {
		t.Errorf("list must show member names: %s", list)
	}
	if r := g(map[string]any{"action": "update", "name": "media", "members": []string{"regular", "root-admin"}}); !r.ok() {
		t.Fatalf("update by name: %s", r)
	}
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM group_members WHERE group_name = 'media'`).Scan(&n)
	if n != 2 {
		t.Errorf("members after update: %d", n)
	}
	// Rename (by id): memberships follow.
	var id int
	_ = e.db.QueryRow(`SELECT id FROM groups WHERE name = 'media'`).Scan(&id)
	if r := g(map[string]any{"action": "update", "id": id, "name": "video"}); !r.ok() {
		t.Fatalf("rename: %s", r)
	}
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM group_members WHERE group_name = 'video'`).Scan(&n)
	if n != 2 {
		t.Errorf("members lost on rename: %d", n)
	}
	if r := g(map[string]any{"action": "delete", "name": "video"}); !r.ok() {
		t.Fatalf("delete by name: %s", r)
	}
	if r := g(map[string]any{"action": "delete", "name": "video"}); r.code != 404 {
		t.Errorf("delete again: %s", r)
	}
}
