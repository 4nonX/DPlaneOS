// Package shareusers makes DPlaneOS's local users and groups usable for file
// sharing.
//
// SMB, NFS and file ownership work with system accounts; DPlaneOS keeps its
// users in its database. Each local user is therefore also a system account
// (no shell, no home, UID from FirstID up, primary group "users") and each
// DPlaneOS group a system group with the same members. Samba (passdb tdbsam)
// authenticates with the NT hash of the password, which is computed when the
// password is set and stored sealed in the database.
//
// The database is the source of truth: Sync creates what is missing and
// removes what it created for users that are gone. It runs at daemon start
// (an OTA update replaces /etc/passwd and /etc/group; /var/lib/samba is
// kept) and after every user or group change. Accounts that exist already
// and were not created here (root, service users) are never changed.
package shareusers

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	"golang.org/x/crypto/md4" //nolint:staticcheck // the NT hash is MD4 by definition

	"dplaned/internal/secrets"
)

const (
	// FirstID is the first UID/GID given to DPlaneOS users and groups.
	FirstID = 3000
	// Marker is the comment (GECOS) of the accounts created here.
	Marker = "DPlaneOS user"
	// PrimaryGroup of the accounts.
	PrimaryGroup = "users"
)

// StatePath records the system groups created here (groups carry no marker).
var StatePath = "/var/lib/dplaneos/share-groups.json"

// NTHash is the NT hash of a password: MD4 of its UTF-16LE encoding, in hex.
func NTHash(password string) string {
	u := utf16.Encode([]rune(password))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	h := md4.New()
	h.Write(b)
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
}

// SealedNTHash is the value stored in users.smb_nt_hash for a password.
func SealedNTHash(password string) (string, error) {
	return secrets.Seal(NTHash(password))
}

// Status is how a user can use file sharing.
type Status struct {
	UID int `json:"uid,omitempty"`
	// SMB: the user can log in over SMB.
	SMB bool `json:"smb"`
	// Note says why not, or what is missing.
	Note string `json:"note,omitempty"`
}

// System runs the account tools; replaced in tests.
type System struct {
	Run func(stdin string, name string, args ...string) ([]byte, error)
	// LookupUser / LookupGroup return the passwd / group entry or ok=false.
	LookupUser  func(name string) (uid int, gecos string, ok bool)
	LookupGroup func(name string) (gid int, ok bool)
	UIDTaken    func(uid int) bool
	GIDTaken    func(gid int) bool
	HaveSamba   func() bool
}

// HostSystem uses the real tools (getent, useradd, pdbedit, ...).
func HostSystem() System {
	run := func(stdin, name string, args ...string) ([]byte, error) {
		cmd := exec.Command(name, args...)
		cmd.Stdin = strings.NewReader(stdin)
		return cmd.CombinedOutput()
	}
	getent := func(db, key string) []string {
		out, err := exec.Command("getent", db, key).Output()
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(out)), ":")
	}
	return System{
		Run: run,
		LookupUser: func(name string) (int, string, bool) {
			f := getent("passwd", name)
			if len(f) < 5 || f[0] != name {
				return 0, "", false
			}
			uid, _ := strconv.Atoi(f[2])
			return uid, f[4], true
		},
		LookupGroup: func(name string) (int, bool) {
			f := getent("group", name)
			if len(f) < 3 || f[0] != name {
				return 0, false
			}
			gid, _ := strconv.Atoi(f[2])
			return gid, true
		},
		UIDTaken:  func(uid int) bool { return getent("passwd", strconv.Itoa(uid)) != nil },
		GIDTaken:  func(gid int) bool { return getent("group", strconv.Itoa(gid)) != nil },
		HaveSamba: func() bool { _, err := exec.LookPath("pdbedit"); return err == nil },
	}
}

var (
	mu   sync.Mutex
	last = map[string]Status{}
	// system is nil until the daemon enables it (EnableHost): tests that
	// reach Sync through handlers must not create accounts on the machine
	// running them.
	system *System
)

