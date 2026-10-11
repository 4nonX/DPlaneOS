package fileaccounts

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"dplaned/internal/secrets"
	"dplaned/internal/testdb"
)

// fakeSystem stands in for /etc/passwd, /etc/group and Samba's database.
type fakeSystem struct {
	passwd map[string][2]string // name -> uid, gecos
	login  map[string][2]string // name -> home, shell
	homes  map[string]bool
	shadow map[string]string
	groups map[string][2]string // name -> gid, members
	samba  map[string]string    // name -> nt hash
	log    []string
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		passwd: map[string][2]string{"root": {"0", "System administrator"}, "postgres": {"71", "PostgreSQL"}},
		shadow: map[string]string{},
		login:  map[string][2]string{},
		homes:  map[string]bool{},
		groups: map[string][2]string{"root": {"0", ""}, "users": {"100", ""}, "wheel": {"1", "operator"}},
		samba:  map[string]string{},
	}
}

func (f *fakeSystem) run(stdin, name string, args ...string) (string, error) {
	f.log = append(f.log, name+" "+strings.Join(args, " "))
	arg := func(flag string) string {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	last := args[len(args)-1]
	switch name {
	case "getent":
		var b strings.Builder
		if args[0] == "passwd" {
			for n, v := range f.passwd {
				l := f.login[n]
				fmt.Fprintf(&b, "%s:x:%s:100:%s:%s:%s\n", n, v[0], v[1], l[0], l[1])
			}
		} else if len(args) == 2 {
			v, ok := f.groups[args[1]]
			if !ok {
				return "", fmt.Errorf("exit status 2")
			}
			return fmt.Sprintf("%s:x:%s:%s\n", args[1], v[0], v[1]), nil
		} else {
			for n, v := range f.groups {
				fmt.Fprintf(&b, "%s:x:%s:%s\n", n, v[0], v[1])
			}
		}
		return b.String(), nil
	case "useradd":
		f.passwd[last] = [2]string{arg("-u"), arg("-c")}
		f.login[last] = [2]string{arg("-d"), arg("-s")}
	case "usermod":
		if p := arg("-p"); p != "" {
			f.shadow[last] = p
		}
		if s := arg("-s"); s != "" {
			f.login[last] = [2]string{arg("-d"), s}
		}
	case "install":
		f.homes[last] = true
	case "userdel":
		delete(f.passwd, last)
		delete(f.shadow, last)
	case "groupadd":
		f.groups[last] = [2]string{arg("-g"), ""}
	case "gpasswd":
		g := f.groups[last]
		members := map[string]bool{}
		for _, m := range strings.Split(g[1], ",") {
			if m != "" {
				members[m] = true
			}
		}
		switch args[0] {
		case "-M":
			f.groups[last] = [2]string{g[0], arg("-M")}
			return "", nil
		case "-a":
			members[args[1]] = true
		case "-d":
			delete(members, args[1])
		}
		var list []string
		for m := range members {
			list = append(list, m)
		}
		sort.Strings(list)
		f.groups[last] = [2]string{g[0], strings.Join(list, ",")}
	case "groupdel":
		delete(f.groups, last)
	case "pdbedit":
		switch {
		case args[0] == "-L":
			var b strings.Builder
			for n := range f.samba {
				fmt.Fprintf(&b, "%s:4294967295:\n", n)
			}
			return b.String(), nil
		case args[0] == "-a":
			if strings.Count(stdin, "\n") != 2 {
				return "no password on stdin", fmt.Errorf("exit status 1")
			}
			f.samba[last] = "throwaway"
		case args[0] == "-x":
			delete(f.samba, last)
		default:
			if args[0] != "-r" {
				return "unknown mode", fmt.Errorf("exit status 1")
			}
			f.samba[arg("-u")] = strings.TrimPrefix(last, "--set-nt-hash=")
		}
	}
	return "", nil
}

func setup(t *testing.T) (*sql.DB, *fakeSystem) {
	t.Helper()
	db := testdb.Open(t)
	if err := secrets.Init(filepath.Join(t.TempDir(), "secrets.key")); err != nil {
		t.Fatal(err)
	}
	f := newFakeSystem()
	prev := run
	run = f.run
	t.Cleanup(func() { run = prev })
	return db, f
}

func addUser(t *testing.T, db *sql.DB, name, source string, active int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, role, active, source) VALUES ($1, 'x', 'user', $2, $3)`, name, active, source); err != nil {
		t.Fatal(err)
	}
}

func names[V any](m map[string]V) string {
	var out []string
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestSyncCreatesAndRemovesAccounts(t *testing.T) {
	db, sys := setup(t)
	addUser(t, db, "alice", "local", 1)
	addUser(t, db, "bob", "local", 1)
	addUser(t, db, "carol", "local", 1)    // never set a password here: no accounts yet
	addUser(t, db, "dirk", "ldap", 1)      // directory user: SMB goes through the domain
	addUser(t, db, "postgres", "local", 1) // collides with a system account
	addUser(t, db, "Bad.Name", "local", 1)
	for _, u := range []string{"alice", "bob", "dirk", "postgres", "Bad.Name"} {
		if err := SetPassword(db, u, "Secret-"+u); err != nil {
			t.Fatal(err)
		}
	}
	if !HasPassword(db, "alice") || HasPassword(db, "carol") {
		t.Fatal("HasPassword")
	}
	if _, err := db.Exec(`INSERT INTO groups (name) VALUES ('family')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_members (group_name, username) VALUES ('family', 'alice'), ('family', 'carol')`); err != nil {
		t.Fatal(err)
	}

	err := Sync(db)
	if err == nil || !strings.Contains(err.Error(), "postgres: a system account of that name exists") {
		t.Fatalf("the collision must be reported: %v", err)
	}
	if got := names(sys.samba); got != "alice,bob" {
		t.Errorf("samba accounts: %s", got)
	}
	if sys.passwd["alice"] != [2]string{"1100", gecos} || sys.passwd["bob"] != [2]string{"1101", gecos} {
		t.Errorf("unix accounts: %v", sys.passwd)
	}
	if sys.passwd["postgres"][0] != "71" {
		t.Errorf("a system account was touched: %v", sys.passwd["postgres"])
	}
	if sys.samba["alice"] != NTHash("Secret-alice") || !strings.HasPrefix(sys.shadow["alice"], "$6$") {
		t.Errorf("alice: samba %q shadow %q", sys.samba["alice"], sys.shadow["alice"])
	}
	// Only members that have an account are put into the group.
	if g := sys.groups["family"]; g != [2]string{"1100", "alice"} {
		t.Errorf("group family: %v", g)
	}
	// The NT hash is not stored in the clear.
	var stored string
	_ = db.QueryRow(`SELECT nt_hash FROM users WHERE username = 'alice'`).Scan(&stored)
	if stored == "" || strings.Contains(stored, NTHash("Secret-alice")) {
		t.Errorf("stored NT hash: %q", stored)
	}

	// A second run changes nothing and creates nothing.
	sys.log = nil
	_ = Sync(db)
	for _, l := range sys.log {
		if strings.HasPrefix(l, "useradd") || strings.HasPrefix(l, "pdbedit -a") || strings.HasPrefix(l, "groupadd") || strings.HasPrefix(l, "gpasswd") {
			t.Errorf("second run: %s", l)
		}
	}

	// New password, a user disabled, a user and a group deleted.
	if err := SetPassword(db, "alice", "Other-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET active = 0 WHERE username = 'bob'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM groups WHERE name = 'family'`); err != nil {
		t.Fatal(err)
	}
	_ = Sync(db)
	if sys.samba["alice"] != NTHash("Other-1") {
		t.Errorf("password change did not reach Samba")
	}
	if _, ok := sys.passwd["bob"]; ok || names(sys.samba) != "alice" {
		t.Errorf("disabled user keeps accounts: %v %v", sys.passwd, sys.samba)
	}
	if _, ok := sys.groups["family"]; ok {
		t.Errorf("deleted group still exists")
	}
	if _, ok := sys.groups["users"]; !ok {
		t.Errorf("a system group was removed")
	}
	// The number of a user never changes; a freed one is reused by a new user.
	var uid int
	_ = db.QueryRow(`SELECT uid FROM users WHERE username = 'alice'`).Scan(&uid)
	if uid != 1100 {
		t.Errorf("alice's ID changed: %d", uid)
	}
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }

func TestAdministratorsGetAShell(t *testing.T) {
	db, sys := setup(t)
	addUser(t, db, "alice", "local", 1)
	addUser(t, db, "root2", "local", 1)
	if _, err := db.Exec(`UPDATE users SET role = 'admin' WHERE username = 'root2'`); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"alice", "root2"} {
		if err := SetPassword(db, u, "Secret-"+u); err != nil {
			t.Fatal(err)
		}
	}
	if err := Sync(db); err != nil {
		t.Fatal(err)
	}
	if sys.login["alice"] != [2]string{noHome, noLogin} {
		t.Errorf("alice: %v", sys.login["alice"])
	}
	if sys.login["root2"] != [2]string{HomeBase + "/root2", adminShell} || !sys.homes[HomeBase+"/root2"] {
		t.Errorf("root2: %v, homes %v", sys.login["root2"], sys.homes)
	}
	// Other members of the group are left alone.
	if got := sys.groups["wheel"][1]; got != "operator,root2" {
		t.Errorf("wheel: %q", got)
	}
	if !CanSignIn("root2") || CanSignIn("alice") || CanSignIn("root") {
		t.Error("CanSignIn")
	}

	// No longer an administrator: no shell, no sudo. A new administrator gets both.
	if _, err := db.Exec(`UPDATE users SET role = CASE username WHEN 'root2' THEN 'user' ELSE 'admin' END`); err != nil {
		t.Fatal(err)
	}
	if err := Sync(db); err != nil {
		t.Fatal(err)
	}
	if sys.login["root2"] != [2]string{noHome, noLogin} || sys.login["alice"][1] != adminShell {
		t.Errorf("after the role change: %v", sys.login)
	}
	if got := sys.groups["wheel"][1]; got != "alice,operator" {
		t.Errorf("wheel after the role change: %q", got)
	}
	if CanSignIn("root2") {
		t.Error("a former administrator can still sign in")
	}
}
