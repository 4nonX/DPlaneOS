package gitops

import (
	"strings"
	"testing"

	"dplaned/internal/testdb"
)

// An apply of the file DPlaneOS itself writes to the repository leaves the
// directory settings as they are: the bind password stays, and so does
// every setting. A file that names only some settings changes only those.
func TestApplyKeepsLDAPSecretAndSettings(t *testing.T) {
	db := testdb.Open(t)
	if _, err := db.Exec(`INSERT INTO ldap_config (id) VALUES (1) ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ldap_config SET enabled=1, server='ldap.example', port=636, use_tls=1,
		bind_dn='cn=svc,dc=example', bind_password='sealed:abc', base_dn='dc=example',
		user_filter='(uid=%s)', user_id_attr='uid', user_name_attr='cn', user_email_attr='mail',
		group_base_dn='ou=groups,dc=example', group_filter='(objectClass=groupOfNames)', group_member_attr='member',
		jit_provisioning=1, default_role='user', sync_interval=3600, timeout=10 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	row := func() string {
		var s string
		if err := db.QueryRow(`SELECT concat_ws('|', enabled, server, port, use_tls, bind_dn, bind_password, base_dn,
			user_filter, user_id_attr, user_name_attr, user_email_attr, group_base_dn, group_filter, group_member_attr,
			jit_provisioning, default_role, sync_interval, timeout) FROM ldap_config WHERE id=1`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := row()

	// The snapshot of the live system, parsed back and applied.
	live, err := readLDAPConfig(db)
	if err != nil {
		t.Fatal(err)
	}
	yaml := GenerateStateYAML(&LiveState{LDAP: live})
	if strings.Contains(yaml, "sealed:abc") || strings.Contains(yaml, "bind_password") {
		t.Fatalf("the snapshot carries the bind password:\n%s", yaml)
	}
	desired, err := ParseStateYAML(yaml)
	if err != nil {
		t.Fatalf("the snapshot does not parse: %v\n%s", err, yaml)
	}
	if desired.LDAP == nil {
		t.Fatalf("the snapshot has no ldap section:\n%s", yaml)
	}
	if err := reconcileLDAP(db, desired.LDAP); err != nil {
		t.Fatal(err)
	}
	if after := row(); after != before {
		t.Errorf("applying the system's own snapshot changed the directory settings:\n before %s\n after  %s", before, after)
	}

	// A file that names two settings changes those two.
	partial, err := ParseStateYAML("version: \"6\"\nldap:\n  enabled: true\n  use_tls: true\n  jit_provisioning: true\n  server: ldap2.example\n  timeout: 20\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileLDAP(db, partial.LDAP); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(strings.Replace(before, "ldap.example", "ldap2.example", 1), "|3600|10", "|3600|20", 1)
	if after := row(); after != want {
		t.Errorf("partial file:\n want %s\n got  %s", want, after)
	}
}
