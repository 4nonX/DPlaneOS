package security

import (
	"fmt"
	"strings"
)

// Role permissions as "resource:action" strings (what the Users page edits).

// RoleSummary is a role with its permissions as strings and its member count.
type RoleSummary struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	IsSystem    bool     `json:"is_system"`
	Permissions []string `json:"permissions"`
	Users       int      `json:"user_count"`
}

// ListRoleSummaries returns all roles with their permissions.
func ListRoleSummaries() ([]RoleSummary, error) {
	rows, err := db.Query(`
		SELECT r.id, r.name, r.display_name, COALESCE(r.description, ''), r.is_system <> 0,
		       COALESCE(string_agg(p.resource || ':' || p.action, ',' ORDER BY p.resource, p.action), ''),
		       (SELECT COUNT(*) FROM user_roles ur WHERE ur.role_id = r.id)
		FROM roles r
		LEFT JOIN role_permissions rp ON rp.role_id = r.id
		LEFT JOIN permissions p ON p.id = rp.permission_id
		GROUP BY r.id
		ORDER BY r.is_system DESC, r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RoleSummary{}
	for rows.Next() {
		var s RoleSummary
		var perms string
		if err := rows.Scan(&s.ID, &s.Name, &s.DisplayName, &s.Description, &s.IsSystem, &perms, &s.Users); err != nil {
			return nil, err
		}
		s.Permissions = []string{}
		if perms != "" {
			s.Permissions = strings.Split(perms, ",")
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetRolePermissions replaces a role's permissions ("resource:action").
// The admin role always has every permission and cannot be edited.
func SetRolePermissions(roleID int, perms []string) error {
	var name string
	if err := db.QueryRow(`SELECT name FROM roles WHERE id = $1`, roleID).Scan(&name); err != nil {
		return fmt.Errorf("role not found")
	}
	if name == "admin" {
		return fmt.Errorf("the admin role always has every permission")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var ids []int
	for _, p := range perms {
		res, act, ok := strings.Cut(strings.TrimSpace(p), ":")
		if !ok {
			return fmt.Errorf("permission %q: use resource:action", p)
		}
		var id int
		if err := tx.QueryRow(`SELECT id FROM permissions WHERE resource = $1 AND action = $2`, res, act).Scan(&id); err != nil {
			return fmt.Errorf("unknown permission %q", p)
		}
		ids = append(ids, id)
	}
	if _, err := tx.Exec(`DELETE FROM role_permissions WHERE role_id = $1`, roleID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT INTO role_permissions (role_id, permission_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, roleID, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	permCache.InvalidateAll() // every holder of the role
	return nil
}

// SetPrimaryRole makes the user hold the built-in role chosen on the Users
// page (users.role): the previous one is replaced; other roles stay.
func SetPrimaryRole(userID int, oldRole, newRole string) error {
	rbacName := func(r string) string {
		if r == "readonly" {
			return "viewer"
		}
		return r
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if oldRole != "" && rbacName(oldRole) != rbacName(newRole) {
		if _, err := tx.Exec(`DELETE FROM user_roles WHERE user_id = $1 AND role_id = (SELECT id FROM roles WHERE name = $2)`,
			userID, rbacName(oldRole)); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id, granted_by)
		SELECT $1, id, 'users-page' FROM roles WHERE name = $2
		ON CONFLICT (user_id, role_id) DO NOTHING`, userID, rbacName(newRole))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists bool
		_ = tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM roles WHERE name = $1)`, rbacName(newRole)).Scan(&exists)
		if !exists {
			return fmt.Errorf("role %q does not exist", newRole)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	permCache.Invalidate(userID)
	return nil
}

// BuiltinRolePermissions are the defaults of the built-in roles other than
// admin (which has every permission). Seeded only while a role has none, so
// an admin's changes stay.
var BuiltinRolePermissions = map[string]func(resource, action string) bool{
	// Everything except managing users, roles and the system itself.
	"operator": func(res, act string) bool {
		return res != "users" && res != "roles" && !(res == "system" && act == "admin")
	},
	// Their files and a read-only view of storage and services.
	"user": func(res, act string) bool {
		switch res {
		case "files":
			return act == "read" || act == "write"
		case "storage", "shares", "snapshots", "docker", "monitoring":
			return act == "read"
		}
		return false
	},
	// Read-only access to everything except the audit trail.
	"viewer": func(res, act string) bool { return act == "read" && res != "audit" },
}
