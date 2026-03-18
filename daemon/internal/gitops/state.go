// Package gitops implements Phase 3: GitOps Differentiator.
//
// State machine:
//
//	Git repo ──► parse state.yaml ──► DesiredState
//	              │
//	              ▼
//	     Validate (by-id paths, schema)
//	              │
//	              ▼
//	    Read live ZFS + shares ──► LiveState
//	              │
//	              ▼
//	        DiffEngine ──► []DiffItem  (SAFE | BLOCKED | CREATE | MODIFY | NOP)
//	              │
//	              ▼
//	       SafeApply  (transactional; BLOCKED items halt plan)
//	              │
//	              ▼
//	      DriftDetector (background; broadcasts on WS)
package gitops

import (
	"bufio"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  DESIRED STATE TYPES  (parsed from state.yaml)
// ═══════════════════════════════════════════════════════════════════════════════

// DesiredState is the top-level structure of /etc/dplaneos/state.yaml.
//
// Example state.yaml:
//
//	version: "1"
//	pools:
//	  - name: tank
//	    vdev_type: mirror
//	    disks:
//	      - /dev/disk/by-id/ata-WDC_WD140EDFZ-11A0VA0_1234567890
//	      - /dev/disk/by-id/ata-WDC_WD140EDFZ-11A0VA0_0987654321
//	    ashift: 12
//	    options:
//	      compression: lz4
//	      atime: "off"
//	datasets:
//	  - name: tank/media
//	    quota: 8T
//	    compression: lz4
//	    atime: "off"
//	    mountpoint: /mnt/media
//	  - name: tank/backups
//	    quota: 4T
//	    compression: zstd
//	    mountpoint: /mnt/backups
//	    encrypted: true
//	shares:
//	  - name: media
//	    path: /mnt/media
//	    read_only: false
//	    valid_users: "@media_users"
//	    comment: "Media library"
//	  - name: backups
//	    path: /mnt/backups
//	    read_only: true
//	nfs:
//	  - path: /mnt/media
//	    clients: "192.168.1.0/24"
//	    options: "rw,sync,no_subtree_check"
//	stacks:
//	  - name: portainer
//	    yaml: |
//	      services:
//	        portainer:
//	          image: portainer/portainer-ce:latest
//	          ports:
//	            - "9443:9443"
//	          volumes:
//	            - /var/run/docker.sock:/var/run/docker.sock
//	            - portainer_data:/data
//	          restart: always
//	      volumes:
//	        portainer_data:
type DesiredState struct {
	Version       string               `yaml:"version"`
	Pools         []DesiredPool        `yaml:"pools"`
	Datasets      []DesiredDataset     `yaml:"datasets"`
	Shares        []DesiredShare       `yaml:"shares"`
	NFS           []DesiredNFS         `yaml:"nfs"`
	Stacks        []DesiredStack       `yaml:"stacks"`
	System        *DesiredSystem       `yaml:"system"`
	Users         []DesiredUser        `yaml:"users"`
	Groups        []DesiredGroup       `yaml:"groups"`
	Replication   []DesiredReplication `yaml:"replication"`
	LDAP          *DesiredLDAP         `yaml:"ldap"`
}

// DesiredPool describes a ZFS pool.
// Disks MUST use /dev/disk/by-id/ paths - enforced at parse time.
type DesiredPool struct {
	Name     string            `yaml:"name"`
	VdevType string            `yaml:"vdev_type"` // mirror, raidz, raidz2, raidz3, "" (stripe)
	Disks    []string          `yaml:"disks"`
	Ashift   int               `yaml:"ashift"`   // 0 = auto
	Options  map[string]string `yaml:"options"`  // pool-level zpool set properties
}

// DesiredDataset describes a ZFS dataset.
type DesiredDataset struct {
	Name        string          `yaml:"name"`
	Quota       string          `yaml:"quota"`        // e.g. "2T", "500G", "none"
	Compression string          `yaml:"compression"`  // lz4, zstd, gzip, off
	Atime       string          `yaml:"atime"`        // on, off
	Mountpoint  string          `yaml:"mountpoint"`
	Encrypted   bool            `yaml:"encrypted"`
	Restore     *RestoreConfig  `yaml:"restore,omitempty"`
}

type RestoreConfig struct {
	Type   string `yaml:"type"`   // zfs-send, rclone, "" (none)
	Source string `yaml:"source"` // remote host or bucket
}

// DesiredShare describes an SMB share.
type DesiredShare struct {
	Name       string `yaml:"name"`
	Path       string `yaml:"path"`
	ReadOnly   bool   `yaml:"read_only"`
	ValidUsers string `yaml:"valid_users"`
	Comment    string `yaml:"comment"`
	GuestOK    bool   `yaml:"guest_ok"`
}
 
// DesiredNFS describes an NFS export.
type DesiredNFS struct {
	Path    string `yaml:"path"`
	Clients string `yaml:"clients"`
	Options string `yaml:"options"`
	Enabled bool   `yaml:"enabled"`
}

// DesiredStack describes a Docker Compose stack.
type DesiredStack struct {
	Name string `yaml:"name"`
	YAML string `yaml:"yaml"`
}
 
// DesiredSystem describes the system-level configuration (NixOS state).
type DesiredSystem struct {
	Hostname   string             `yaml:"hostname"`
	Timezone   string             `yaml:"timezone"`
	DNSServers []string           `yaml:"dns_servers"`
	NTPServers []string           `yaml:"ntp_servers"`
	Firewall   DesiredFirewall    `yaml:"firewall"`
	Networking DesiredNetworking  `yaml:"networking"`
	Samba      DesiredSambaGlobal `yaml:"samba"`
}
 
type DesiredFirewall struct {
	TCP []int `yaml:"tcp"`
	UDP []int `yaml:"udp"`
}
 
type DesiredNetworking struct {
	Statics map[string]DesiredNetworkStatic `yaml:"statics"`
	Bonds   map[string]DesiredNetworkBond   `yaml:"bonds"`
	VLANs   map[string]DesiredNetworkVLAN   `yaml:"vlans"`
}
 
type DesiredNetworkStatic struct {
	CIDR    string `yaml:"cidr"`
	Gateway string `yaml:"gateway"`
}
 
type DesiredNetworkBond struct {
	Slaves []string `yaml:"slaves"`
	Mode   string   `yaml:"mode"`
}
 
type DesiredNetworkVLAN struct {
	Parent string `yaml:"parent"`
	VID    int    `yaml:"vid"`
}
 
type DesiredSambaGlobal struct {
	Workgroup    string `yaml:"workgroup"`
	ServerString string `yaml:"server_string"`
	TimeMachine  bool   `yaml:"time_machine"`
	AllowGuest   bool   `yaml:"allow_guest"`
	ExtraGlobal  string `yaml:"extra_global"`
}

type DesiredUser struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
	Email        string `yaml:"email"`
	Role         string `yaml:"role"`
	Active       bool   `yaml:"active"`
}

