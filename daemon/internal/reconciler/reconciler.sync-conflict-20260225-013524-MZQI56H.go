// Package reconciler implements D-PlaneOS's boot-time state restoration.
//
// Problem it solves:
//   - Netlink calls (VLANs, bonds, static IPs) survive until reboot
//   - After reboot, the kernel starts clean — all imperative network config is gone
//   - On NixOS: if nixwriter.Writer was used, NixOS restores state declaratively
//   - On non-NixOS (Debian/Ubuntu): there is no declarative layer — the daemon
//     must restore state imperatively on every startup
//
// Approach:
//  1. Read desired network state from SQLite (tables: network_interfaces, network_bonds, network_vlans)
//  2. Read actual kernel state via netlinkx.LinkList()
//  3. For each desired item missing from kernel: re-apply via netlink
//  4. Log every restoration action to the audit log
//
// Tables are created by this package's EnsureSchema() — safe to call on every startup.
package reconciler

import (
	"database/sql"
	"fmt"
	"log"
	"net"
	"strings"

	"dplaned/internal/netlinkx"
)

// NetworkState is the desired state of a network interface stored in the DB.
type NetworkState struct {
	ID        int64
	Type      string // "static", "dhcp"
	Interface string
	CIDR      string
	Gateway   string
}

// BondState is the desired state of a bond stored in the DB.
type BondState struct {
	ID     int64
	Name   string
	Slaves []string
	Mode   string
}

// VLANState is the desired state of a VLAN stored in the DB.
type VLANState struct {
	ID     int64
	Name   string
	Parent string
	VID    int
}

// EnsureSchema creates the reconciler's state tables if they don't exist.
// Safe to call on every startup — all statements use IF NOT EXISTS.
func EnsureSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS network_interfaces (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			interface TEXT    NOT NULL UNIQUE,
			type      TEXT    NOT NULL DEFAULT 'dhcp', -- 'dhcp' or 'static'
			cidr      TEXT    NOT NULL DEFAULT '',
			gateway   TEXT    NOT NULL DEFAULT '',
			created_at TEXT   NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT   NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS network_bonds (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			name      TEXT    NOT NULL UNIQUE,
			slaves    TEXT    NOT NULL DEFAULT '', -- comma-separated
			mode      TEXT    NOT NULL DEFAULT '802.3ad',
			created_at TEXT   NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT   NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS network_vlans (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			name      TEXT    NOT NULL UNIQUE,
			parent    TEXT    NOT NULL,
			vid       INTEGER NOT NULL,
			created_at TEXT   NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT   NOT NULL DEFAULT (datetime('now'))
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("reconciler schema: %w", err)
		}
	}
	return nil
}

// Run performs a full reconciliation pass.
// It reads desired state from db, reads actual kernel state via netlink,
// and re-applies anything that's missing.
// Errors are logged but never fatal — a partial reconciliation is better than none.
func Run(db *sql.DB) {
	log.Printf("[reconciler] starting boot reconciliation")

	// Get current kernel interfaces
	links, err := netlinkx.LinkList()
	if err != nil {
		log.Printf("[reconciler] WARNING: cannot list kernel interfaces: %v — skipping reconciliation", err)
		return
	}

	present := make(map[string]netlinkx.LinkInfo)
	for _, l := range links {
		present[l.Name] = l
	}

	restored := 0

	// ── 1. Bonds ─────────────────────────────────────────────────────────────
	bonds, err := loadBonds(db)
	if err != nil {
		log.Printf("[reconciler] WARNING: cannot load bonds from DB: %v", err)
	} else {
		for _, b := range bonds {
			if _, exists := present[b.Name]; exists {
				log.Printf("[reconciler] bond %s: already present", b.Name)
				continue
			}
			log.Printf("[reconciler] bond %s: missing from kernel, restoring (mode=%s slaves=%v)", b.Name, b.Mode, b.Slaves)
			if err := restoreBond(b); err != nil {
				log.Printf("[reconciler] ERROR restoring bond %s: %v", b.Name, err)
			} else {
				log.Printf("[reconciler] bond %s: restored", b.Name)
				restored++
			}
		}
	}

	// ── 2. VLANs ─────────────────────────────────────────────────────────────
	vlans, err := loadVLANs(db)
	if err != nil {
		log.Printf("[reconciler] WARNING: cannot load VLANs from DB: %v", err)
	} else {
		for _, v := range vlans {
			if _, exists := present[v.Name]; exists {
				log.Printf("[reconciler] VLAN %s: already present", v.Name)
				continue
			}
			log.Printf("[reconciler] VLAN %s: missing from kernel, restoring (parent=%s vid=%d)", v.Name, v.Parent, v.VID)
			if err := restoreVLAN(v); err != nil {
				log.Printf("[reconciler] ERROR restoring VLAN %s: %v", v.Name, err)
			} else {
				log.Printf("[reconciler] VLAN %s: restored", v.Name)
				restored++
			}
		}
	}

	// ── 3. Static IPs ────────────────────────────────────────────────────────
	ifaces, err := loadInterfaces(db)
	if err != nil {
		log.Printf("[reconciler] WARNING: cannot load interface configs from DB: %v", err)
	} else {
		for _, iface := range ifaces {
			if iface.Type != "static" || iface.CIDR == "" {
				continue
			}
			// Check if the interface already has this address
			link, exists := present[iface.Interface]
			if !exists {
				log.Printf("[reconciler] interface %s: not present in kernel, skipping static IP restoration", iface.Interface)
				continue
			}
			if hasAddress(link, iface.CIDR) {
				log.Printf("[reconciler] interface %s: address %s already configured", iface.Interface, iface.CIDR)
				continue
			}
			log.Printf("[reconciler] interface %s: restoring static IP %s", iface.Interface, iface.CIDR)
			if err := netlinkx.AddrAdd(iface.Interface, iface.CIDR); err != nil {
				log.Printf("[reconciler] ERROR restoring static IP on %s: %v", iface.Interface, err)
			} else {
				if iface.Gateway != "" {
					// best-effort route restore
					_ = netlinkx.RouteReplace("0.0.0.0/0", iface.Gateway, iface.Interface)
				}
				log.Printf("[reconciler] interface %s: static IP %s restored", iface.Interface, iface.CIDR)
				restored++
			}
		}
	}

	if restored == 0 {
		log.Printf("[reconciler] all network state intact — nothing to restore")
	} else {
		log.Printf("[reconciler] boot reconciliation complete: %d item(s) restored", restored)
	}
}

