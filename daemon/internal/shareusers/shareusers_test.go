package shareusers

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"dplaned/internal/database"
	"dplaned/internal/secrets"
)

// The NT hash of "password" (documented test vector).
func TestNTHash(t *testing.T) {
	if got := NTHash("password"); got != "8846F7EAEE8FB117AD06BDD830B7586C" {
		t.Errorf("NTHash = %s", got)
	}
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set")
	}
	b := make([]byte, 6)
	rand.Read(b) //nolint:errcheck
	schema := "t_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); admin.Close() }) //nolint:errcheck
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// fakeHost keeps passwd, group and the Samba database in memory.
type fakeHost struct {
	users map[string]struct {
		uid   int
		gecos string
	}
	groups map[string]int
	member map[string]string
	samba  map[string]struct{ nt, flags string }
	ran    []string
}

func newFakeHost() *fakeHost {
	h := &fakeHost{
		users: map[string]struct {
			uid   int
			gecos string
		}{"root": {0, "root"}, "nginx": {60, "nginx web server"}},
		groups: map[string]int{"users": 100, "wheel": 1},
		member: map[string]string{},
		samba:  map[string]struct{ nt, flags string }{},
	}
	return h
}

func (h *fakeHost) system() System {
	return System{
		Run: func(stdin, name string, args ...string) ([]byte, error) {
			h.ran = append(h.ran, name+" "+strings.Join(args, " "))
			last := args[len(args)-1]
			switch name {
			case "useradd":
				uid := 0
				for i, a := range args {
					if a == "-u" {
						uid = atoi(args[i+1])
					}
				}
				for _, u := range h.users {
					if u.uid == uid {
						return []byte("UID is not unique"), errFail
					}
				}
				h.users[last] = struct {
					uid   int
					gecos string
				}{uid, Marker}
			case "userdel":
				delete(h.users, last)
			case "usermod":
				u := h.users[last]
				u.uid = atoi(args[1])
				h.users[last] = u
			case "groupadd":
				h.groups[last] = atoi(args[1])
			case "groupdel":
				delete(h.groups, last)
			case "gpasswd":
				h.member[last] = args[1]
			case "pdbedit":
				switch {
				case args[0] == "-L":
					var b strings.Builder
					for n, s := range h.samba {
						b.WriteString(n + ":3000:XXX:" + s.nt + ":" + s.flags + ":LCT-0:\n")
					}
					return []byte(b.String()), nil
				case args[0] == "-a":
					h.samba[last] = struct{ nt, flags string }{"RANDOM", "[U          ]"}
				case args[0] == "-x":
					delete(h.samba, last)
				case slices.Contains(args, "--set-nt-hash"):
					s := h.samba[args[1]]
					s.nt = last
					h.samba[args[1]] = s
				}
			case "smbpasswd":
				s := h.samba[last]
				if args[0] == "-d" {
					s.flags = "[DU         ]"
				} else {
					s.flags = "[U          ]"
				}
				h.samba[last] = s
			}
			return nil, nil
		},
		LookupUser: func(n string) (int, string, bool) {
			u, ok := h.users[n]
			return u.uid, u.gecos, ok
		},
		LookupGroup: func(n string) (int, bool) { g, ok := h.groups[n]; return g, ok },
		UIDTaken: func(uid int) bool {
			for _, u := range h.users {
				if u.uid == uid {
					return true
				}
			}
			return false
		},
		GIDTaken: func(gid int) bool {
			for _, g := range h.groups {
				if g == gid {
					return true
				}
			}
			return false
		},
		HaveSamba: func() bool { return true },
	}
}

