package gitops

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"dplaned/internal/cmdutil"
	"dplaned/internal/nixwriter"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  LIVE STATE READER
//
//  Reads the actual state of the system from ZFS commands and the DB.
//  This is the ground truth against which desired state is diffed.
// ═══════════════════════════════════════════════════════════════════════════════

// LivePool is the observed state of a ZFS pool.
type LivePool struct {
	Name   string
	GUID   string
	Health string   // ONLINE, DEGRADED, FAULTED, UNAVAIL, REMOVED
	Disks  []string // /dev/disk/by-id/... paths from `zpool status`
}

// LiveDataset is the observed state of a ZFS dataset.
type LiveDataset struct {
	Name        string
	Used        uint64 // bytes - 0 means genuinely empty or unknown
	Avail       uint64
	Compression string
	Atime       string
	Mountpoint  string
	Quota       string // raw string from zfs get, e.g. "2000000000" or "none"
	Encrypted   bool
}

// LiveShare is an SMB share as stored in the daemon DB.
type LiveShare struct {
	ID         int64
	Name       string
	Path       string
	ReadOnly   bool
	ValidUsers string
	Comment    string
	GuestOK    bool
	Enabled    bool
}
 
// LiveNFSExport is an NFS export from the daemon DB.
type LiveNFSExport struct {
	ID      int64
	Path    string
	Clients string
	Options string
	Enabled bool
}

// LiveUser is a user from the daemon DB.
type LiveUser struct {
	ID           int64
	Username     string
	PasswordHash string
	Email        string
	Role         string
	Active       bool
}

// LiveGroup is a group from the daemon DB.
type LiveGroup struct {
	ID          int64
	Name        string
	Description string
	GID         int
	Members     []string // usernames
}

// LiveReplication is a replication schedule from the JSON file.
type LiveReplication struct {
	Name              string
	SourceDataset     string
	RemoteHost        string
	RemoteUser        string
	RemotePort        int
	RemotePool        string
	SSHKeyPath        string
	Interval          string
	TriggerOnSnapshot bool
	Compress          bool
	RateLimitMB       int
	Enabled           bool
}


// LiveState is the complete observed system state.
type LiveState struct {
	Pools    []LivePool
	Datasets []LiveDataset
	Shares   []LiveShare
	NFS      []LiveNFSExport
	Stacks      []LiveStack
	System      *nixwriter.DPlaneState
	Users       []LiveUser
	Groups      []LiveGroup
	Replication []LiveReplication
	LDAP        *DesiredLDAP
	ACME        *DesiredACME
	Certificates []DesiredCertificate
	SMART        []DesiredSMARTTask
}

// ReadLiveState collects all live state from ZFS and the DB.
// Called by the diff engine and the drift detector.
func ReadLiveState(db *sql.DB) (*LiveState, error) {
	state := &LiveState{}
	var err error

	state.Pools, err = readLivePools()
	if err != nil {
		return nil, fmt.Errorf("reading live pools: %w", err)
	}

	state.Datasets, err = readLiveDatasets()
	if err != nil {
		return nil, fmt.Errorf("reading live datasets: %w", err)
	}

	state.Shares, err = readLiveShares(db)
	if err != nil {
		return nil, fmt.Errorf("reading live shares: %w", err)
	}

	state.Stacks, err = readLiveStacks()
	if err != nil {
		return nil, fmt.Errorf("reading live stacks: %w", err)
	}

	state.Users, err = readLiveUsers(db)
	if err != nil {
		return nil, fmt.Errorf("reading live users: %w", err)
	}

	state.Groups, err = readLiveGroups(db)
	if err != nil {
		return nil, fmt.Errorf("reading live groups: %w", err)
	}

	state.Replication, err = readLiveReplication()
	if err != nil {
		return nil, fmt.Errorf("reading live replication: %w", err)
	}

	state.NFS, err = readLiveNFSExports(db)
	if err != nil {
		return nil, fmt.Errorf("reading live NFS: %w", err)
	}
 
	state.System, err = readLiveSystem()
	if err != nil {
		return nil, fmt.Errorf("reading live system: %w", err)
	}

	state.LDAP, _ = readLDAPConfig(db)
	state.ACME, _ = readLiveACME(db)
	state.Certificates, _ = readLiveCertificates(db)
	state.SMART, _ = readLiveSMART(db)

	return state, nil
}