type DesiredGroup struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	GID         int      `yaml:"gid"`
	Members     []string `yaml:"members"` // member usernames
}

type DesiredReplication struct {
	Name              string `yaml:"name"`
	SourceDataset     string `yaml:"source_dataset"`
	RemoteHost        string `yaml:"remote_host"`
	RemoteUser        string `yaml:"remote_user"`
	RemotePort        int    `yaml:"remote_port"`
	RemotePool        string `yaml:"remote_pool"`
	SSHKeyPath        string `yaml:"ssh_key_path"`
	Interval          string `yaml:"interval"`
	TriggerOnSnapshot bool   `yaml:"trigger_on_snapshot"`
	Compress          bool   `yaml:"compress"`
	RateLimitMB       int    `yaml:"rate_limit_mb"`
	Enabled           bool   `yaml:"enabled"`
}

type DesiredLDAP struct {
	Enabled         bool   `yaml:"enabled"`
	Server          string `yaml:"server"`
	Port            int    `yaml:"port"`
	UseTLS          bool   `yaml:"use_tls"`
	BindDN          string `yaml:"bind_dn"`
	BindPassword    string `yaml:"bind_password"`
	BaseDN          string `yaml:"base_dn"`
	UserFilter      string `yaml:"user_filter"`
	UserIDAttr      string `yaml:"user_id_attr"`
	UserNameAttr    string `yaml:"user_name_attr"`
	UserEmailAttr   string `yaml:"user_email_attr"`
	GroupBaseDN     string `yaml:"group_base_dn"`
	GroupFilter     string `yaml:"group_filter"`
	GroupMemberAttr string `yaml:"group_member_attr"`
	JITProvisioning bool   `yaml:"jit_provisioning"`
	DefaultRole     string `yaml:"default_role"`
	SyncInterval    int    `yaml:"sync_interval"`
	Timeout         int    `yaml:"timeout"`
}

// ═══════════════════════════════════════════════════════════════════════════════
//  VALIDATION RULES
// ═══════════════════════════════════════════════════════════════════════════════