var errFail = os.ErrInvalid

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestSync(t *testing.T) {
	db := testDB(t)
	host := newFakeHost()
	SetSystem(host.system())
	StatePath = filepath.Join(t.TempDir(), "groups.json")
	markedAccounts = func() []string {
		var out []string
		for n, u := range host.users {
			if u.gecos == Marker {
				out = append(out, n)
			}
		}
		slices.Sort(out)
		return out
	}

	if err := secrets.Init(filepath.Join(t.TempDir(), "secrets.key")); err != nil {
		t.Fatal(err)
	}
	alice, err1 := SealedNTHash("alice-pw")
	carol, err2 := SealedNTHash("carol-pw")
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	for _, q := range []string{
		`INSERT INTO users (username, smb_nt_hash, active) VALUES ('alice', '` + alice + `', 1)`,
		`INSERT INTO users (username, active) VALUES ('bob', 1)`,
		`INSERT INTO users (username, smb_nt_hash, active) VALUES ('carol', '` + carol + `', 0)`,
		`INSERT INTO users (username, active) VALUES ('nginx', 1)`,
		`INSERT INTO users (username, active) VALUES ('Mixed.Case', 1)`,
		`INSERT INTO users (username, source, active) VALUES ('ldapuser', 'ldap', 1)`,
		`INSERT INTO groups (name) VALUES ('media'), ('wheel')`,
		`INSERT INTO group_members (group_name, username) VALUES ('media', 'alice'), ('media', 'bob'), ('wheel', 'alice')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	if err := Sync(db); err != nil {
		t.Fatal(err)
	}
	st := LastStatus()
	if host.users["alice"].uid != 3000 || host.users["bob"].uid != 3001 || host.users["carol"].uid != 3002 {
		t.Errorf("accounts: %v", host.users)
	}
	if host.users["nginx"].gecos != "nginx web server" || st["nginx"].SMB || !strings.Contains(st["nginx"].Note, "system account") {
		t.Errorf("a system account was touched: %v %+v", host.users["nginx"], st["nginx"])
	}
	if _, ok := host.users["ldapuser"]; ok || !strings.Contains(st["Mixed.Case"].Note, "cannot be a system account name") {
		t.Errorf("ldap/invalid users: %v %+v", host.users, st)
	}
	if !st["alice"].SMB || host.samba["alice"].nt != NTHash("alice-pw") {
		t.Errorf("alice: %+v %v", st["alice"], host.samba["alice"])
	}
	if st["bob"].SMB || !strings.Contains(st["bob"].Note, "set the password again") {
		t.Errorf("bob without a hash: %+v", st["bob"])
	}
	if !strings.Contains(host.samba["carol"].flags, "D") || st["carol"].SMB {
		t.Errorf("carol is disabled: %v %+v", host.samba["carol"], st["carol"])
	}
	if host.groups["media"] != 3000 || host.member["media"] != "alice,bob" {
		t.Errorf("group media: %v %v", host.groups, host.member)
	}
	if _, touched := host.member["wheel"]; touched || host.groups["wheel"] != 1 {
		t.Error("the system group wheel was changed")
	}
	var aliceUID int
	db.QueryRow(`SELECT uid FROM users WHERE username = 'alice'`).Scan(&aliceUID)
	if aliceUID != 3000 {
		t.Errorf("UID not saved: %d", aliceUID)
	}

	// Nothing changed: nothing is created again.
	host.ran = nil
	if err := Sync(db); err != nil {
		t.Fatal(err)
	}
	for _, c := range host.ran {
		if strings.HasPrefix(c, "useradd") || strings.HasPrefix(c, "groupadd") || strings.Contains(c, "--set-nt-hash") || strings.HasPrefix(c, "pdbedit -a") {
			t.Errorf("second sync ran %q", c)
		}
	}

	// alice renamed (keeps her UID), bob deleted, group media deleted.
	db.Exec(`UPDATE users SET username = 'alice2' WHERE username = 'alice'`) //nolint:errcheck
	db.Exec(`DELETE FROM users WHERE username = 'bob'`)                      //nolint:errcheck
	db.Exec(`DELETE FROM groups WHERE name = 'media'`)                       //nolint:errcheck
	if err := Sync(db); err != nil {
		t.Fatal(err)
	}
	if _, ok := host.users["alice"]; ok || host.users["alice2"].uid != 3000 {
		t.Errorf("rename: %v", host.users)
	}
	if _, ok := host.users["bob"]; ok {
		t.Error("bob's account kept")
	}
	if _, ok := host.samba["alice"]; ok || host.samba["alice2"].nt != NTHash("alice-pw") {
		t.Errorf("samba after rename: %v", host.samba)
	}
	if _, ok := host.groups["media"]; ok {
		t.Error("group media kept")
	}

	// A password set where only the bcrypt hash travels: the SMB hash goes.
	db.Exec(`UPDATE users SET smb_nt_hash = '' WHERE username = 'alice2'`) //nolint:errcheck
	if err := Sync(db); err != nil {
		t.Fatal(err)
	}
	if _, ok := host.samba["alice2"]; ok || LastStatus()["alice2"].SMB {
		t.Error("stale SMB password kept")
	}
}