// readLivePools reads pool state via `zpool list` and disk membership via
// `zpool status` to extract /dev/disk/by-id/ paths.
func readLivePools() ([]LivePool, error) {
	// Pool list: name, health, and guid
	out, err := cmdutil.RunZFS("zpool", "list", "-H", "-o", "name,health,guid")
	if err != nil {
		return nil, fmt.Errorf("zpool list: %w", err)
	}

	var pools []LivePool
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pools = append(pools, LivePool{
			Name:   fields[0],
			Health: fields[1],
			GUID:   fields[2],
		})
	}

	// Disk membership: `zpool status` with -P flag for full paths
	statusOut, err := cmdutil.RunZFS("zpool", "status", "-P")
	if err == nil {
		disksByPool := parseZpoolStatusPaths(string(statusOut))
		for i, p := range pools {
			if disks, ok := disksByPool[p.Name]; ok {
				pools[i].Disks = disks
			}
		}
	}

	return pools, nil
}

// parseZpoolStatusPaths extracts per-pool disk paths from `zpool status -P`.
// Only lines with /dev/disk/by-id/ paths are kept - everything else (mirror,
// raidz, spares, cache headers) is ignored.
func parseZpoolStatusPaths(output string) map[string][]string {
	result := make(map[string][]string)
	var currentPool string

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)

		// Pool header: "  pool: tank"
		if strings.HasPrefix(trimmed, "pool:") {
			currentPool = strings.TrimSpace(strings.TrimPrefix(trimmed, "pool:"))
			continue
		}

		// Device line: contains any /dev/ path
		if currentPool != "" && strings.Contains(trimmed, "/dev/") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 1 {
				result[currentPool] = append(result[currentPool], fields[0])
			}
		}
	}
	return result
}

// readLiveDatasets reads dataset properties via `zfs get`.
// Uses a single `zfs get` call for all properties to minimise ZFS I/O.
func readLiveDatasets() ([]LiveDataset, error) {
	// Get names first
	listOut, err := cmdutil.RunZFS("zfs", "list", "-H", "-t", "filesystem", "-o", "name")
	if err != nil {
		return nil, fmt.Errorf("zfs list: %w", err)
	}

	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(listOut)), "\n") {
		if n := strings.TrimSpace(line); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}

	// Fetch all properties in one `zfs get -H -p` call
	// -p = machine-parseable (exact bytes for sizes)
	props := "used,avail,compression,atime,mountpoint,quota,encryption"
	args := append([]string{"get", "-H", "-p", "-o", "name,property,value", props}, names...)
	getOut, err := cmdutil.RunZFS("zfs", args...)
	if err != nil {
		return nil, fmt.Errorf("zfs get: %w", err)
	}

	// Build dataset map from output
	type propsMap map[string]string
	dsProps := make(map[string]propsMap)
	for _, line := range strings.Split(string(getOut), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name, prop, value := fields[0], fields[1], fields[2]
		if _, ok := dsProps[name]; !ok {
			dsProps[name] = make(propsMap)
		}
		dsProps[name][prop] = value
	}

	var datasets []LiveDataset
	for _, name := range names {
		pm := dsProps[name]
		if pm == nil {
			pm = make(propsMap)
		}
		ds := LiveDataset{
			Name:        name,
			Compression: pm["compression"],
			Atime:       pm["atime"],
			Mountpoint:  pm["mountpoint"],
			Quota:       pm["quota"],
			Encrypted:   pm["encryption"] != "" && pm["encryption"] != "off",
		}
		ds.Used, _ = strconv.ParseUint(pm["used"], 10, 64)
		ds.Avail, _ = strconv.ParseUint(pm["avail"], 10, 64)
		datasets = append(datasets, ds)
	}

	return datasets, nil
}