// EnableHost makes Sync change the real system accounts (daemon start).
func EnableHost() { s := HostSystem(); system = &s }

// SetSystem replaces the system access (tests).
func SetSystem(s System) { system = &s }

// LastStatus returns the result of the last Sync per username.
func LastStatus() map[string]Status {
	mu.Lock()
	defer mu.Unlock()
	out := make(map[string]Status, len(last))
	for k, v := range last {
		out[k] = v
	}
	return out
}

type user struct {
	id       int64
	name     string
	active   bool
	uid      sql.NullInt64
	ntSealed string
}

// Sync makes the system accounts, groups and Samba users match the database.
// The error is the first failure; the rest is in the per-user status.
func Sync(db *sql.DB) error {
	mu.Lock()
	defer mu.Unlock()
	if system == nil {
		return nil
	}
	sys := *system
	var errs []error
	note := func(err error) {
		if err != nil {
			log.Printf("[shareusers] %v", err)
			errs = append(errs, err)
		}
	}

	rows, err := db.Query(`SELECT id, username, active, uid, smb_nt_hash FROM users WHERE source IN ('local', 'git') ORDER BY id`)
	if err != nil {
		return fmt.Errorf("load users: %w", err)
	}
	var users []user
	for rows.Next() {
		var u user
		var active int
		if err := rows.Scan(&u.id, &u.name, &active, &u.uid, &u.ntSealed); err != nil {
			rows.Close()
			return err
		}
		u.active = active == 1
		users = append(users, u)
	}
	rows.Close()

	status := map[string]Status{}
	eligible := map[string]bool{}
	nologin := nologinShell()

	// UIDs: kept once given, so file ownership stays with the user.
	usedUID := map[int64]bool{}
	for _, u := range users {
		if u.uid.Valid {
			usedUID[u.uid.Int64] = true
		}
	}
	next := int64(FirstID)
	for i := range users {
		u := &users[i]
		if u.uid.Valid {
			continue
		}
		for usedUID[next] || sys.UIDTaken(int(next)) {
			next++
		}
		if _, err := db.Exec(`UPDATE users SET uid = $1 WHERE id = $2`, next, u.id); err != nil {
			note(fmt.Errorf("give %s a UID: %w", u.name, err))
			continue
		}
		u.uid = sql.NullInt64{Int64: next, Valid: true}
		usedUID[next] = true
	}

	// Which users get an account: a valid name that no other system
	// account has.
	for _, u := range users {
		st := Status{}
		if u.uid.Valid {
			st.UID = int(u.uid.Int64)
		}
		status[u.name] = st
		if !validName(u.name) {
			st.Note = "the name cannot be a system account name (use lower-case letters, digits, - and _, at most 32 characters)"
			status[u.name] = st
			continue
		}
		if _, gecos, exists := sys.LookupUser(u.name); exists && gecos != Marker {
			st.Note = "a system account has this name; it is not used for file sharing"
			status[u.name] = st
			continue
		}
		eligible[u.name] = true
	}

	// Remove the accounts created here whose user is gone (or was renamed)
	// first, so their UIDs are free.
	var removed []string
	for _, name := range markedAccounts() {
		if !eligible[name] {
			removed = append(removed, name)
			if out, err := sys.Run("", "userdel", name); err != nil {
				note(fmt.Errorf("userdel %s: %v (%s)", name, err, strings.TrimSpace(string(out))))
			}
		}
	}

	for _, u := range users {
		if !eligible[u.name] {
			continue
		}
		st := status[u.name]
		uid, _, exists := sys.LookupUser(u.name)
		switch {
		case !exists:
			if out, err := sys.Run("", "useradd", "-M", "-N", "-g", PrimaryGroup, "-s", nologin, "-d", "/var/empty",
				"-u", strconv.Itoa(st.UID), "-c", Marker, u.name); err != nil {
				note(fmt.Errorf("useradd %s: %v (%s)", u.name, err, strings.TrimSpace(string(out))))
				st.Note = "the system account could not be created"
				status[u.name] = st
				delete(eligible, u.name)
				continue
			}
		case uid != st.UID:
			if out, err := sys.Run("", "usermod", "-u", strconv.Itoa(st.UID), u.name); err != nil {
				note(fmt.Errorf("usermod %s: %v (%s)", u.name, err, strings.TrimSpace(string(out))))
			}
		}
	}

	note(syncGroups(db, sys, eligible))

	if sys.HaveSamba() {
		note(syncSamba(sys, users, eligible, removed, status))
	} else {
		for n, st := range status {
			if st.Note == "" {
				st.Note = "Samba is not installed"
				status[n] = st
			}
		}
	}
	last = status
	return errors.Join(errs...)
}

