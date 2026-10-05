package gitops

import (
	"strings"
	"testing"
)

func TestValidStateRejectsPlainTextSecrets(t *testing.T) {
	const hash = "$2b$12$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"
	cases := []struct {
		name  string
		state DesiredState
		want  string // substring of an error, "" = valid
	}{
		{"bcrypt hash accepted", DesiredState{Version: "1", Users: []DesiredUser{{Username: "alice", PasswordHash: hash}}}, ""},
		{"empty hash accepted", DesiredState{Version: "1", Users: []DesiredUser{{Username: "alice"}}}, ""},
		{"plain password rejected", DesiredState{Version: "1", Users: []DesiredUser{{Username: "alice", PasswordHash: "hunter2"}}}, "password_hash must be a bcrypt hash"},
		{"truncated hash rejected", DesiredState{Version: "1", Users: []DesiredUser{{Username: "alice", PasswordHash: "$2b$12$short"}}}, "password_hash must be a bcrypt hash"},
		{"ldap bind password rejected", DesiredState{Version: "1", LDAP: &DesiredLDAP{BindPassword: "secret"}}, "ldap.bind_password"},
		{"ldap without password accepted", DesiredState{Version: "1", LDAP: &DesiredLDAP{}}, ""},
	}
	for _, c := range cases {
		errs := strings.Join(ValidState(&c.state), "\n")
		if c.want == "" && strings.Contains(errs, "password") {
			t.Errorf("%s: unexpected error: %s", c.name, errs)
		}
		if c.want != "" && !strings.Contains(errs, c.want) {
			t.Errorf("%s: want error containing %q, got %q", c.name, c.want, errs)
		}
	}
}