// readLiveShares reads SMB share state from the daemon DB.
func readLiveShares(db *sql.DB) ([]LiveShare, error) {
	rows, err := db.Query(`
		SELECT id, name, path, read_only, valid_users, comment, guest_ok, enabled
		FROM smb_shares ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("query smb_shares: %w", err)
	}
	defer rows.Close()

	var shares []LiveShare
	for rows.Next() {
		var s LiveShare
		var roInt, gokInt, enabledInt int
		if err := rows.Scan(&s.ID, &s.Name, &s.Path, &roInt, &s.ValidUsers, &s.Comment, &gokInt, &enabledInt); err != nil {
			continue
		}
		s.ReadOnly = roInt == 1
		s.GuestOK = gokInt == 1
		s.Enabled = enabledInt == 1
		shares = append(shares, s)
	}
	return shares, nil
}
 
func readLiveNFSExports(db *sql.DB) ([]LiveNFSExport, error) {
	rows, err := db.Query(`SELECT id, path, clients, options, enabled FROM nfs_exports ORDER BY id`)
	if err != nil {
		// Table might not exist yet if NFS was never used
		return nil, nil
	}
	defer rows.Close()
 
	var exports []LiveNFSExport
	for rows.Next() {
		var e LiveNFSExport
		var enabledInt int
		if err := rows.Scan(&e.ID, &e.Path, &e.Clients, &e.Options, &enabledInt); err != nil {
			continue
		}
		e.Enabled = enabledInt == 1
		exports = append(exports, e)
	}
	return exports, nil
}

func readLiveUsers(db *sql.DB) ([]LiveUser, error) {
	rows, err := db.Query(`SELECT id, username, password_hash, COALESCE(email,''), COALESCE(role,'user'), active FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []LiveUser
	for rows.Next() {
		var u LiveUser
		var active int
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Email, &u.Role, &active); err != nil {
			return nil, err
		}
		u.Active = active == 1
		users = append(users, u)
	}
	return users, nil
}

func readLiveGroups(db *sql.DB) ([]LiveGroup, error) {
	rows, err := db.Query(`SELECT id, name, COALESCE(description,''), COALESCE(gid,0) FROM groups`)
	if err != nil {
		// Table might not exist yet if zero groups were ever created
		return nil, nil
	}
	defer rows.Close()

	var groups []LiveGroup
	for rows.Next() {
		var g LiveGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.GID); err != nil {
			return nil, err
		}

		// Get members
		mRows, err := db.Query(`SELECT username FROM group_members WHERE group_name = $1`, g.Name)
		if err == nil {
			for mRows.Next() {
				var username string
				mRows.Scan(&username)
				g.Members = append(g.Members, username)
			}
			mRows.Close()
		}

		groups = append(groups, g)
	}
	return groups, nil
}

func readLiveReplication() ([]LiveReplication, error) {
	// Usually in /etc/dplaneos/config or /var/lib/dplaneos/config
	// We'll check common paths or use a default.
	// For this convergence, we assume it's in the same dir as the daemon's working config.
	path := "/etc/dplaneos/replication-schedules.json"
	if _, err := os.Stat("/var/lib/dplaneos/config/replication-schedules.json"); err == nil {
		path = "/var/lib/dplaneos/config/replication-schedules.json"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var raw []map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	var repls []LiveReplication
	for _, m := range raw {
		r := LiveReplication{
			Name:              fmt.Sprint(m["name"]),
			SourceDataset:     fmt.Sprint(m["source_dataset"]),
			RemoteHost:        fmt.Sprint(m["remote_host"]),
			RemoteUser:        fmt.Sprint(m["remote_user"]),
			RemotePort:        int(m["remote_port"].(float64)),
			RemotePool:        fmt.Sprint(m["remote_pool"]),
			SSHKeyPath:        fmt.Sprint(m["ssh_key_path"]),
			Interval:          fmt.Sprint(m["interval"]),
			TriggerOnSnapshot: m["trigger_on_snapshot"] == true,
			Compress:          m["compress"] == true,
			RateLimitMB:       int(m["rate_limit_mb"].(float64)),
			Enabled:           m["enabled"] == true,
		}
		repls = append(repls, r)
	}
	return repls, nil
}
 
 

func readLiveSystem() (*nixwriter.DPlaneState, error) {
	w := nixwriter.DefaultWriter()
	if err := w.LoadFromDisk(); err != nil {
		return nil, err
	}
	s := w.State()
	return &s, nil
}

// HasActiveSMBConnections returns true if smbstatus reports any active connections
// to the named share. Uses cmdutil.RunFast - if smbstatus is unavailable or
// returns no output, conservatively returns false (do not block).
//
// This is the live-connection probe used by the BLOCKED safety check.
func HasActiveSMBConnections(shareName string) bool {
	// smbstatus -S outputs one line per share with connected clients.
	// Format: "sharename  pid  machine  connected-at"
	out, err := cmdutil.RunFast("smbstatus", "-S", "-n")
	if err != nil {
		// smbstatus not available or Samba not running - cannot confirm connections.
		// Conservative: return false (do not incorrectly block).
		return false
	}

	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 1 && fields[0] == shareName {
			return true
		}
	}
	return false
}

