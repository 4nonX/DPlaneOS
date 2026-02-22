package handlers

import (
	"database/sql"
	"log"

	"dplaned/internal/networkdwriter"
	"dplaned/internal/nixwriter"
	"dplaned/internal/reconciler"
)

// Package-level singletons injected by main.go at startup.

var (
	// NetWriter writes systemd-networkd unit files.
	// Works on ALL systemd distros. Changes survive reboot AND nixos-rebuild.
	// Never nil — falls back gracefully when networkd is not active.
	NetWriter *networkdwriter.Writer

	// NixWriter writes dplane-generated.nix fragments.
	// Used ONLY for NixOS-specific settings with no networkd equivalent:
	//   - Firewall ports (networking.firewall)
	//   - Samba globals (services.dplaneos.samba)
	// nil on non-NixOS systems.
	NixWriter *nixwriter.Writer

	// ReconcilerDB for non-systemd-networkd fallback (legacy netlink restore).
	ReconcilerDB *sql.DB
)

func SetNetWriter(w *networkdwriter.Writer)  { NetWriter = w }
func SetNixWriter(w *nixwriter.Writer)       { NixWriter = w }
func SetReconcilerDB(db *sql.DB)             { ReconcilerDB = db }

// ── Network persistence ───────────────────────────────────────────────────────
// These functions are the single call-site for every network change.
// They write to networkd files (primary) and the reconciler DB (fallback).

func persistStaticIP(iface, cidr, gateway string, dns []string) {
	if NetWriter != nil {
		if err := NetWriter.SetStatic(iface, cidr, gateway, dns); err != nil {
			log.Printf("[persist] SetStatic %s: %v", iface, err)
		}
	}
	if ReconcilerDB != nil {
		_ = reconciler.SaveStaticIP(ReconcilerDB, iface, cidr, gateway)
	}
}

func persistDHCP(iface string) {
	if NetWriter != nil {
		if err := NetWriter.SetDHCP(iface, nil); err != nil {
			log.Printf("[persist] SetDHCP %s: %v", iface, err)
		}
	}
	if ReconcilerDB != nil {
		_ = reconciler.SaveDHCP(ReconcilerDB, iface)
	}
}

func persistRemoveInterface(iface string) {
	if NetWriter != nil {
		if err := NetWriter.RemoveInterface(iface); err != nil {
			log.Printf("[persist] RemoveInterface %s: %v", iface, err)
		}
	}
}

func persistVLAN(name, parent string, vid int) {
	if NetWriter != nil {
		if err := NetWriter.SetVLAN(name, parent, vid, "", nil); err != nil {
			log.Printf("[persist] SetVLAN %s: %v", name, err)
		}
	}
	if ReconcilerDB != nil {
		_ = reconciler.SaveVLAN(ReconcilerDB, name, parent, vid)
	}
}

func persistVLANDelete(name string) {
	// need parent+vid to remove all 3 files — best effort on just the .network
	if NetWriter != nil {
		if err := NetWriter.RemoveInterface(name); err != nil {
			log.Printf("[persist] RemoveInterface(vlan) %s: %v", name, err)
		}
	}
	if ReconcilerDB != nil {
		_ = reconciler.DeleteVLAN(ReconcilerDB, name)
	}
}

func persistBond(name string, slaves []string, mode string) {
	if NetWriter != nil {
		if err := NetWriter.SetBond(name, slaves, mode, "", nil); err != nil {
			log.Printf("[persist] SetBond %s: %v", name, err)
		}
	}
	if ReconcilerDB != nil {
		_ = reconciler.SaveBond(ReconcilerDB, name, slaves, mode)
	}
}

func persistBondDelete(name string) {
	if NetWriter != nil {
		if err := NetWriter.RemoveBond(name, nil); err != nil {
			log.Printf("[persist] RemoveBond %s: %v", name, err)
		}
	}
	if ReconcilerDB != nil {
		_ = reconciler.DeleteBond(ReconcilerDB, name)
	}
}

func persistDNS(servers []string) {
	if NetWriter != nil {
		if err := NetWriter.SetGlobalDNS(servers); err != nil {
			log.Printf("[persist] SetGlobalDNS: %v", err)
		}
	}
	// NixWriter.SetDNS removed — networkd handles this now
}

// ── System settings ───────────────────────────────────────────────────────────
// hostname → /etc/hostname via hostnamectl  (already persistent — no extra step)
// timezone → /etc/localtime via timedatectl (already persistent — no extra step)
// NTP      → /etc/systemd/timesyncd.conf   (already persistent — no extra step)
// These functions exist as stubs so handlers compile cleanly; the persistence
// is already handled by the OS-level tool calls in the handler code itself.

func persistHostname(_ string) { /* hostnamectl writes /etc/hostname — already persistent */ }
func persistTimezone(_ string)  { /* timedatectl writes /etc/localtime — already persistent */ }
func persistNTP(_ []string)     { /* timesyncd.conf write in handler — already persistent */ }

// ── NixOS-only: firewall and samba ───────────────────────────────────────────
// These have no systemd equivalent and still require nixos-rebuild switch.
// That is acceptable: firewall and samba global settings change rarely.

func persistFirewallPorts(tcpPorts, udpPorts []int) {
	if NixWriter != nil {
		if err := NixWriter.SetFirewallPorts(tcpPorts, udpPorts); err != nil {
			log.Printf("[persist] SetFirewallPorts: %v", err)
		}
	}
}

func persistSambaGlobals(db *sql.DB) {
	if NixWriter == nil || !NixWriter.IsNixOS() {
		return
	}
	var timeMachine, allowGuest int
	var serverString, workgroup, extraGlobal string
	db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_time_machine'`).Scan(&timeMachine)
	db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_allow_guest'`).Scan(&allowGuest)
	db.QueryRow(`SELECT COALESCE(value,'D-PlaneOS NAS') FROM settings WHERE key='smb_server_string'`).Scan(&serverString)
	db.QueryRow(`SELECT COALESCE(value,'WORKGROUP') FROM settings WHERE key='smb_workgroup'`).Scan(&workgroup)
	db.QueryRow(`SELECT COALESCE(value,'') FROM settings WHERE key='smb_extra_global'`).Scan(&extraGlobal)
	if serverString == "" { serverString = "D-PlaneOS NAS" }
	if workgroup == "" { workgroup = "WORKGROUP" }
	_ = NixWriter.SetSambaGlobals(nixwriter.SambaGlobalOpts{
		Workgroup:    workgroup,
		ServerString: serverString,
		TimeMachine:  timeMachine == 1,
		AllowGuest:   allowGuest == 1,
		ExtraGlobal:  extraGlobal,
	})
}

// persistFirewallFromRequest is called on every ufw rule change.
// On NixOS it logs that a sync via /api/firewall/sync is needed.
func persistFirewallFromRequest(action, portSpec string) {
	if NixWriter == nil || !NixWriter.IsNixOS() {
		return
	}
	log.Printf("[nixwriter] firewall change: action=%s port=%s — sync via POST /api/firewall/sync", action, portSpec)
}
