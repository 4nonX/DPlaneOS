package handlers

import (
	"strconv"
	"strings"
	"testing"

	"dplaned/internal/security"
)

func can(t *testing.T, uid int, res, act string) bool {
	t.Helper()
	ok, err := security.UserHasPermission(uid, res, act)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// A user created on the Users page gets the permissions of the chosen role.
func TestUserRoleGrantsPermissions(t *testing.T) {
	e := newUserEnv(t)
	create := func(name, role string) int {
		r := e.action(t, "root-admin", map[string]any{"action": "create", "username": name, "password": "Some-Passw0rd!", "role": role, "confirm_password": "Admin-Passw0rd"})
		if !r.ok() {
			t.Fatalf("create %s: %s", name, r)
		}
		return int(r.body["id"].(float64))
	}
	op := create("opsy", "operator")
	if !can(t, op, "storage", "write") || !can(t, op, "docker", "write") || can(t, op, "users", "write") || can(t, op, "system", "admin") {
		t.Error("operator permissions")
	}
	viewer := create("vic", "viewer")
	if !can(t, viewer, "storage", "read") || can(t, viewer, "storage", "write") || can(t, viewer, "audit", "read") {
		t.Error("viewer permissions")
	}
	usr := create("ursula", "user")
	if !can(t, usr, "files", "write") || can(t, usr, "storage", "write") {
		t.Error("user permissions")
	}
	// Changing the role replaces the permissions.
	if r := e.action(t, "root-admin", map[string]any{"action": "update", "id": viewer, "role": "operator", "confirm_password": "Admin-Passw0rd"}); !r.ok() {
		t.Fatalf("role change: %s", r)
	}
	if !can(t, viewer, "storage", "write") {
		t.Error("role change not applied")
	}
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = $1 AND r.name = 'viewer'`, viewer).Scan(&n)
	if n != 0 {
		t.Error("old role kept")
	}
}

func TestRoleEditing(t *testing.T) {
	db := testDB(t)
	_ = db
	// What the role editor sends: name, description, permissions.
	r := call(t, HandleCreateRole, req{method: "POST", body: map[string]any{"name": "backup-ops", "description": "Snapshots", "permissions": []string{"snapshots:read", "snapshots:write"}}})
	if r.code != 201 {
		t.Fatalf("create role: %s", r)
	}
	if r := call(t, HandleCreateRole, req{method: "POST", body: map[string]any{"name": "bad", "permissions": []string{"storage:fly"}}}); r.code != 400 {
		t.Errorf("unknown permission: %s", r)
	}
	if r := call(t, HandleCreateRole, req{method: "POST", body: map[string]any{"name": "Bad Name"}}); r.code != 400 {
		t.Errorf("bad role name: %s", r)
	}
	list := call(t, HandleListRoles, req{})
	if !strings.Contains(list.raw, `"permissions":["snapshots:read","snapshots:write"]`) {
		t.Fatalf("role list: %s", list)
	}
	var id, adminID int
	_ = db.QueryRow(`SELECT id FROM roles WHERE name = 'backup-ops'`).Scan(&id)
	_ = db.QueryRow(`SELECT id FROM roles WHERE name = 'admin'`).Scan(&adminID)
	if r := call(t, HandleUpdateRole, req{method: "PUT", vars: map[string]string{"id": strconv.Itoa(id)}, body: map[string]any{"name": "backup-ops", "permissions": []string{"snapshots:read"}}}); !r.ok() {
		t.Fatalf("update role: %s", r)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM role_permissions WHERE role_id = $1`, id).Scan(&n)
	if n != 1 {
		t.Errorf("permissions after update: %d", n)
	}
	if r := call(t, HandleUpdateRole, req{method: "PUT", vars: map[string]string{"id": strconv.Itoa(adminID)}, body: map[string]any{"permissions": []string{}}}); r.code != 400 {
		t.Errorf("emptying the admin role: %s", r)
	}
}

// A role can be given for a time: it ends by itself.
func TestRoleAssignmentExpires(t *testing.T) {
	e := newUserEnv(t)
	r := e.action(t, "root-admin", map[string]any{"action": "create", "username": "temp", "password": "Some-Passw0rd!", "role": "viewer", "confirm_password": "Admin-Passw0rd"})
	if !r.ok() {
		t.Fatalf("create: %s", r)
	}
	uid := int(r.body["id"].(float64))
	var operator int
	if err := e.db.QueryRow(`SELECT id FROM roles WHERE name = 'operator'`).Scan(&operator); err != nil {
		t.Fatal(err)
	}
	until := "2099-01-01T00:00:00Z"
	if err := security.AssignRoleToUser(uid, operator, nil, &until); err != nil {
		t.Fatal(err)
	}
	if !can(t, uid, "storage", "write") {
		t.Fatal("the role given for a time does not count")
	}
	roles, err := security.GetUserRoles(uid)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, role := range roles {
		if role.Name == "operator" {
			found = role.ExpiresAt != nil && strings.HasPrefix(*role.ExpiresAt, "2099-01-01")
		}
	}
	if !found {
		t.Errorf("the end of the assignment is not reported: %+v", roles)
	}
	// Its time is over.
	if _, err := e.db.Exec(`UPDATE user_roles SET expires_at = NOW() - INTERVAL '1 minute' WHERE user_id = $1 AND role_id = $2`, uid, operator); err != nil {
		t.Fatal(err)
	}
	security.InvalidateUserPermissions(uid)
	if can(t, uid, "storage", "write") || !can(t, uid, "storage", "read") {
		t.Error("an expired role still counts (or the account role was lost with it)")
	}
}