// DatasetUsedBytes returns the `used` property of a dataset in bytes.
// Returns 0 if the dataset does not exist or the property cannot be read.
func DatasetUsedBytes(name string) uint64 {
	out, err := cmdutil.RunZFS("zfs", "get", "-H", "-p", "-o", "value", "used", name)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	return n
}

func readLDAPConfig(db *sql.DB) (*DesiredLDAP, error) {
	row := db.QueryRow(`SELECT enabled, server, port, use_tls, bind_dn, COALESCE(bind_password,''), base_dn,
		user_filter, user_id_attr, user_name_attr, user_email_attr,
		group_base_dn, group_filter, group_member_attr,
		jit_provisioning, default_role, sync_interval, timeout FROM ldap_config WHERE id=1`)

	var cfg DesiredLDAP
	var enabled, useTLS, jit int
	err := row.Scan(&enabled, &cfg.Server, &cfg.Port, &useTLS, &cfg.BindDN, &cfg.BindPassword, &cfg.BaseDN,
		&cfg.UserFilter, &cfg.UserIDAttr, &cfg.UserNameAttr, &cfg.UserEmailAttr,
		&cfg.GroupBaseDN, &cfg.GroupFilter, &cfg.GroupMemberAttr,
		&jit, &cfg.DefaultRole, &cfg.SyncInterval, &cfg.Timeout)
	if err != nil {
		return nil, err
	}
	cfg.Enabled = enabled == 1
	cfg.UseTLS = useTLS == 1
	cfg.JITProvisioning = jit == 1
	return &cfg, nil
}

func readLiveACME(db *sql.DB) (*DesiredACME, error) {
	row := db.QueryRow(`SELECT email, server, resolver, dns_config, domains, enabled FROM acme_config WHERE id=1`)
	var email, server, resolver, dnsJson, domainsJson string
	var enabled int
	if err := row.Scan(&email, &server, &resolver, &dnsJson, &domainsJson, &enabled); err != nil {
		return nil, nil
	}
	cfg := &DesiredACME{
		Email:    email,
		Server:   server,
		Resolver: resolver,
		Enabled:  enabled == 1,
	}
	json.Unmarshal([]byte(dnsJson), &cfg.DNSConfig)
	json.Unmarshal([]byte(domainsJson), &cfg.Domains)
	return cfg, nil
}

func readLiveCertificates(db *sql.DB) ([]DesiredCertificate, error) {
	rows, err := db.Query(`SELECT name, cert_pem, key_pem FROM certificates ORDER BY name`)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	var certs []DesiredCertificate
	for rows.Next() {
		var c DesiredCertificate
		if err := rows.Scan(&c.Name, &c.Cert, &c.Key); err == nil {
			certs = append(certs, c)
		}
	}
	return certs, nil
}

func readLiveSMART(db *sql.DB) ([]DesiredSMARTTask, error) {
	rows, err := db.Query(`SELECT device, test_type, schedule, enabled FROM smart_schedules ORDER BY device`)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	var tasks []DesiredSMARTTask
	for rows.Next() {
		var t DesiredSMARTTask
		var enabled int
		if err := rows.Scan(&t.Device, &t.Type, &t.Schedule, &enabled); err == nil {
			t.Enabled = enabled == 1
			tasks = append(tasks, t)
		}
	}
	return tasks, nil
}