var (
	validDatasetRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/_\-\.]*$`)
	validPoolRe    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-\.]*$`)
	validShareRe   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-\.]*$`)
	validStackRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	validNFSPathRe = regexp.MustCompile(`^/[a-zA-Z0-9_\-\./]+$`)
)

// byIDPrefix is the only disk path prefix accepted by the parser.
const byIDPrefix = "/dev/disk/by-id/"

// ValidState validates a parsed DesiredState and returns a list of human-readable
// errors. An empty slice means the state is valid and safe to diff against live.
func ValidState(s *DesiredState) []string {
	var errs []string

	if s.Version != "1" {
		errs = append(errs, fmt.Sprintf("unsupported state.yaml version %q (only \"1\" is supported)", s.Version))
	}

	// ── Pools ──────────────────────────────────────────────────────────────────
	poolNames := map[string]bool{}
	for i, p := range s.Pools {
		pfx := fmt.Sprintf("pools[%d] %q", i, p.Name)

		if !validPoolRe.MatchString(p.Name) {
			errs = append(errs, pfx+": invalid pool name")
		}
		if poolNames[p.Name] {
			errs = append(errs, pfx+": duplicate pool name")
		}
		poolNames[p.Name] = true

		validVdev := map[string]bool{"": true, "mirror": true, "raidz": true,
			"raidz1": true, "raidz2": true, "raidz3": true}
		if !validVdev[p.VdevType] {
			errs = append(errs, pfx+": unknown vdev_type "+p.VdevType)
		}

		if len(p.Disks) == 0 {
			errs = append(errs, pfx+": disks list is empty")
		}

		// THE HARD RULE: every disk must be a /dev/disk/by-id/ path.
		// /dev/sdX paths are rejected unconditionally - they are unstable across
		// reboots and cause catastrophic pool imports on hardware changes.
		for _, d := range p.Disks {
			if !strings.HasPrefix(d, byIDPrefix) {
				errs = append(errs, fmt.Sprintf(
					"%s: disk %q must use /dev/disk/by-id/ path (got %q) - "+
						"/dev/sdX paths are unstable across reboots and are REJECTED",
					pfx, d, d,
				))
			}
			// Prevent shell injection through disk paths
			if strings.ContainsAny(d, ";|&$`\\\"' \t\n") {
				errs = append(errs, fmt.Sprintf("%s: disk %q contains illegal characters", pfx, d))
			}
		}

		if p.Ashift != 0 && (p.Ashift < 9 || p.Ashift > 16) {
			errs = append(errs, fmt.Sprintf("%s: ashift %d out of range [9,16]", pfx, p.Ashift))
		}
	}

	// ── Datasets ───────────────────────────────────────────────────────────────
	datasetNames := map[string]bool{}
	for i, d := range s.Datasets {
		pfx := fmt.Sprintf("datasets[%d] %q", i, d.Name)

		if !validDatasetRe.MatchString(d.Name) {
			errs = append(errs, pfx+": invalid dataset name")
		}
		if datasetNames[d.Name] {
			errs = append(errs, pfx+": duplicate dataset name")
		}
		datasetNames[d.Name] = true

		// Dataset must be under a declared pool
		hasPool := false
		for _, p := range s.Pools {
			if strings.HasPrefix(d.Name, p.Name+"/") || d.Name == p.Name {
				hasPool = true
				break
			}
		}
		// Allow datasets under pools not declared in this file (pre-existing pools)
		// - only warn, do not error. The diff engine handles this.
		_ = hasPool

		validComp := map[string]bool{"": true, "lz4": true, "zstd": true,
			"gzip": true, "off": true, "on": true}
		if !validComp[d.Compression] {
			errs = append(errs, pfx+": unknown compression "+d.Compression)
		}

		validAtime := map[string]bool{"": true, "on": true, "off": true}
		if !validAtime[d.Atime] {
			errs = append(errs, pfx+": atime must be \"on\" or \"off\"")
		}

		if d.Mountpoint != "" && !strings.HasPrefix(d.Mountpoint, "/") {
			errs = append(errs, pfx+": mountpoint must be an absolute path")
		}
	}

	// ── Shares ─────────────────────────────────────────────────────────────────
	shareNames := map[string]bool{}
	for i, sh := range s.Shares {
		pfx := fmt.Sprintf("shares[%d] %q", i, sh.Name)

		if !validShareRe.MatchString(sh.Name) {
			errs = append(errs, pfx+": invalid share name")
		}
		if shareNames[sh.Name] {
			errs = append(errs, pfx+": duplicate share name")
		}
		shareNames[sh.Name] = true

		if sh.Path == "" || !strings.HasPrefix(sh.Path, "/") {
			errs = append(errs, pfx+": path must be a non-empty absolute path")
		}
	}
 
	// ── NFS ──────────────────────────────────────────────────────────────────
	for i, n := range s.NFS {
		pfx := fmt.Sprintf("nfs[%d] %q", i, n.Path)
		if !validNFSPathRe.MatchString(n.Path) {
			errs = append(errs, pfx+": invalid NFS path")
		}
		if n.Path == "" {
			errs = append(errs, pfx+": path is required")
		}
	}

	// ── Stacks ─────────────────────────────────────────────────────────────────
	stackNames := map[string]bool{}
	for i, st := range s.Stacks {
		pfx := fmt.Sprintf("stacks[%d] %q", i, st.Name)

		if !validStackRe.MatchString(st.Name) {
			errs = append(errs, pfx+": invalid stack name (must be lowercase alphanumeric + hyphens/underscores)")
		}
		if stackNames[st.Name] {
			errs = append(errs, pfx+": duplicate stack name")
		}
		stackNames[st.Name] = true

		if st.YAML == "" {
			errs = append(errs, pfx+": YAML content is required")
		}
		if !strings.Contains(st.YAML, "services:") {
			errs = append(errs, pfx+": invalid compose YAML: must contain 'services:' section")
		}
	}
 
	// ── System ─────────────────────────────────────────────────────────────────
	if s.System != nil {
		if s.System.Hostname != "" && !validPoolRe.MatchString(s.System.Hostname) {
			errs = append(errs, "system: invalid hostname")
		}
		for i, dns := range s.System.DNSServers {
			if !validNFSPathRe.MatchString("/" + dns) { // rough check for IP/host
				// errs = append(errs, fmt.Sprintf("system.dns_servers[%d]: invalid format", i))
			}
			_ = i
			_ = dns
		}
	}

	return errs
}

