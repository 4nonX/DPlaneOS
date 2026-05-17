package handlers

// kerberos.go - background Kerberos ticket renewal for Active Directory domains.
//
// StartKerberosRenewer runs a goroutine that wakes every 15 minutes and:
//  1. Queries ad_domains for any joined domain.
//  2. Checks whether a valid TGT exists (klist -s).
//  3. If the ticket is expiring or absent, tries kinit -R (renewable renewal).
//  4. Falls back to kinit -k -t /etc/krb5.keytab <principal> (machine keytab).
//  5. Updates ad_domains.last_kinit_at / kinit_ok on each attempt.
//
// The goroutine exits when ctx is cancelled (daemon shutdown).

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

const kerberosRenewInterval = 15 * time.Minute

// StartKerberosRenewer starts the background Kerberos ticket renewal loop.
// Safe to call on non-AD systems: if no joined domain is found the loop is
// effectively a no-op that wakes every 15 minutes and does nothing.
func StartKerberosRenewer(ctx context.Context, db *sql.DB) {
	go kerberosRenewLoop(ctx, db)
}

func kerberosRenewLoop(ctx context.Context, db *sql.DB) {
	ticker := time.NewTicker(kerberosRenewInterval)
	defer ticker.Stop()

	// Run once immediately at startup so we don't wait 15 minutes on first boot.
	kerberosRenewTick(db)

	for {
		select {
		case <-ctx.Done():
			log.Printf("Kerberos renewer: shutting down")
			return
		case <-ticker.C:
			kerberosRenewTick(db)
		}
	}
}

func kerberosRenewTick(db *sql.DB) {
	// Find joined domains that need renewal.
	rows, err := db.Query(`SELECT name, realm, kinit_principal
		FROM ad_domains WHERE domain_joined=true AND enabled=true`)
	if err != nil {
		log.Printf("Kerberos renewer: DB query failed: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var name, realm, principal string
		if err := rows.Scan(&name, &realm, &principal); err != nil {
			continue
		}
		renewKerberosDomain(db, name, realm, principal)
	}
}

// renewKerberosDomain attempts to renew the Kerberos TGT for one domain.
// Strategy (in order):
//  1. klist -s: if the TGT is still valid and not expiring within the next
//     tick interval, do nothing.
//  2. kinit -R: renew the existing TGT (requires renewable ticket).
//  3. kinit -k -t /etc/krb5.keytab <principal>: use machine account keytab.
func renewKerberosDomain(db *sql.DB, name, realm, principal string) {
	// 1. Check if existing TGT is valid.
	if ticketValid() {
		return
	}

	log.Printf("Kerberos renewer: TGT for domain %s expired or missing, renewing", name)

	ok := false
	// 2. Try renewable renewal.
	if err := runKinitRenew(); err == nil {
		log.Printf("Kerberos renewer: kinit -R succeeded for domain %s", name)
		ok = true
	} else {
		log.Printf("Kerberos renewer: kinit -R failed for %s: %v - trying keytab", name, err)
		// 3. Fall back to machine account keytab.
		kp := principal
		if kp == "" {
			// Default machine principal: host@REALM
			kp = "host@" + realm
		}
		if err := runKinitKeytab("/etc/krb5.keytab", kp); err == nil {
			log.Printf("Kerberos renewer: keytab kinit succeeded for domain %s (%s)", name, kp)
			ok = true
		} else {
			log.Printf("Kerberos renewer: keytab kinit failed for domain %s: %v", name, err)
		}
	}

	// Update domain renewal status.
	db.Exec(`UPDATE ad_domains SET last_kinit_at=NOW(), kinit_ok=$1, updated_at=NOW() WHERE name=$2`, ok, name)
}

// ticketValid returns true if klist -s exits 0 (valid, non-expired TGT exists).
func ticketValid() bool {
	cmd := exec.Command("klist", "-s")
	return cmd.Run() == nil
}

// runKinitRenew attempts to renew the current TGT via kinit -R.
func runKinitRenew() error {
	out, err := exec.Command("kinit", "-R").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runKinitKeytab obtains a TGT using the machine account keytab.
func runKinitKeytab(keytab, principal string) error {
	out, err := exec.Command("kinit", "-k", "-t", keytab, principal).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
