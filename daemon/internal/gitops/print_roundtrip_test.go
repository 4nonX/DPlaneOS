package gitops

import (
	"reflect"
	"testing"
)

// PrintStateYAML must write every section ParseStateYAML reads: a state
// exported (Change History, Git) and read back is the same state.
func TestPrintParseRoundTripIdentityAndDirectory(t *testing.T) {
	in := &DesiredState{
		Version: "1",
		Users: []DesiredUser{
			{Username: "alice", PasswordHash: "secret://user/alice/password", Email: "a@x.lan", Role: "admin", Active: true},
			{Username: "bob", Email: "", Role: "user", Active: false},
		},
		Groups: []DesiredGroup{{Name: "staff", Description: "All staff", GID: 3000, Members: []string{"alice", "bob"}}},
		Replication: []DesiredReplication{{Name: "offsite", SourceDataset: "tank/data", Remote: "backup", RemotePool: "vault",
			Interval: "daily", TriggerOnSnapshot: true, Incremental: true, Compress: true, RateLimitMB: 50, Enabled: true}},
		LDAP: &DesiredLDAP{Enabled: true, Server: "ldap.lan", Port: 636, UseTLS: true, BindDN: "cn=svc,dc=lan",
			BindPassword: "secret://ldap/ldap/bind_password", BaseDN: "dc=lan", UserFilter: "(uid=%s)", UserIDAttr: "uid",
			UserNameAttr: "cn", UserEmailAttr: "mail", GroupBaseDN: "ou=groups,dc=lan", GroupFilter: "(member=%s)",
			GroupMemberAttr: "member", JITProvisioning: true, DefaultRole: "user", SyncInterval: 3600, Timeout: 10},
		ACME: &DesiredACME{Enabled: true, Email: "ops@x.lan", Server: "https://acme.example", Resolver: "dns",
			Domains: []string{"nas.x.lan"}, DNSConfig: map[string]string{"CF_API_TOKEN": "secret://acme/acme/dns_config.CF_API_TOKEN"}},
	}
	out := PrintStateYAML(in)
	back, err := ParseStateYAML(out)
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
	for name, pair := range map[string][2]any{
		"users":       {in.Users, back.Users},
		"groups":      {in.Groups, back.Groups},
		"replication": {in.Replication, back.Replication},
		"ldap":        {in.LDAP, back.LDAP},
		"acme":        {in.ACME, back.ACME},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s changed:\n got  %+v\n want %+v\n%s", name, pair[1], pair[0], out)
		}
	}
}

func TestSecretReferencesValidate(t *testing.T) {
	ok := &DesiredState{Version: "1", Users: []DesiredUser{{Username: "alice", PasswordHash: "secret://user/alice/password"}},
		LDAP: &DesiredLDAP{Server: "x", BindPassword: "secret://ldap/ldap/bind_password"}}
	if errs := ValidState(ok); len(errs) != 0 {
		t.Errorf("references refused: %v", errs)
	}
	bad := &DesiredState{Version: "1", Users: []DesiredUser{{Username: "alice", PasswordHash: "hunter2"}},
		LDAP: &DesiredLDAP{Server: "x", BindPassword: "s3cret"}}
	if errs := ValidState(bad); len(errs) != 2 {
		t.Errorf("plain-text secrets accepted: %v", errs)
	}
}

// Block scalars keep blank lines and comment lines: they are part of a
// compose file, not YAML comments of state.yaml.
func TestBlockScalarKeepsBlankLinesAndComments(t *testing.T) {
	compose := "services:\n  web:\n    image: nginx\n\n  # the database\n  db:\n    image: postgres\n    environment:\n      - A=1"
	in := &DesiredState{Version: "1", Stacks: []DesiredStack{{Name: "app", YAML: compose}}}
	back, err := ParseStateYAML(PrintStateYAML(in))
	if err != nil {
		t.Fatal(err)
	}
	if back.Stacks[0].YAML != compose {
		t.Fatalf("compose changed:\n%q\nwant\n%q", back.Stacks[0].YAML, compose)
	}
	// Folded scalars join lines.
	folded, err := ParseStateYAML("version: \"1\"\nsystem:\n  samba:\n    workgroup: W\n    server_string: >\n      one\n      two\n\n      three\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := folded.System.Samba.ServerString; got != "one two\nthree" {
		t.Errorf("folded: %q", got)
	}
}

// Users missing from a complete desired state are deleted, except the
// built-in accounts and accounts that come from a directory.
func TestUndeclaredUsersAreDeletedExceptBuiltInAndDirectory(t *testing.T) {
	live := &LiveState{Users: []LiveUser{
		{ID: 1, Username: "admin", Source: "local"},
		{ID: 2, Username: "bob", Source: "local"},
		{ID: 3, Username: "carol", Source: "git"},
		{ID: 4, Username: "dave", Source: "ldap"},
		{ID: 5, Username: "erin", Source: "oidc"},
	}}
	plan := ComputeDiff(&DesiredState{Version: "1"}, live)
	var deleted []string
	for _, it := range plan.Items {
		if it.Kind == KindUser && it.Action == ActionDelete {
			deleted = append(deleted, it.Name)
		}
	}
	if len(deleted) != 2 || deleted[0] != "bob" || deleted[1] != "carol" {
		t.Fatalf("deleted %v, want [bob carol]", deleted)
	}

	// A partial state (ignore_extraneous) deletes nothing it does not name.
	live.Groups = []LiveGroup{{ID: 1, Name: "staff"}}
	live.Replication = []LiveReplication{{Name: "offsite"}}
	plan = ComputeDiff(&DesiredState{Version: "1", IgnoreExtraneous: true}, live)
	for _, it := range plan.Items {
		if it.Action == ActionDelete {
			t.Errorf("partial state deletes %s %s", it.Kind, it.Name)
		}
	}
}