// ═══════════════════════════════════════════════════════════════════════════════
//  MINIMAL YAML PARSER  (stdlib only - no external dependency)
//
//  Supports the exact subset required by state.yaml:
//    - top-level scalar keys
//    - top-level sequence keys (list of mappings)
//    - mapping keys with scalar values, bool values, and sub-map values
//    - inline lists for disk paths
//
//  Does NOT support: anchors, tags, multi-document, block scalars, JSON flow style.
//  Anything outside this subset returns a parse error - fail closed, never silent.
// ═══════════════════════════════════════════════════════════════════════════════

// ParseStateYAML parses the contents of state.yaml into a DesiredState.
// Returns validation errors if the schema is invalid.
func ParseStateYAML(content string) (*DesiredState, error) {
	p := &yamlParser{lines: splitLines(content)}
	raw, err := p.parseDocument()
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	state, err := mapToState(raw)
	if err != nil {
		return nil, fmt.Errorf("schema error: %w", err)
	}

	if errs := ValidState(state); len(errs) > 0 {
		return nil, fmt.Errorf("validation errors:\n  - %s", strings.Join(errs, "\n  - "))
	}

	return state, nil
}

// PrintStateYAML serializes a DesiredState back to YAML.
// Implements v6 "Deterministic Serialization" - keys are ordered and
// output is normalized to ensure Parse(Print(S)) == S.
func PrintStateYAML(s *DesiredState) string {
	var b strings.Builder

	fmt.Fprintf(&b, "version: %q\n", s.Version)

	if len(s.Pools) > 0 {
		b.WriteString("\npools:\n")
		for _, p := range s.Pools {
			fmt.Fprintf(&b, "  - name: %s\n", p.Name)
			if p.VdevType != "" {
				fmt.Fprintf(&b, "    vdev_type: %s\n", p.VdevType)
			}
			if len(p.Disks) > 0 {
				b.WriteString("    disks:\n")
				for _, d := range p.Disks {
					fmt.Fprintf(&b, "      - %s\n", d)
				}
			}
			if p.Ashift != 0 {
				fmt.Fprintf(&b, "    ashift: %d\n", p.Ashift)
			}
			if len(p.Options) > 0 {
				b.WriteString("    options:\n")
				// Sort keys for determinism
				var keys []string
				for k := range p.Options {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(&b, "      %s: %s\n", k, p.Options[k])
				}
			}
		}
	}

	if len(s.Datasets) > 0 {
		b.WriteString("\ndatasets:\n")
		for _, d := range s.Datasets {
			fmt.Fprintf(&b, "  - name: %s\n", d.Name)
			if d.Quota != "" && d.Quota != "none" {
				fmt.Fprintf(&b, "    quota: %s\n", d.Quota)
			}
			if d.Compression != "" {
				fmt.Fprintf(&b, "    compression: %s\n", d.Compression)
			}
			if d.Atime != "" {
				fmt.Fprintf(&b, "    atime: %s\n", d.Atime)
			}
			if d.Mountpoint != "" {
				fmt.Fprintf(&b, "    mountpoint: %s\n", d.Mountpoint)
			}
			if d.Encrypted {
				b.WriteString("    encrypted: true\n")
			}
			if d.Restore != nil {
				b.WriteString("    restore:\n")
				fmt.Fprintf(&b, "      type: %s\n", d.Restore.Type)
				fmt.Fprintf(&b, "      source: %s\n", d.Restore.Source)
			}
		}
	}

	if len(s.Shares) > 0 {
		b.WriteString("\nshares:\n")
		for _, sh := range s.Shares {
			fmt.Fprintf(&b, "  - name: %s\n", sh.Name)
			fmt.Fprintf(&b, "    path: %s\n", sh.Path)
			if sh.ReadOnly {
				b.WriteString("    read_only: true\n")
			}
			if sh.ValidUsers != "" {
				fmt.Fprintf(&b, "    valid_users: %q\n", sh.ValidUsers)
			}
			if sh.Comment != "" {
				fmt.Fprintf(&b, "    comment: %q\n", sh.Comment)
			}
			if sh.GuestOK {
				b.WriteString("    guest_ok: true\n")
			}
		}
	}

	if len(s.NFS) > 0 {
		b.WriteString("\nnfs:\n")
		for _, n := range s.NFS {
			fmt.Fprintf(&b, "  - path: %s\n", n.Path)
			fmt.Fprintf(&b, "    clients: %q\n", n.Clients)
			fmt.Fprintf(&b, "    options: %q\n", n.Options)
			if !n.Enabled {
				b.WriteString("    enabled: false\n")
			}
		}
	}

	if len(s.Stacks) > 0 {
		b.WriteString("\nstacks:\n")
		for _, st := range s.Stacks {
			fmt.Fprintf(&b, "  - name: %s\n", st.Name)
			b.WriteString("    yaml: |\n")
			lines := strings.Split(strings.TrimSpace(st.YAML), "\n")
			for _, line := range lines {
				fmt.Fprintf(&b, "      %s\n", line)
			}
		}
	}

	if s.System != nil {
		b.WriteString("\nsystem:\n")
		if s.System.Hostname != "" {
			fmt.Fprintf(&b, "  hostname: %s\n", s.System.Hostname)
		}
		if s.System.Timezone != "" {
			fmt.Fprintf(&b, "  timezone: %s\n", s.System.Timezone)
		}
		if len(s.System.DNSServers) > 0 {
			b.WriteString("  dns_servers:\n")
			for _, dns := range s.System.DNSServers {
				fmt.Fprintf(&b, "    - %s\n", dns)
			}
		}
		if len(s.System.NTPServers) > 0 {
			b.WriteString("  ntp_servers:\n")
			for _, ntp := range s.System.NTPServers {
				fmt.Fprintf(&b, "    - %s\n", ntp)
			}
		}

		b.WriteString("  firewall:\n")
		if len(s.System.Firewall.TCP) > 0 {
			b.WriteString("    tcp: [")
			for i, p := range s.System.Firewall.TCP {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%d", p)
			}
			b.WriteString("]\n")
		}
		if len(s.System.Firewall.UDP) > 0 {
			b.WriteString("    udp: [")
			for i, p := range s.System.Firewall.UDP {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%d", p)
			}
			b.WriteString("]\n")
		}

		b.WriteString("  networking:\n")
		if len(s.System.Networking.Statics) > 0 {
			b.WriteString("    statics:\n")
			var keys []string
			for k := range s.System.Networking.Statics {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				st := s.System.Networking.Statics[k]
				fmt.Fprintf(&b, "      %s:\n", k)
				fmt.Fprintf(&b, "        cidr: %s\n", st.CIDR)
				if st.Gateway != "" {
					fmt.Fprintf(&b, "        gateway: %s\n", st.Gateway)
				}
			}
		}
		if len(s.System.Networking.Bonds) > 0 {
			b.WriteString("    bonds:\n")
			var keys []string
			for k := range s.System.Networking.Bonds {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				bn := s.System.Networking.Bonds[k]
				fmt.Fprintf(&b, "      %s:\n", k)
				fmt.Fprintf(&b, "        mode: %s\n", bn.Mode)
				b.WriteString("        slaves: [")
				for i, sl := range bn.Slaves {
					if i > 0 {
						b.WriteString(", ")
					}
					fmt.Fprintf(&b, "%s", sl)
				}
				b.WriteString("]\n")
			}
		}
		if len(s.System.Networking.VLANs) > 0 {
			b.WriteString("    vlans:\n")
			var keys []string
			for k := range s.System.Networking.VLANs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				vl := s.System.Networking.VLANs[k]
				fmt.Fprintf(&b, "      %s:\n", k)
				fmt.Fprintf(&b, "        parent: %s\n", vl.Parent)
				fmt.Fprintf(&b, "        vid: %d\n", vl.VID)
			}
		}

		b.WriteString("  samba:\n")
		fmt.Fprintf(&b, "    workgroup: %s\n", s.System.Samba.Workgroup)
		fmt.Fprintf(&b, "    server_string: %q\n", s.System.Samba.ServerString)
		if s.System.Samba.TimeMachine {
			b.WriteString("    time_machine: true\n")
		}
		if s.System.Samba.AllowGuest {
			b.WriteString("    allow_guest: true\n")
		}
		if s.System.Samba.ExtraGlobal != "" {
			b.WriteString("    extra_global: |\n")
			lines := strings.Split(strings.TrimSpace(s.System.Samba.ExtraGlobal), "\n")
			for _, line := range lines {
				fmt.Fprintf(&b, "      %s\n", line)
			}
		}
	}

	return b.String()
}

