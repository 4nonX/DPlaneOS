package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"

	"dplaned/internal/networkdwriter"
	"dplaned/internal/nixwriter"
)

// Package-level singletons injected by main.go at startup.

var (
	// NetWriter writes systemd-networkd unit files.
	// Works on ALL systemd distros. Changes survive reboot AND nixos-rebuild.
	// Never nil - falls back gracefully when networkd is not active.
	NetWriter *networkdwriter.Writer

	// NixWriter writes dplane-generated.nix fragments.
	// Used ONLY for NixOS-specific settings with no networkd equivalent:
	//   - Firewall ports (networking.firewall)
	//   - Samba globals (services.dplaneos.samba)
	// nil on non-NixOS systems.
	NixWriter *nixwriter.Writer

	// ReconcilerDB for non-systemd-networkd fallback (legacy netlink restore).
	ReconcilerDB *sql.DB

	// GitOpsStatePath is the absolute path to state.yaml.
	GitOpsStatePath string
)

func SetNetWriter(w *networkdwriter.Writer) { NetWriter = w }
func SetNixWriter(w *nixwriter.Writer)      { NixWriter = w }
func SetReconcilerDB(db *sql.DB)            { ReconcilerDB = db }
func SetGitOpsStatePath(p string)           { GitOpsStatePath = p }

// Network configuration goes through NetWriter (network_config.go).

func persistErr(what string, errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	err := fmt.Errorf("saving %s: %w", what, errors.Join(errs...))
	log.Printf("[persist] %v", err)
	return err
}

// ── System settings ───────────────────────────────────────────────────────────
// Elsewhere hostnamectl/timedatectl/timesyncd.conf persist these themselves.
// On NixOS /etc/hostname, /etc/localtime and timesyncd.conf belong to the
// system configuration (read-only, rewritten on rebuild), so the values go
// into the JSON bridge (networking.hostName, time.timeZone,
// services.timesyncd.servers) and apply on the next nixos-rebuild.

// onNixOS reports whether settings must be persisted through the NixOS bridge.
func onNixOS() bool { return NixWriter != nil && NixWriter.IsNixOS() }

func persistHostname(name string) {
	if onNixOS() {
		if err := NixWriter.SetHostname(name); err != nil {
			log.Printf("[persist] SetHostname: %v", err)
		}
	}
}

func persistTimezone(tz string) {
	if onNixOS() {
		if err := NixWriter.SetTimezone(tz); err != nil {
			log.Printf("[persist] SetTimezone: %v", err)
		}
	}
}

func persistNTP(servers []string) error {
	if onNixOS() {
		if err := NixWriter.SetNTP(servers); err != nil {
			return persistErr("NTP servers", []error{err})
		}
	}
	return nil
}

// ── NixOS-only: firewall and samba ───────────────────────────────────────────
// These have no systemd equivalent and still require nixos-rebuild switch.
// That is acceptable: firewall and samba global settings change rarely.

func persistSambaGlobals(db *sql.DB) {
	if NixWriter == nil || !NixWriter.IsNixOS() {
		return
	}
	var timeMachine, allowGuest int
	var serverString, workgroup, extraGlobal string
	db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_time_machine'`).Scan(&timeMachine)
	db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_allow_guest'`).Scan(&allowGuest)
	db.QueryRow(`SELECT COALESCE(value,'DPlaneOS NAS') FROM settings WHERE key='smb_server_string'`).Scan(&serverString)
	db.QueryRow(`SELECT COALESCE(value,'WORKGROUP') FROM settings WHERE key='smb_workgroup'`).Scan(&workgroup)
	db.QueryRow(`SELECT COALESCE(value,'') FROM settings WHERE key='smb_extra_global'`).Scan(&extraGlobal)
	if serverString == "" {
		serverString = "DPlaneOS NAS"
	}
	if workgroup == "" {
		workgroup = "WORKGROUP"
	}
	// Apple SMB extensions (fruit globals in modules/samba.nix) are needed as
	// soon as any share is a Time Machine target, not only with the global toggle.
	var tmShares int
	db.QueryRow(`SELECT COUNT(*) FROM smb_shares WHERE enabled = 1 AND time_machine = 1`).Scan(&tmShares)
	_ = NixWriter.SetSambaGlobals(nixwriter.SambaGlobalOpts{
		Workgroup:    workgroup,
		ServerString: serverString,
		TimeMachine:  timeMachine == 1 || tmShares > 0,
		AllowGuest:   allowGuest == 1,
		ExtraGlobal:  extraGlobal,
	})
}
