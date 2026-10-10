package handlers

import (
	"database/sql"
	"reflect"
	"testing"
)

func TestMappedRoles(t *testing.T) {
	m := map[string][]string{"nas-admins": {"admin"}, "staff": {"user", "operator"}}
	got := mappedRoles([]string{"Staff", "NAS-Admins", "other"}, m)
	if !reflect.DeepEqual(got, []string{"user", "operator", "admin"}) {
		t.Errorf("mapped roles %v", got)
	}
	if mappedRoles(nil, m) != nil {
		t.Error("no groups, no roles")
	}
}

func TestLDAPMappingsAndRoleSync(t *testing.T) {
	db := testDB(t)
	seedRoles(t, db)
	h := NewLDAPHandler(db)

	if r := call(t, h.AddMapping, req{method: "POST", body: map[string]any{"ldap_group": "x", "role_name": "power_user"}}); r.code != 400 {
		t.Errorf("unknown role accepted: %s", r)
	}
	if r := call(t, h.AddMapping, req{method: "POST", body: map[string]any{"ldap_group": "nas-admins", "role_name": "admin"}}); !r.ok() {
		t.Fatalf("add mapping: %s", r)
	}
	var roleID *int64
	_ = db.QueryRow(`SELECT role_id FROM ldap_group_mappings WHERE ldap_group = 'nas-admins'`).Scan(&roleID)
	if roleID == nil {
		t.Error("role_id not stored")
	}

	// An LDAP user gets the mapped role, keeps admin-granted roles, and
	// loses LDAP-granted roles of groups they left.
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, role, source, active) VALUES ('carol', '', 'user', 'ldap', 1)`); err != nil {
		t.Fatal(err)
	}
	var uid int64
	_ = db.QueryRow(`SELECT id FROM users WHERE username = 'carol'`).Scan(&uid)
	if _, err := db.Exec(`INSERT INTO user_roles (user_id, role_id, granted_by) SELECT $1, id, 'admin' FROM roles WHERE name = 'viewer'`, uid); err != nil {
		t.Fatal(err)
	}
	if err := h.applyLDAPRoles("carol", []string{"admin", "operator"}); err != nil {
		t.Fatal(err)
	}
	if got := userRoleNames(t, db, uid); !reflect.DeepEqual(got, []string{"admin", "operator", "viewer"}) {
		t.Errorf("after sync: %v", got)
	}
	if err := h.applyLDAPRoles("carol", []string{"operator"}); err != nil {
		t.Fatal(err)
	}
	if got := userRoleNames(t, db, uid); !reflect.DeepEqual(got, []string{"operator", "viewer"}) {
		t.Errorf("left the admin group: %v", got)
	}
	if err := h.applyLDAPRoles("carol", []string{"nope"}); err == nil {
		t.Error("unknown role not reported")
	}
}

// seedRoles creates the built-in RBAC roles (cmd/dplaned seeds them at
// startup; test schemas only have the migrations).
func seedRoles(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, r := range []string{"admin", "operator", "user", "viewer"} {
		if _, err := db.Exec(`INSERT INTO roles (name, display_name, description, is_system) VALUES ($1, $1, '', 1) ON CONFLICT (name) DO NOTHING`, r); err != nil {
			t.Fatal(err)
		}
	}
}

func userRoleNames(t *testing.T, db *sql.DB, uid int64) []string {
	t.Helper()
	rows, err := db.Query(`SELECT r.name FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = $1 ORDER BY r.name`, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out
}
