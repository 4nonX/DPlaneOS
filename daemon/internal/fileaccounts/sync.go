package fileaccounts

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dplaned/internal/secrets"
)

// IDs of the accounts this package creates. Below the ranges Samba maps
// directory users into (idmap, from 3000), above NixOS's own accounts.
const (
	firstID = 1100
	lastID  = 2999

	// gecos marks the accounts this package owns; it never changes or
	// removes any other account of the system.
	gecos = "DPlaneOS user"

	usersGroup = "users"
	noLogin    = "/run/current-system/sw/bin/nologin"
)

// nameRe: what useradd and Samba accept as an account name.
var nameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ValidName reports whether a DPlaneOS user or group name can be a system
// account name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// run executes a command with optional standard input. Replaced in tests.
var run = func(stdin string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// SetPassword stores what the file services need to accept this password
// for the user: the NT hash (SMB, sealed) and a crypt(3) hash (FTP). Call it
// wherever a password is set, then Sync (or RequestSync).
func SetPassword(db *sql.DB, username, password string) error {
	crypt, err := SHA512Crypt(password)
	if err != nil {
		return err
	}
	sealed, err := secrets.Seal(NTHash(password))
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE users SET nt_hash = $2, unix_hash = $3 WHERE username = $1`, username, sealed, crypt)
	return err
}

// HasPassword reports whether SetPassword has run for the user (accounts
// that existed before only get it at their next sign-in).
func HasPassword(db *sql.DB, username string) bool {
	var h string
	_ = db.QueryRow(`SELECT COALESCE(nt_hash, '') FROM users WHERE username = $1`, username).Scan(&h)
	return h != ""
}

type account struct {
	name     string
	uid      int
	ntHash   string
	unixHash string
}

type group struct {
	name    string
	gid     int
	members []string
}

var syncMu sync.Mutex

// Sync brings the system's accounts in line with the database: a Unix and a
// Samba account for every active local user that has a password set here,
// a Unix group for every group. Accounts of users that are gone or disabled
// are removed. Only accounts this package created are touched.
func Sync(db *sql.DB) error {
	syncMu.Lock()
	defer syncMu.Unlock()

	want, err := wantedAccounts(db)
	if err != nil {
		return err
	}
	groups, err := wantedGroups(db)
	if err != nil {
		return err
	}
	have, foreign, err := systemAccounts()
	if err != nil {
		return err
	}
	samba, err := sambaAccounts()
	if err != nil {
		return err
	}

	var problems []string
	note := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	wanted := map[string]bool{}
	for _, a := range want {
		if foreign[a.name] {
			note("%s: a system account of that name exists; no file access", a.name)
			continue
		}
		wanted[a.name] = true
		if uid, ok := have[a.name]; !ok {
			if out, err := run("", "useradd", "-M", "-N", "-g", usersGroup, "-s", noLogin, "-c", gecos, "-u", strconv.Itoa(a.uid), a.name); err != nil {
				note("%s: useradd: %v: %s", a.name, err, strings.TrimSpace(out))
				continue
			}
		} else if uid != a.uid {
			// The files of this user carry the old number.
			note("%s: the system account has ID %d, the database says %d; keeping %d", a.name, uid, a.uid, uid)
		}
		if out, err := run("", "usermod", "-p", a.unixHash, a.name); err != nil {
			note("%s: usermod: %v: %s", a.name, err, strings.TrimSpace(out))
		}
		if !samba[a.name] {
			// Created with a throwaway password; the real hash follows.
			throwaway := NTHash(a.name + time.Now().String())
			if out, err := run(throwaway+"\n"+throwaway+"\n", "pdbedit", "-a", "-t", "-u", a.name); err != nil {
				note("%s: pdbedit -a: %v: %s", a.name, err, strings.TrimSpace(out))
				continue
			}
		}
		if out, err := run("", "pdbedit", "-r", "-u", a.name, "--set-nt-hash="+a.ntHash); err != nil {
			note("%s: pdbedit --set-nt-hash: %v: %s", a.name, err, lastLine(out))
		}
	}

	// Accounts of ours that are no longer wanted.
	for name := range samba {
		if !wanted[name] && !foreign[name] {
			if _, mine := have[name]; mine {
				if out, err := run("", "pdbedit", "-x", "-u", name); err != nil {
					note("%s: pdbedit -x: %v: %s", name, err, strings.TrimSpace(out))
				}
			}
		}
	}
	for name := range have {
		if !wanted[name] {
			if out, err := run("", "userdel", name); err != nil {
				note("%s: userdel: %v: %s", name, err, strings.TrimSpace(out))
			}
		}
	}

	if err := syncGroups(groups, wanted, note); err != nil {
		return err
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("file accounts: %s", strings.Join(problems, "; "))
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// wantedAccounts: active local users with a stored password, each with a
// stable ID (assigned here on first use).
func wantedAccounts(db *sql.DB) ([]account, error) {
	rows, err := db.Query(`SELECT username, COALESCE(uid, 0), COALESCE(nt_hash, ''), COALESCE(unix_hash, '')
		FROM users WHERE active = 1 AND COALESCE(source, 'local') = 'local' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var list []account
	for rows.Next() {
		var a account
		var sealed string
		if err := rows.Scan(&a.name, &a.uid, &sealed, &a.unixHash); err != nil {
			rows.Close()
			return nil, err
		}
		if sealed == "" || a.unixHash == "" || !ValidName(a.name) {
			continue
		}
		if a.ntHash, err = secrets.Open(sealed); err != nil {
			log.Printf("FILE ACCOUNTS: %s: stored SMB hash cannot be opened: %v", a.name, err)
			continue
		}
		list = append(list, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].uid != 0 {
			continue
		}
		uid, err := allocate(db, "users", "uid")
		if err != nil {
			return nil, err
		}
		if _, err := db.Exec(`UPDATE users SET uid = $2 WHERE username = $1`, list[i].name, uid); err != nil {
			return nil, err
		}
		list[i].uid = uid
	}
	return list, nil
}

func wantedGroups(db *sql.DB) ([]group, error) {
	rows, err := db.Query(`SELECT name, COALESCE(gid, 0) FROM groups ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var list []group
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.name, &g.gid); err != nil {
			rows.Close()
			return nil, err
		}
		if ValidName(g.name) {
			list = append(list, g)
		}
	}
	rows.Close()
	for i := range list {
		if list[i].gid < firstID || list[i].gid > lastID {
			gid, err := allocate(db, "groups", "gid")
			if err != nil {
				return nil, err
			}
			if _, err := db.Exec(`UPDATE groups SET gid = $2 WHERE name = $1`, list[i].name, gid); err != nil {
				return nil, err
			}
			list[i].gid = gid
		}
		mrows, err := db.Query(`SELECT username FROM group_members WHERE group_name = $1 ORDER BY username`, list[i].name)
		if err != nil {
			return nil, err
		}
		for mrows.Next() {
			var m string
			if mrows.Scan(&m) == nil {
				list[i].members = append(list[i].members, m)
			}
		}
		mrows.Close()
	}
	return list, nil
}

// allocate returns the lowest free ID of the range in table.column.
func allocate(db *sql.DB, table, column string) (int, error) {
	rows, err := db.Query(`SELECT ` + column + ` FROM ` + table + ` WHERE ` + column + ` IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var n int
		if rows.Scan(&n) == nil {
			used[n] = true
		}
	}
	for id := firstID; id <= lastID; id++ {
		if !used[id] {
			return id, nil
		}
	}
	return 0, fmt.Errorf("no free account ID left between %d and %d", firstID, lastID)
}

// systemAccounts reads /etc/passwd through getent: the accounts this
// package owns (by their comment field) with their IDs, and the names of
// all others.
func systemAccounts() (mine map[string]int, foreign map[string]bool, err error) {
	out, err := run("", "getent", "passwd")
	if err != nil {
		return nil, nil, fmt.Errorf("getent passwd: %v: %s", err, strings.TrimSpace(out))
	}
	mine, foreign = map[string]int{}, map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, ":")
		if len(f) < 7 {
			continue
		}
		if f[4] == gecos {
			mine[f[0]], _ = strconv.Atoi(f[2])
		} else {
			foreign[f[0]] = true
		}
	}
	return mine, foreign, nil
}