func syncSamba(sys System, users []user, eligible map[string]bool, removed []string, status map[string]Status) error {
	out, err := sys.Run("", "pdbedit", "-L", "-w")
	if err != nil {
		return fmt.Errorf("pdbedit -L: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	// name:uid:LM:NT:[flags]:LCT
	have := map[string]struct{ nt, flags string }{}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Split(l, ":")
		if len(f) >= 5 {
			have[f[0]] = struct{ nt, flags string }{strings.ToUpper(f[3]), f[4]}
		}
	}
	var errs []error
	for _, u := range users {
		if !eligible[u.name] {
			continue
		}
		st := status[u.name]
		nt := ""
		if u.ntSealed != "" {
			if plain, err := secrets.Open(u.ntSealed); err == nil {
				nt = plain
			} else {
				errs = append(errs, fmt.Errorf("SMB password of %s unreadable: %w", u.name, err))
			}
		}
		cur, inDB := have[u.name]
		if nt == "" {
			// Without the hash the old one (if any) is stale: the password
			// was changed where only the bcrypt hash travels (GitOps).
			if inDB {
				sys.Run("", "pdbedit", "-x", "-u", u.name) //nolint:errcheck
			}
			st.Note = "set the password again to use SMB"
			status[u.name] = st
			continue
		}
		if !inDB {
			pw := randomPassword()
			if out, err := sys.Run(pw+"\n"+pw+"\n", "pdbedit", "-a", "-t", "-u", u.name); err != nil {
				errs = append(errs, fmt.Errorf("pdbedit -a %s: %v (%s)", u.name, err, strings.TrimSpace(string(out))))
				st.Note = "the Samba account could not be created"
				status[u.name] = st
				continue
			}
		}
		if !inDB || cur.nt != nt {
			if out, err := sys.Run("", "pdbedit", "-u", u.name, "--set-nt-hash", nt); err != nil {
				errs = append(errs, fmt.Errorf("set the SMB password of %s: %v (%s)", u.name, err, strings.TrimSpace(string(out))))
				st.Note = "the SMB password could not be set"
				status[u.name] = st
				continue
			}
		}
		disabled := strings.Contains(cur.flags, "D")
		switch {
		case u.active && (disabled || !inDB):
			sys.Run("", "smbpasswd", "-e", u.name) //nolint:errcheck
		case !u.active && !disabled:
			sys.Run("", "smbpasswd", "-d", u.name) //nolint:errcheck
		}
		if u.active {
			st.SMB = true
		} else {
			st.Note = "the account is disabled"
		}
		status[u.name] = st
	}
	// Samba users of the accounts removed above.
	for _, name := range removed {
		if _, ok := have[name]; ok {
			sys.Run("", "pdbedit", "-x", "-u", name) //nolint:errcheck
		}
	}
	return errors.Join(errs...)
}

// syncGroups creates the DPlaneOS groups as system groups (GID from FirstID
// up unless set) and sets their members to the eligible users.
func syncGroups(db *sql.DB, sys System, eligible map[string]bool) error {
	rows, err := db.Query(`SELECT id, name, COALESCE(gid, 0) FROM groups ORDER BY id`)
	if err != nil {
		return fmt.Errorf("load groups: %w", err)
	}
	type group struct {
		id   int64
		name string
		gid  int
	}
	var groups []group
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.id, &g.name, &g.gid); err != nil {
			rows.Close()
			return err
		}
		groups = append(groups, g)
	}
	rows.Close()

	members := map[string][]string{}
	mrows, err := db.Query(`SELECT group_name, username FROM group_members ORDER BY username`)
	if err != nil {
		return fmt.Errorf("load group members: %w", err)
	}
	for mrows.Next() {
		var g, u string
		if mrows.Scan(&g, &u) == nil && eligible[u] {
			members[g] = append(members[g], u)
		}
	}
	mrows.Close()

	managed := loadManaged()
	used := map[int]bool{}
	for _, g := range groups {
		used[g.gid] = true
	}
	var errs []error
	now := map[string]bool{}
	for _, g := range groups {
		if !validName(g.name) {
			continue
		}
		gid, exists := sys.LookupGroup(g.name)
		if exists && !managed[g.name] {
			// A system group of that name (e.g. "users", "wheel"): not touched.
			continue
		}
		if g.gid == 0 {
			next := FirstID
			for used[next] || sys.GIDTaken(next) {
				next++
			}
			if _, err := db.Exec(`UPDATE groups SET gid = $1 WHERE id = $2`, next, g.id); err != nil {
				errs = append(errs, fmt.Errorf("give group %s a GID: %w", g.name, err))
				continue
			}
			g.gid, used[next] = next, true
		}
		if !exists {
			if out, err := sys.Run("", "groupadd", "-g", strconv.Itoa(g.gid), g.name); err != nil {
				errs = append(errs, fmt.Errorf("groupadd %s: %v (%s)", g.name, err, strings.TrimSpace(string(out))))
				continue
			}
		} else if gid != g.gid {
			if out, err := sys.Run("", "groupmod", "-g", strconv.Itoa(g.gid), g.name); err != nil {
				errs = append(errs, fmt.Errorf("groupmod %s: %v (%s)", g.name, err, strings.TrimSpace(string(out))))
			}
		}
		now[g.name] = true
		if out, err := sys.Run("", "gpasswd", "-M", strings.Join(members[g.name], ","), g.name); err != nil {
			errs = append(errs, fmt.Errorf("members of %s: %v (%s)", g.name, err, strings.TrimSpace(string(out))))
		}
	}
	for name := range managed {
		if !now[name] {
			if _, exists := sys.LookupGroup(name); exists {
				if out, err := sys.Run("", "groupdel", name); err != nil {
					errs = append(errs, fmt.Errorf("groupdel %s: %v (%s)", name, err, strings.TrimSpace(string(out))))
					now[name] = true // try again next time
				}
			}
		}
	}
	if err := saveManaged(now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func loadManaged() map[string]bool {
	m := map[string]bool{}
	b, err := os.ReadFile(StatePath)
	if err != nil {
		return m
	}
	var names []string
	if json.Unmarshal(b, &names) == nil {
		for _, n := range names {
			m[n] = true
		}
	}
	return m
}

func saveManaged(m map[string]bool) error {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	b, _ := json.Marshal(names)
	if err := os.MkdirAll(filepath.Dir(StatePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(StatePath, b, 0o644)
}

// markedAccounts lists the accounts with the Marker comment.
var markedAccounts = func() []string {
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) >= 5 && f[4] == Marker {
			out = append(out, f[0])
		}
	}
	return out
}

// validName: what useradd accepts everywhere (shadow's default rule).
func validName(n string) bool {
	if n == "" || len(n) > 32 || n[0] == '-' || n[0] >= '0' && n[0] <= '9' {
		return false
	}
	for _, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func nologinShell() string {
	for _, p := range []string{"/run/current-system/sw/bin/nologin", "/usr/sbin/nologin", "/sbin/nologin"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/bin/false"
}

func randomPassword() string {
	b := make([]byte, 24)
	rand.Read(b) //nolint:errcheck
	return hex.EncodeToString(b)
}
