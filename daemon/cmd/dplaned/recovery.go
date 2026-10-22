package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"dplaned/internal/configbackup"
	"dplaned/internal/database"
	"dplaned/internal/handlers"
	"dplaned/internal/secrets"
	"dplaned/internal/shareusers"
)

// runRecovery implements the -recover-* flags. It returns the exit code.
func runRecovery(dsn, secretsKey string, list bool, resetUser, totpUser string) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "dplaned: "+format+"\n", a...)
		return 1
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fail("database: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return fail("database not reachable (is postgresql running?): %v", err)
	}
	if err := database.RunMigrations(db); err != nil {
		return fail("database schema: %v", err)
	}
	switch {
	case list:
		users, err := handlers.RecoverListUsers(db)
		if err != nil {
			return fail("%v", err)
		}
		for _, u := range users {
			state := "active"
			if !u.Active {
				state = "inactive"
			}
			twofa := ""
			if u.TOTP {
				twofa = " 2FA"
			}
			fmt.Printf("%s\t%s\t%s%s\n", u.Username, u.Role, state, twofa)
		}
	case resetUser != "":
		// The SMB password is sealed with the secrets key; accounts are
		// applied to the system as at daemon start.
		if err := secrets.Init(secretsKey); err != nil {
			return fail("secrets key: %v", err)
		}
		shareusers.EnableHost()
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return fail("no password on standard input")
		}
		if err := handlers.RecoverResetPassword(db, resetUser, strings.TrimRight(line, "\r\n")); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("Password of %s set; the account is active and its sessions were ended.\n", resetUser)
	case totpUser != "":
		if err := handlers.RecoverDisableTOTP(db, totpUser); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("Two-factor authentication of %s is off; set it up again after logging in.\n", totpUser)
	}
	return 0
}

// runRecoveryRestore stages a configuration backup from the console (the
// web UI's restore without the web UI). The next start of dplaned applies it.
func runRecoveryRestore(path, configDir string, network bool) int {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dplaned: %v\n", err)
		return 1
	}
	defer f.Close()
	m, err := configbackup.Stage(f, configDir, database.LatestVersion(), configbackup.Options{Secrets: true, Network: network})
	if err != nil {
		fmt.Fprintf(os.Stderr, "dplaned: %v\n", err)
		return 1
	}
	fmt.Printf("Backup of %s (%s, DPlaneOS %s) checked and staged. Apply it with: systemctl restart dplaned\n",
		m.Hostname, m.CreatedAt.Format("2006-01-02 15:04"), m.Version)
	return 0
}
