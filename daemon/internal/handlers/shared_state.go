package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"slices"

	"dplaned/internal/networkdwriter"
	"dplaned/internal/nixwriter"
	"dplaned/internal/reconciler"
	"strconv"
	"strings"
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

func SetNetWriter(w *networkdwriter.Writer)  { NetWriter = w }
func SetNixWriter(w *nixwriter.Writer)       { NixWriter = w }
func SetReconcilerDB(db *sql.DB)             { ReconcilerDB = db }
func SetGitOpsStatePath(p string)           { GitOpsStatePath = p }

// ── Network persistence ───────────────────────────────────────────────────────
// These functions are the single call-site for every network change.
// They write to networkd files (primary) and the reconciler DB (fallback).

// persistStaticIP, persistVLAN and persistBond return an error when the
// change could not be saved; callers must not report success then, since the
// setting would be lost at the next reboot.

func persistStaticIP(iface, cidr, gateway string, dns []string) error {
	var errs []error
	if NetWriter != nil {
		if err := NetWriter.SetStatic(iface, cidr, gateway, dns); err != nil {
			errs = append(errs, fmt.Errorf("networkd file: %w", err))
		}
	}
	if ReconcilerDB != nil {
		if err := reconciler.SaveStaticIP(ReconcilerDB, iface, cidr, gateway); err != nil {
			errs = append(errs, fmt.Errorf("reconciler: %w", err))
		}
	}
	return persistErr("static IP "+iface, errs)
}

func persistVLAN(name, parent string, vid int) error {
	var errs []error
	if NetWriter != nil {
		if err := NetWriter.SetVLAN(name, parent, vid, "", nil); err != nil {
			errs = append(errs, fmt.Errorf("networkd file: %w", err))
		}
	}
	if ReconcilerDB != nil {
		if err := reconciler.SaveVLAN(ReconcilerDB, name, parent, vid); err != nil {
			errs = append(errs, fmt.Errorf("reconciler: %w", err))
		}
	}
	return persistErr("VLAN "+name, errs)
}

func persistErr(what string, errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	err := fmt.Errorf("saving %s: %w", what, errors.Join(errs...))
	log.Printf("[persist] %v", err)
	return err
}

func persistVLANDelete(name string) {
	// need parent+vid to remove all 3 files - best effort on just the .network
	if NetWriter != nil {
		if err := NetWriter.RemoveInterface(name); err != nil {
			log.Printf("[persist] RemoveInterface(vlan) %s: %v", name, err)
		}
	}
	if ReconcilerDB != nil {
		_ = reconciler.DeleteVLAN(ReconcilerDB, name)
	}
}

func persistBond(name string, slaves []string, mode string) error {
	var errs []error
	if NetWriter != nil {
		if err := NetWriter.SetBond(name, slaves, mode, "", nil); err != nil {
			errs = append(errs, fmt.Errorf("networkd file: %w", err))
		}
	}
	if ReconcilerDB != nil {
		if err := reconciler.SaveBond(ReconcilerDB, name, slaves, mode); err != nil {
			errs = append(errs, fmt.Errorf("reconciler: %w", err))
		}
	}
	return persistErr("bond "+name, errs)
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

func persistDNS(servers []string) error {
	if NetWriter != nil {
		if err := NetWriter.SetGlobalDNS(servers); err != nil {
			return persistErr("DNS servers", []error{err})
		}
	}
	// NixWriter.SetDNS removed - networkd handles this now
	return nil
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
	if serverString == "" { serverString = "DPlaneOS NAS" }
	if workgroup == "" { workgroup = "WORKGROUP" }
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

// persistFirewallFromRequest is called on every ufw rule change.
// On NixOS it updates the NixWriter state with the new port lists.
func persistFirewallFromRequest(action, portSpec string) {
	if NixWriter == nil || !NixWriter.IsNixOS() {
		return
	}

	// Parse port and protocol (e.g. "80", "80/tcp", "53/udp")
	portStr := portSpec
	proto := "tcp"
	if strings.Contains(portSpec, "/") {
		parts := strings.SplitN(portSpec, "/", 2)
		portStr = parts[0]
		proto = parts[1]
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Printf("[persist] invalid port spec: %s", portSpec)
		return
	}

	state := NixWriter.State()
	tcpPorts := state.FirewallTCP
	udpPorts := state.FirewallUDP

	modified := false
	switch action {
	case "allow":
		if proto == "tcp" {
			if !containsPort(tcpPorts, port) {
				tcpPorts = append(tcpPorts, port)
				modified = true
			}
		} else {
			if !containsPort(udpPorts, port) {
				udpPorts = append(udpPorts, port)
				modified = true
			}
		}
	case "deny", "delete":
		if proto == "tcp" {
			tcpPorts, modified = removePort(tcpPorts, port)
		} else {
			udpPorts, modified = removePort(udpPorts, port)
		}
	}

	if modified {
		if err := NixWriter.SetFirewallPorts(tcpPorts, udpPorts); err != nil {
			log.Printf("[persist] SetFirewallPorts failed: %v", err)
		}
	}
}

func containsPort(ports []int, port int) bool {
	return slices.Contains(ports, port)
}

func removePort(ports []int, port int) ([]int, bool) {
	newPorts := []int{}
	found := false
	for _, p := range ports {
		if p == port {
			found = true
			continue
		}
		newPorts = append(newPorts, p)
	}
	return newPorts, found
}

