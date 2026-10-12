package handlers

import (
	"database/sql"
	"log"
	"os"
	"os/user"
	"strconv"

	"dplaned/internal/fileaccounts"
)

// setFilePassword makes SMB and FTP accept the password just set for the
// user (see internal/fileaccounts). The web login does not depend on it: a
// failure is logged and shown on the Users page as "no file access yet".
func setFilePassword(db *sql.DB, username, password string) {
	if err := fileaccounts.SetPassword(db, username, password); err != nil {
		log.Printf("FILE ACCOUNTS: storing the file service password of %s: %v", username, err)
		return
	}
	// The account number is chosen here, with the password, so that it is
	// part of what the other nodes of a cluster receive for this user.
	if err := fileaccounts.EnsureUID(db, username); err != nil {
		log.Printf("FILE ACCOUNTS: account number of %s: %v", username, err)
	}
	fileaccounts.RequestSync()
}

// prepareShareRoot makes a fresh folder usable as a share: a new dataset's
// mountpoint belongs to root with mode 755, so no user could write to it
// over SMB. It is given to the group "users" (which every file user is in)
// and made group-writable, with the setgid bit so new entries keep the
// group. A folder that already has other owners or permissions is left as
// it is: someone set those on purpose.
func prepareShareRoot(path string) {
	real, err := resolveExisting(path)
	if err != nil {
		return
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0755 || info.Mode()&os.ModeSetgid != 0 {
		return
	}
	uid, gid, ok := ownerOf(info)
	if !ok || uid != 0 || gid != 0 {
		return
	}
	g, err := user.LookupGroup("users")
	if err != nil {
		log.Printf("SHARES: group users not found, %s stays root:root 755: %v", real, err)
		return
	}
	usersGID, err := strconv.Atoi(g.Gid)
	if err != nil {
		return
	}
	if err := os.Chown(real, 0, usersGID); err != nil {
		log.Printf("SHARES: chgrp users %s: %v", real, err)
		return
	}
	if err := os.Chmod(real, 0775|os.ModeSetgid); err != nil {
		log.Printf("SHARES: chmod 2775 %s: %v", real, err)
	}
}