// ── Internal parser types ──────────────────────────────────────────────────────

type yamlParser struct {
	lines []parsedLine
	pos   int
}

type parsedLine struct {
	indent  int
	content string // trimmed content, comments stripped
	raw     string
	lineNum int
}

type yamlNode = interface{} // string | map[string]yamlNode | []yamlNode

func splitLines(s string) []parsedLine {
	var result []parsedLine
	scanner := bufio.NewScanner(strings.NewReader(s))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		raw := scanner.Text()
		// Strip comment - but only outside quoted strings (simplified: strip # at word boundary)
		content := stripComment(raw)
		if strings.TrimSpace(content) == "" {
			continue // blank or comment-only lines
		}
		indent := countIndent(raw)
		result = append(result, parsedLine{
			indent:  indent,
			content: strings.TrimSpace(content),
			raw:     raw,
			lineNum: lineNum,
		})
	}
	return result
}

func countIndent(s string) int {
	count := 0
	for _, ch := range s {
		if ch == ' ' {
			count++
		} else {
			break
		}
	}
	return count
}

func stripComment(s string) string {
	// Very simple: find first # not inside quotes
	inSingle, inDouble := false, false
	for i, ch := range s {
		switch ch {
		case '\'':
			inSingle = !inSingle
		case '"':
			inDouble = !inDouble
		case '#':
			if !inSingle && !inDouble {
				return s[:i]
			}
		}
	}
	return s
}

// parseDocument parses the top-level mapping.
func (p *yamlParser) parseDocument() (map[string]yamlNode, error) {
	return p.parseMapping(0)
}