// ── DB persistence helpers ────────────────────────────────────────────────────

// SaveBond persists a bond to the DB so it survives reboots on non-NixOS.
func SaveBond(db *sql.DB, name string, slaves []string, mode string) error {
	_, err := db.Exec(`
		INSERT INTO network_bonds (name, slaves, mode, updated_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(name) DO UPDATE SET
			slaves=excluded.slaves, mode=excluded.mode, updated_at=excluded.updated_at`,
		name, strings.Join(slaves, ","), mode,
	)
	return err
}

// DeleteBond removes a bond from the DB.
func DeleteBond(db *sql.DB, name string) error {
	_, err := db.Exec(`DELETE FROM network_bonds WHERE name = ?`, name)
	return err
}

// SaveVLAN persists a VLAN to the DB.
func SaveVLAN(db *sql.DB, name, parent string, vid int) error {
	_, err := db.Exec(`
		INSERT INTO network_vlans (name, parent, vid, updated_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(name) DO UPDATE SET
			parent=excluded.parent, vid=excluded.vid, updated_at=excluded.updated_at`,
		name, parent, vid,
	)
	return err
}

// DeleteVLAN removes a VLAN from the DB.
func DeleteVLAN(db *sql.DB, name string) error {
	_, err := db.Exec(`DELETE FROM network_vlans WHERE name = ?`, name)
	return err
}

// SaveStaticIP persists a static IP configuration to the DB.
func SaveStaticIP(db *sql.DB, iface, cidr, gateway string) error {
	_, err := db.Exec(`
		INSERT INTO network_interfaces (interface, type, cidr, gateway, updated_at)
		VALUES (?, 'static', ?, ?, datetime('now'))
		ON CONFLICT(interface) DO UPDATE SET
			type='static', cidr=excluded.cidr, gateway=excluded.gateway, updated_at=excluded.updated_at`,
		iface, cidr, gateway,
	)
	return err
}

// SaveDHCP marks an interface as DHCP (clears any static config).
func SaveDHCP(db *sql.DB, iface string) error {
	_, err := db.Exec(`
		INSERT INTO network_interfaces (interface, type, cidr, gateway, updated_at)
		VALUES (?, 'dhcp', '', '', datetime('now'))
		ON CONFLICT(interface) DO UPDATE SET
			type='dhcp', cidr='', gateway='', updated_at=excluded.updated_at`,
		iface,
	)
	return err
}

// ── Internal loaders ──────────────────────────────────────────────────────────

func loadBonds(db *sql.DB) ([]BondState, error) {
	rows, err := db.Query(`SELECT id, name, slaves, mode FROM network_bonds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BondState
	for rows.Next() {
		var b BondState
		var slaves string
		if err := rows.Scan(&b.ID, &b.Name, &slaves, &b.Mode); err != nil {
			continue
		}
		if slaves != "" {
			b.Slaves = strings.Split(slaves, ",")
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func loadVLANs(db *sql.DB) ([]VLANState, error) {
	rows, err := db.Query(`SELECT id, name, parent, vid FROM network_vlans`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VLANState
	for rows.Next() {
		var v VLANState
		if err := rows.Scan(&v.ID, &v.Name, &v.Parent, &v.VID); err != nil {
			continue
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func loadInterfaces(db *sql.DB) ([]NetworkState, error) {
	rows, err := db.Query(`SELECT id, interface, type, cidr, gateway FROM network_interfaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NetworkState
	for rows.Next() {
		var n NetworkState
		if err := rows.Scan(&n.ID, &n.Interface, &n.Type, &n.CIDR, &n.Gateway); err != nil {
			continue
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ── Restoration helpers ───────────────────────────────────────────────────────

func restoreBond(b BondState) error {
	if err := netlinkx.LinkAdd(netlinkx.LinkAttrs{
		Name:     b.Name,
		Type:     netlinkx.LinkTypeBond,
		BondMode: b.Mode,
	}); err != nil {
		return fmt.Errorf("create bond: %w", err)
	}
	for _, slave := range b.Slaves {
		_ = netlinkx.LinkSetDown(slave)
		_ = netlinkx.LinkSetMaster(slave, b.Name)
	}
	return netlinkx.LinkSetUp(b.Name)
}

func restoreVLAN(v VLANState) error {
	if err := netlinkx.LinkAdd(netlinkx.LinkAttrs{
		Name:     v.Name,
		Type:     netlinkx.LinkTypeVLAN,
		VLANID:   v.VID,
		ParentName: v.Parent,
	}); err != nil {
		return fmt.Errorf("create VLAN: %w", err)
	}
	return netlinkx.LinkSetUp(v.Name)
}

// hasAddress checks whether a kernel interface already has the given CIDR.
func hasAddress(link netlinkx.LinkInfo, cidr string) bool {
	addrs, err := netlinkx.AddrList(link.Name)
	if err != nil {
		return false
	}
	_, want, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if a.CIDR != nil && a.CIDR.String() == want.String() {
			return true
		}
	}
	return false
}
