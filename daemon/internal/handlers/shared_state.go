package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"

	"dplaned/internal/cmdutil"
	"dplaned/internal/netconf"
	"dplaned/internal/nixwriter"
	"dplaned/internal/reconciler"
)

// Package-level singletons injected by main.go at startup.

var (
	// NixWriter writes dplane-generated.nix fragments.
	// Used ONLY for NixOS-specific settings with no networkd equivalent:
	//   - Firewall ports (networking.firewall)
	//   - Samba globals (services.dplaneos.samba)
	// nil on non-NixOS systems.
	NixWriter *nixwriter.Writer

	// ReconcilerDB holds the network configuration (interfaces, bonds,
	// VLANs, DNS) that internal/netconf renders for systemd-networkd.
	ReconcilerDB *sql.DB

	// GitOpsStatePath is the absolute path to state.yaml.
	GitOpsStatePath string
)

func SetNixWriter(w *nixwriter.Writer) { NixWriter = w }
func SetReconcilerDB(db *sql.DB)       { ReconcilerDB = db }
func SetGitOpsStatePath(p string)      { GitOpsStatePath = p }

// ── Network persistence ───────────────────────────────────────────────────────
// Every network change is saved to the database and then the complete set
// of systemd-networkd files is rendered from it again (internal/netconf).
// Each function returns an error when the change could not be saved or
// applied; callers must not report success then.

// ApplyNetworkConfig renders the saved network configuration and makes
// systemd-networkd and systemd-resolved use it. Called at start (the files
// then match the database whatever happened before) and after every change.
func ApplyNetworkConfig() error {
	if ReconcilerDB == nil {
		return errors.New("no database")
	}
	cfg, err := netconf.Load(ReconcilerDB)
	if err != nil {
		return fmt.Errorf("reading the network configuration: %w", err)
	}
	if !netconf.Active() {
		// Not managed by systemd-networkd: nothing would read the files.
		return nil
	}
	if _, err := netconf.System().Apply(cfg); err != nil {
		return err
	}
	return nil
}

func persistNetwork(what string, saveErr error) error {
	if saveErr == nil {
		saveErr = ApplyNetworkConfig()
	}
	if saveErr == nil {
		return nil
	}
	err := fmt.Errorf("saving %s: %w", what, saveErr)
	log.Printf("[persist] %v", err)
	return err
}

func persistStaticIP(iface, cidr, gateway string) error {
	if ReconcilerDB == nil {
		return persistNetwork("address of "+iface, errors.New("no database"))
	}
	if cidr == "" {
		return persistNetwork("address of "+iface, reconciler.SaveDHCP(ReconcilerDB, iface))
	}
	return persistNetwork("address of "+iface, reconciler.SaveStaticIP(ReconcilerDB, iface, cidr, gateway))
}

// persistVLAN saves a VLAN, with its address if it has one (cidr may be
// empty), and applies both in one step.
func persistVLAN(name, parent string, vid int, cidr string) error {
	if ReconcilerDB == nil {
		return persistNetwork("VLAN "+name, errors.New("no database"))
	}
	err := reconciler.SaveVLAN(ReconcilerDB, name, parent, vid)
	if err == nil && cidr != "" {
		err = reconciler.SaveStaticIP(ReconcilerDB, name, cidr, "")
	}
	return persistNetwork("VLAN "+name, err)
}

func persistVLANDelete(name string) error {
	if ReconcilerDB == nil {
		return persistNetwork("removal of VLAN "+name, errors.New("no database"))
	}
	err := reconciler.DeleteVLAN(ReconcilerDB, name)
	if err == nil {
		err = reconciler.DeleteInterface(ReconcilerDB, name)
	}
	return persistNetwork("removal of VLAN "+name, err)
}

// persistBond saves a bond, with its address if it has one (cidr may be
// empty: DHCP), and applies both in one step.
func persistBond(name string, slaves []string, mode, cidr string) error {
	if ReconcilerDB == nil {
		return persistNetwork("bond "+name, errors.New("no database"))
	}
	err := reconciler.SaveBond(ReconcilerDB, name, slaves, mode)
	if err == nil && cidr != "" {
		err = reconciler.SaveStaticIP(ReconcilerDB, name, cidr, "")
	}
	return persistNetwork("bond "+name, err)
}

func persistBondDelete(name string) error {
	if ReconcilerDB == nil {
		return persistNetwork("removal of bond "+name, errors.New("no database"))
	}
	err := reconciler.DeleteBond(ReconcilerDB, name)
	if err == nil {
		err = reconciler.DeleteInterface(ReconcilerDB, name)
	}
	return persistNetwork("removal of bond "+name, err)
}

func persistDNS(servers, search []string) error {
	if ReconcilerDB == nil {
		return persistNetwork("DNS servers", errors.New("no database"))
	}
	return persistNetwork("DNS servers", netconf.SaveDNS(ReconcilerDB, servers, search))
}

// ── System settings ───────────────────────────────────────────────────────────
// On an installed system the settings are saved in the system state file and
// applied at once and at every boot (internal/sysapply); the system is an
// image, nothing rebuilds it. Elsewhere hostnamectl, timedatectl and
// timesyncd.conf do both.

// onNixOS reports whether settings go through the system state file.
func onNixOS() bool { return NixWriter != nil && NixWriter.IsNixOS() }

// persistHostname sets the host name, now and for every boot.
func persistHostname(name string) error {
	if onNixOS() {
		return NixWriter.SetHostname(name)
	}
	if out, err := cmdutil.RunFast("hostnamectl", "set-hostname", name); err != nil {
		return fmt.Errorf("hostnamectl: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// persistTimezone sets the time zone, now and for every boot.
func persistTimezone(tz string) error {
	if onNixOS() {
		return NixWriter.SetTimezone(tz)
	}
	if out, err := cmdutil.RunFast("timedatectl", "set-timezone", tz); err != nil {
		return fmt.Errorf("timedatectl: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func persistNTP(servers []string) error {
	if onNixOS() {
		if err := NixWriter.SetNTP(servers); err != nil {
			return fmt.Errorf("saving NTP servers: %w", err)
		}
	}
	return nil
}

// ── Samba ─────────────────────────────────────────────────────────────────────

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