// sambaAccounts lists the names in Samba's password database.
func sambaAccounts() (map[string]bool, error) {
	out, err := run("", "pdbedit", "-L")
	if err != nil {
		return nil, fmt.Errorf("pdbedit -L: %v: %s", err, strings.TrimSpace(out))
	}
	names := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if name, _, ok := strings.Cut(line, ":"); ok && ValidName(name) {
			names[name] = true
		}
	}
	return names, nil
}

// syncGroups creates the groups and sets their members (only users that
// have a system account).
func syncGroups(groups []group, accounts map[string]bool, note func(string, ...any)) error {
	out, err := run("", "getent", "group")
	if err != nil {
		return fmt.Errorf("getent group: %v: %s", err, strings.TrimSpace(out))
	}
	type sysGroup struct {
		gid     int
		members string
	}
	system := map[string]sysGroup{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 4 {
			gid, _ := strconv.Atoi(f[2])
			system[f[0]] = sysGroup{gid, f[3]}
		}
	}
	wanted := map[string]bool{}
	for _, g := range groups {
		sg, exists := system[g.name]
		if exists && (sg.gid < firstID || sg.gid > lastID) {
			note("group %s: a system group of that name exists; not managed", g.name)
			continue
		}
		wanted[g.name] = true
		if !exists {
			if out, err := run("", "groupadd", "-g", strconv.Itoa(g.gid), g.name); err != nil {
				note("group %s: groupadd: %v: %s", g.name, err, strings.TrimSpace(out))
				continue
			}
		}
		var members []string
		for _, m := range g.members {
			if accounts[m] {
				members = append(members, m)
			}
		}
		list := strings.Join(members, ",")
		if !exists || sg.members != list {
			if out, err := run("", "gpasswd", "-M", list, g.name); err != nil {
				note("group %s: gpasswd: %v: %s", g.name, err, strings.TrimSpace(out))
			}
		}
	}
	// Groups in our ID range that are gone from the database.
	for name, sg := range system {
		if sg.gid >= firstID && sg.gid <= lastID && !wanted[name] {
			if out, err := run("", "groupdel", name); err != nil {
				note("group %s: groupdel: %v: %s", name, err, strings.TrimSpace(out))
			}
		}
	}
	return nil
}

// ── Background sync ──────────────────────────────────────────────────────────

var syncNow = make(chan struct{}, 1)

// RequestSync asks for a Sync soon (users or groups changed).
func RequestSync() {
	select {
	case syncNow <- struct{}{}:
	default:
	}
}

// Start syncs at start, after every RequestSync and every ten minutes (the
// accounts of another system slot or a restored system are recreated).
func Start(db *sql.DB, stop <-chan struct{}) {
	go func() {
		for {
			if err := Sync(db); err != nil {
				log.Printf("FILE ACCOUNTS: %v", err)
			}
			select {
			case <-stop:
				return
			case <-syncNow:
			case <-time.After(10 * time.Minute):
			}
		}
	}()
}