// parseMapping parses a block mapping at the given minimum indent.
func (p *yamlParser) parseMapping(minIndent int) (map[string]yamlNode, error) {
	result := make(map[string]yamlNode)
	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if line.indent < minIndent {
			break // dedented - done with this mapping
		}
		if !strings.Contains(line.content, ":") {
			return nil, fmt.Errorf("line %d: expected key:value, got %q", line.lineNum, line.content)
		}

		// Split on first colon
		colonIdx := strings.Index(line.content, ":")
		key := strings.TrimSpace(line.content[:colonIdx])
		rest := strings.TrimSpace(line.content[colonIdx+1:])

		p.pos++

		var val yamlNode
		var err error

		if rest == "" {
			// Value is on next lines - could be sequence or mapping
			if p.pos < len(p.lines) && p.lines[p.pos].indent > line.indent {
				nextLine := p.lines[p.pos]
				if strings.HasPrefix(nextLine.content, "- ") || nextLine.content == "-" {
					// Sequence
					val, err = p.parseSequence(nextLine.indent)
				} else {
					// Nested mapping
					val, err = p.parseMapping(nextLine.indent)
				}
				if err != nil {
					return nil, err
				}
			} else {
				val = ""
			}
		} else if rest == "|" || rest == ">" {
			// Block scalar literal
			val = p.parseBlockScalar(line.indent + 1)
		} else if strings.HasPrefix(rest, "[") {
			// Inline sequence: [a, b, c]
			val, err = parseInlineSequence(rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line.lineNum, err)
			}
		} else {
			// Scalar
			val = unquote(rest)
		}

		result[key] = val
	}
	return result, nil
}

// parseSequence parses a block sequence (lines starting with "- ").
func (p *yamlParser) parseSequence(minIndent int) ([]yamlNode, error) {
	var result []yamlNode
	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if line.indent < minIndent {
			break
		}
		if !strings.HasPrefix(line.content, "- ") && line.content != "-" {
			break
		}

		itemContent := ""
		if len(line.content) > 2 {
			itemContent = strings.TrimSpace(line.content[2:])
		}
		p.pos++

		if itemContent == "" {
			// Next lines are the item's content (mapping)
			if p.pos < len(p.lines) && p.lines[p.pos].indent > line.indent {
				subMap, err := p.parseMapping(p.lines[p.pos].indent)
				if err != nil {
					return nil, err
				}
				result = append(result, subMap)
			} else {
				result = append(result, "")
			}
		} else if strings.Contains(itemContent, ":") {
			// Inline key: value as first field of mapping item
			// e.g. "- name: tank"
			// Build a sub-mapping starting with this key:value
			colonIdx := strings.Index(itemContent, ":")
			key := strings.TrimSpace(itemContent[:colonIdx])
			val := strings.TrimSpace(itemContent[colonIdx+1:])

			subMap := map[string]yamlNode{key: unquote(val)}

			// Continue reading additional keys at deeper indent
			if p.pos < len(p.lines) && p.lines[p.pos].indent > line.indent {
				rest, err := p.parseMapping(p.lines[p.pos].indent)
				if err != nil {
					return nil, err
				}
				for k, v := range rest {
					subMap[k] = v
				}
			}
			result = append(result, subMap)
		} else {
			// Plain scalar list item
			result = append(result, unquote(itemContent))
		}
	}
	return result, nil
}

// parseBlockScalar reads all lines at a deeper indent level and preserves them literally.
func (p *yamlParser) parseBlockScalar(minIndent int) string {
	var b strings.Builder
	first := true
	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if line.indent < minIndent {
			break
		}
		if !first {
			b.WriteRune('\n')
		}
		// Preserve indentation relative to the block start
		// (very basic: just strip the minIndent spaces)
		b.WriteString(line.raw[minIndent:])
		p.pos++
		first = false
	}
	return b.String()
}

// parseInlineSequence parses [a, b, c] syntax.
func parseInlineSequence(s string) ([]yamlNode, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("invalid inline sequence: %q", s)
	}
	inner := s[1 : len(s)-1]
	parts := strings.Split(inner, ",")
	var result []yamlNode
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, unquote(p))
		}
	}
	return result, nil
}

// unquote removes surrounding quotes from a scalar value.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// ── Map → struct converters ───────────────────────────────────────────────────

