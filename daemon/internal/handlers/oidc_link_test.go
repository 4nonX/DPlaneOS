package handlers

import (
	"testing"

	"dplaned/internal/oidc"
)

func TestOIDCLinkingAndRoles(t *testing.T) {
	db := testDB(t)
	db.Exec(`DELETE FROM users WHERE username IN ('boss', 'idp_boss')`)      //nolint:errcheck
	db.Exec(`DELETE FROM oidc_identities WHERE issuer = 'https://idp.test'`) //nolint:errcheck
	var bossID int64
	if err := db.QueryRow(`INSERT INTO users (username, password_hash, email, role, active) VALUES ('boss', 'x', 'boss@corp.test', 'admin', 1) RETURNING id`).Scan(&bossID); err != nil {
		t.Fatal(err)
	}
	h := &OIDCHandler{db: db}
	cfg := &oidcConfigRow{Issuer: "https://idp.test", AutoProvision: true}

	// An unverified email does not take over the local account.
	id, name, err := h.resolveUser(cfg, &oidc.Claims{Issuer: "https://idp.test", Subject: "attacker", Email: "boss@corp.test", EmailVerified: false, PreferredUsername: "boss"})
	if err != nil {
		t.Fatal(err)
	}
	if id == bossID {
		t.Fatalf("an unverified email was linked to the local account %q", name)
	}
	// A verified email links.
	id, _, err = h.resolveUser(cfg, &oidc.Claims{Issuer: "https://idp.test", Subject: "real-boss", Email: "boss@corp.test", EmailVerified: true})
	if err != nil || id != bossID {
		t.Fatalf("verified email: %d %v", id, err)
	}

	roleCount := func(role string) int {
		var n int
		db.QueryRow(`SELECT COUNT(*) FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = $1 AND r.name = $2`, bossID, role).Scan(&n) //nolint:errcheck
		return n
	}
	// A locally granted role stays; IdP roles follow the groups.
	db.Exec(`INSERT INTO user_roles (user_id, role_id, granted_by) SELECT $1, id, 'admin' FROM roles WHERE name = 'viewer' ON CONFLICT DO NOTHING`, bossID) //nolint:errcheck
	if err := h.assignRoles(bossID, []string{"operator", "not-a-role"}); err != nil {
		t.Fatal(err)
	}
	if roleCount("operator") != 1 {
		t.Fatal("operator not granted from the group")
	}
	if err := h.assignRoles(bossID, []string{}); err != nil {
		t.Fatal(err)
	}
	if roleCount("operator") != 0 {
		t.Error("operator kept after leaving the IdP group")
	}
	if roleCount("viewer") != 1 {
		t.Error("a locally granted role was revoked")
	}
}
