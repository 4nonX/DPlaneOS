package handlers

import (
	"testing"

	"dplaned/internal/security"
)

func TestShareACLSpec(t *testing.T) {
	ps, err := sharePrincipals("alice, @media +staff")
	if err != nil {
		t.Fatal(err)
	}
	got := shareACLSpec(ps, false)
	want := "u:alice:rwx,d:u:alice:rwx,g:media:rwx,d:g:media:rwx,g:staff:rwx,d:g:staff:rwx,m::rwx,d:m::rwx"
	if got != want {
		t.Errorf("spec\n got %s\nwant %s", got, want)
	}
	// No valid users: every file-sharing account (group users), read-only.
	ps, _ = sharePrincipals("")
	if got := shareACLSpec(ps, true); got != "g:users:r-x,d:g:users:r-x,m::rwx,d:m::rwx" {
		t.Errorf("default: %s", got)
	}
	for _, bad := range []string{"Alice", "a;b", "@x:rwx", "../x"} {
		if _, err := sharePrincipals(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The command whitelist accepts what grantShareAccess runs (default entries
// were refused before).
func TestShareACLSpecWhitelisted(t *testing.T) {
	ps, _ := sharePrincipals("alice @media")
	if err := security.ValidateCommand("setfacl", []string{"-m", shareACLSpec(ps, false), "/tank/share"}); err != nil {
		t.Errorf("whitelist: %v", err)
	}
}