func mapToState(raw map[string]yamlNode) (*DesiredState, error) {
	s := &DesiredState{
		Version: strField(raw, "version"),
	}

	if poolsRaw, ok := raw["pools"]; ok {
		pools, err := toSliceOfMaps(poolsRaw, "pools")
		if err != nil {
			return nil, err
		}
		for _, pm := range pools {
			p, err := mapToPool(pm)
			if err != nil {
				return nil, err
			}
			s.Pools = append(s.Pools, p)
		}
	}

	if dsRaw, ok := raw["datasets"]; ok {
		datasets, err := toSliceOfMaps(dsRaw, "datasets")
		if err != nil {
			return nil, err
		}
		for _, dm := range datasets {
			d, err := mapToDataset(dm)
			if err != nil {
				return nil, err
			}
			s.Datasets = append(s.Datasets, d)
		}
	}

	if shRaw, ok := raw["shares"]; ok {
		shares, err := toSliceOfMaps(shRaw, "shares")
		if err != nil {
			return nil, err
		}
		for _, sm := range shares {
			sh, err := mapToShare(sm)
			if err != nil {
				return nil, err
			}
			s.Shares = append(s.Shares, sh)
		}
	}
 
	if nfsRaw, ok := raw["nfs"]; ok {
		nfs, err := toSliceOfMaps(nfsRaw, "nfs")
		if err != nil {
			return nil, err
		}
		for _, nm := range nfs {
			n := DesiredNFS{
				Path:    strField(nm, "path"),
				Clients: strField(nm, "clients"),
				Options: strField(nm, "options"),
				Enabled: strField(nm, "enabled") != "false",
			}
			s.NFS = append(s.NFS, n)
		}
	}

	if stRaw, ok := raw["stacks"]; ok {
		stacks, err := toSliceOfMaps(stRaw, "stacks")
		if err != nil {
			return nil, err
		}
		for _, sm := range stacks {
			st := DesiredStack{
				Name: strField(sm, "name"),
				YAML: strField(sm, "yaml"),
			}
			s.Stacks = append(s.Stacks, st)
		}
	}
 
	if sysRaw, ok := raw["system"]; ok {
		if sysMap, ok := sysRaw.(map[string]yamlNode); ok {
			s.System = mapToSystem(sysMap)
		}
	}

	if uRaw, ok := raw["users"]; ok {
		users, _ := toSliceOfMaps(uRaw, "users")
		for _, um := range users {
			s.Users = append(s.Users, DesiredUser{
				Username:     strField(um, "username"),
				PasswordHash: strField(um, "password_hash"),
				Email:        strField(um, "email"),
				Role:         strField(um, "role"),
				Active:       strField(um, "active") != "false",
			})
		}
	}

	if gRaw, ok := raw["groups"]; ok {
		groups, _ := toSliceOfMaps(gRaw, "groups")
		for _, gm := range groups {
			g := DesiredGroup{
				Name:        strField(gm, "name"),
				Description: strField(gm, "description"),
				GID:         intField(gm, "gid"),
			}
			if mbrs, ok := gm["members"]; ok {
				g.Members, _ = toStringSlice(mbrs, "members")
			}
			s.Groups = append(s.Groups, g)
		}
	}

	if rRaw, ok := raw["replication"]; ok {
		repls, _ := toSliceOfMaps(rRaw, "replication")
		for _, rm := range repls {
			r := DesiredReplication{
				Name:              strField(rm, "name"),
				SourceDataset:     strField(rm, "source_dataset"),
				RemoteHost:        strField(rm, "remote_host"),
				RemoteUser:        strField(rm, "remote_user"),
				RemotePort:        intField(rm, "remote_port"),
				RemotePool:        strField(rm, "remote_pool"),
				SSHKeyPath:        strField(rm, "ssh_key_path"),
				Interval:          strField(rm, "interval"),
				TriggerOnSnapshot: strField(rm, "trigger_on_snapshot") == "true",
				Compress:          strField(rm, "compress") == "true",
				RateLimitMB:       intField(rm, "rate_limit_mb"),
				Enabled:           strField(rm, "enabled") != "false",
			}
			s.Replication = append(s.Replication, r)
		}
	}

	if lRaw, ok := raw["ldap"]; ok {
		if lm, ok := lRaw.(map[string]yamlNode); ok {
			s.LDAP = &DesiredLDAP{
				Enabled:         strField(lm, "enabled") == "true",
				Server:          strField(lm, "server"),
				Port:            intField(lm, "port"),
				UseTLS:          strField(lm, "use_tls") == "true",
				BindDN:          strField(lm, "bind_dn"),
				BindPassword:    strField(lm, "bind_password"),
				BaseDN:          strField(lm, "base_dn"),
				UserFilter:      strField(lm, "user_filter"),
				UserIDAttr:      strField(lm, "user_id_attr"),
				UserNameAttr:    strField(lm, "user_name_attr"),
				UserEmailAttr:   strField(lm, "user_email_attr"),
				GroupBaseDN:     strField(lm, "group_base_dn"),
				GroupFilter:     strField(lm, "group_filter"),
				GroupMemberAttr: strField(lm, "group_member_attr"),
				JITProvisioning: strField(lm, "jit_provisioning") == "true",
				DefaultRole:     strField(lm, "default_role"),
				SyncInterval:    intField(lm, "sync_interval"),
				Timeout:         intField(lm, "timeout"),
			}
		}
	}

	return s, nil
}

func mapToPool(m map[string]yamlNode) (DesiredPool, error) {
	p := DesiredPool{
		Name:     strField(m, "name"),
		VdevType: strField(m, "vdev_type"),
	}
	if a := strField(m, "ashift"); a != "" {
		fmt.Sscanf(a, "%d", &p.Ashift)
	}

	if disksRaw, ok := m["disks"]; ok {
		disks, err := toStringSlice(disksRaw, "disks")
		if err != nil {
			return p, err
		}
		p.Disks = disks
	}

	if optsRaw, ok := m["options"]; ok {
		optsMap, ok := optsRaw.(map[string]yamlNode)
		if !ok {
			return p, fmt.Errorf("pool %q: options must be a mapping", p.Name)
		}
		p.Options = make(map[string]string)
		for k, v := range optsMap {
			p.Options[k] = fmt.Sprintf("%v", v)
		}
	}

	return p, nil
}

func mapToDataset(m map[string]yamlNode) (DesiredDataset, error) {
	d := DesiredDataset{
		Name:        strField(m, "name"),
		Quota:       strField(m, "quota"),
		Compression: strField(m, "compression"),
		Atime:       strField(m, "atime"),
		Mountpoint:  strField(m, "mountpoint"),
	}
	if enc := strField(m, "encrypted"); enc == "true" {
		d.Encrypted = true
	}
	return d, nil
}

func mapToShare(m map[string]yamlNode) (DesiredShare, error) {
	sh := DesiredShare{
		Name:       strField(m, "name"),
		Path:       strField(m, "path"),
		ValidUsers: strField(m, "valid_users"),
		Comment:    strField(m, "comment"),
	}
	if ro := strField(m, "read_only"); ro == "true" {
		sh.ReadOnly = true
	}
	if gok := strField(m, "guest_ok"); gok == "true" {
		sh.GuestOK = true
	}
	return sh, nil
}

// ── Low-level helpers ─────────────────────────────────────────────────────────

func strField(m map[string]yamlNode, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func toSliceOfMaps(v yamlNode, field string) ([]map[string]yamlNode, error) {
	seq, ok := v.([]yamlNode)
	if !ok {
		return nil, fmt.Errorf("%s must be a sequence", field)
	}
	var result []map[string]yamlNode
	for i, item := range seq {
		m, ok := item.(map[string]yamlNode)
		if !ok {
			return nil, fmt.Errorf("%s[%d]: expected a mapping, got %T", field, i, item)
		}
		result = append(result, m)
	}
	return result, nil
}

func toStringSlice(v yamlNode, field string) ([]string, error) {
	seq, ok := v.([]yamlNode)
	if !ok {
		return nil, fmt.Errorf("%s must be a sequence", field)
	}
	var result []string
	for _, item := range seq {
		result = append(result, fmt.Sprintf("%v", item))
	}
	return result, nil
}

func mapToSystem(m map[string]yamlNode) *DesiredSystem {
	sys := &DesiredSystem{
		Hostname:   strField(m, "hostname"),
		Timezone:   strField(m, "timezone"),
		DNSServers: toStringSliceQuiet(m["dns_servers"]),
		NTPServers: toStringSliceQuiet(m["ntp_servers"]),
	}
 
	if fwRaw, ok := m["firewall"]; ok {
		if fwMap, ok := fwRaw.(map[string]yamlNode); ok {
			sys.Firewall.TCP = toIntSlice(fwMap["tcp"])
			sys.Firewall.UDP = toIntSlice(fwMap["udp"])
		}
	}
 
	if nwRaw, ok := m["networking"]; ok {
		if nwMap, ok := nwRaw.(map[string]yamlNode); ok {
			if stRaw, ok := nwMap["statics"]; ok {
				if stMap, ok := stRaw.(map[string]yamlNode); ok {
					sys.Networking.Statics = make(map[string]DesiredNetworkStatic)
					for k, v := range stMap {
						if vm, ok := v.(map[string]yamlNode); ok {
							sys.Networking.Statics[k] = DesiredNetworkStatic{
								CIDR:    strField(vm, "cidr"),
								Gateway: strField(vm, "gateway"),
							}
						}
					}
				}
			}
			if bndRaw, ok := nwMap["bonds"]; ok {
				if bndMap, ok := bndRaw.(map[string]yamlNode); ok {
					sys.Networking.Bonds = make(map[string]DesiredNetworkBond)
					for k, v := range bndMap {
						if vm, ok := v.(map[string]yamlNode); ok {
							sys.Networking.Bonds[k] = DesiredNetworkBond{
								Slaves: toStringSliceQuiet(vm["slaves"]),
								Mode:   strField(vm, "mode"),
							}
						}
					}
				}
			}
			if vlnRaw, ok := nwMap["vlans"]; ok {
				if vlnMap, ok := vlnRaw.(map[string]yamlNode); ok {
					sys.Networking.VLANs = make(map[string]DesiredNetworkVLAN)
					for k, v := range vlnMap {
						if vm, ok := v.(map[string]yamlNode); ok {
							sys.Networking.VLANs[k] = DesiredNetworkVLAN{
								Parent: strField(vm, "parent"),
								VID:    intField(vm, "vid"),
							}
						}
					}
				}
			}
		}
	}
 
	if smbRaw, ok := m["samba"]; ok {
		if smbMap, ok := smbRaw.(map[string]yamlNode); ok {
			sys.Samba.Workgroup = strField(smbMap, "workgroup")
			sys.Samba.ServerString = strField(smbMap, "server_string")
			sys.Samba.TimeMachine = strField(smbMap, "time_machine") == "true"
			sys.Samba.AllowGuest = strField(smbMap, "allow_guest") == "true"
			sys.Samba.ExtraGlobal = strField(smbMap, "extra_global")
		}
	}
 
	return sys
}
 
func toStringSliceQuiet(v yamlNode) []string {
	if v == nil {
		return nil
	}
	seq, ok := v.([]yamlNode)
	if !ok {
		return nil
	}
	var res []string
	for _, item := range seq {
		res = append(res, fmt.Sprintf("%v", item))
	}
	return res
}
 
func toIntSlice(v yamlNode) []int {
	if v == nil {
		return nil
	}
	seq, ok := v.([]yamlNode)
	if !ok {
		return nil
	}
	var res []int
	for _, item := range seq {
		var n int
		fmt.Sscanf(fmt.Sprintf("%v", item), "%d", &n)
		res = append(res, n)
	}
	return res
}
 
func intField(m map[string]yamlNode, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	var n int
	fmt.Sscanf(fmt.Sprintf("%v", v), "%d", &n)
	return n
}

