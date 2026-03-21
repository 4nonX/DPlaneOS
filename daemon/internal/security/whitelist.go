package security

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Command represents a whitelisted system command
type Command struct {
	Name        string
	Path        string
	AllowedArgs []string         // Exact arg matches
	ArgPatterns []*regexp.Regexp // Regex patterns for args
	Description string
}

// validDatasetRe is used for dataset name matching in ArgPatterns
var validDatasetRe = regexp.MustCompile(`^[a-zA-Z0-9_\-\./:]+$`)

// CommandWhitelist defines all allowed system operations
var CommandWhitelist = map[string]Command{
	// ZFS Operations
	"zfs_list": {
		Name:        "zfs_list",
		Path:        "zfs",
		AllowedArgs: []string{"list", "-H", "-o", "name,used,avail,refer,mountpoint", "-t", "filesystem"},
		Description: "List ZFS filesystems",
	},
	"zfs_get": {
		Name:        "zfs_get",
		Path:        "zfs",
		AllowedArgs: []string{"get", "-H", "-o", "value"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-\./:]+$`)}, // dataset name
		Description: "Get ZFS property",
	},
	"zpool_list": {
		Name:        "zpool_list",
		Path:        "zpool",
		AllowedArgs: []string{"list", "-H", "-o", "name,size,alloc,free,cap,health"},
		Description: "List ZFS pools with capacity and health",
	},
	"zpool_status": {
		Name:        "zpool_status",
		Path:        "zpool",
		AllowedArgs: []string{"status", "-P"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)}, // pool name
		Description: "Get pool status",
	},
	"zpool_clear": {
		Name:        "zpool_clear",
		Path:        "zpool",
		AllowedArgs: []string{"clear"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)}, // pool name
		Description: "Clear device errors in ZFS pool",
	},
	"zpool_online": {
		Name:        "zpool_online",
		Path:        "zpool",
		AllowedArgs: []string{"online"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`), // pool name
			regexp.MustCompile(`^[a-zA-Z0-9_\-/]+$`), // device path
		},
		Description: "Bring a ZFS device back online",
	},
	"zfs_create": {
		Name:        "zfs_create",
		Path:        "zfs",
		AllowedArgs: []string{"create"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+/[a-zA-Z0-9_\-/]+$`)}, // pool/dataset
		Description: "Create ZFS dataset",
	},
	"zfs_destroy": {
		Name:        "zfs_destroy",
		Path:        "zfs",
		AllowedArgs: []string{"destroy", "-r"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+/[a-zA-Z0-9_\-/]+$`)},
		Description: "Destroy ZFS dataset",
	},
	"zfs_snapshot": {
		Name:        "zfs_snapshot",
		Path:        "zfs",
		AllowedArgs: []string{"snapshot"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+/[a-zA-Z0-9_\-/]+@[a-zA-Z0-9_\-]+$`)},
		Description: "Create ZFS snapshot",
	},
	"zfs_list_snapshots": {
		Name:        "zfs_list_snapshots",
		Path:        "zfs",
		AllowedArgs: []string{"list", "-t", "snapshot", "-r"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+(/[a-zA-Z0-9_\-/]+)?$`)},
		Description: "List ZFS snapshots",
	},
	"zpool_create": {
		Name:        "zpool_create",
		Path:        "zpool",
		AllowedArgs: []string{"create"},
		Description: "Create ZFS pool (requires manual validation of remaining args)",
	},
	"zpool_destroy": {
		Name:        "zpool_destroy",
		Path:        "zpool",
		AllowedArgs: []string{"destroy"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)},
		Description: "Destroy ZFS pool",
	},
	"zpool_scrub": {
		Name:        "zpool_scrub",
		Path:        "zpool",
		AllowedArgs: []string{"scrub"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)},
		Description: "Start ZFS pool scrub",
	},
	"zpool_add_cache": {
		Name:        "zpool_add_cache",
		Path:        "zpool",
		AllowedArgs: []string{"add"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`),  // pool name
			regexp.MustCompile(`^cache$`),            // "cache" keyword
			regexp.MustCompile(`^[a-zA-Z0-9_\-/]+$`), // device path
		},
		Description: "Add L2ARC cache device to pool",
	},
	"zpool_add_log": {
		Name:        "zpool_add_log",
		Path:        "zpool",
		AllowedArgs: []string{"add"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`),  // pool name
			regexp.MustCompile(`^(?:log|mirror)$`),   // "log" or "mirror"
			regexp.MustCompile(`^[a-zA-Z0-9_\-/]+$`), // device path
		},
		Description: "Add ZIL log device to pool",
	},
	"zpool_remove_device": {
		Name:        "zpool_remove_device",
		Path:        "zpool",
		AllowedArgs: []string{"remove"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`),  // pool name
			regexp.MustCompile(`^[a-zA-Z0-9_\-/]+$`), // device path
		},
		Description: "Remove cache or log device from pool",
	},
	"zpool_import_scan": {
		Name:        "zpool_import_scan",
		Path:        "zpool",
		AllowedArgs: []string{"import"},
		Description: "Scan for importable ZFS pools",
	},
	"zpool_import": {
		Name:        "zpool_import",
		Path:        "zpool",
		AllowedArgs: []string{"import"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^(-f|[a-zA-Z0-9_\-]+)$`)},
		Description: "Import existing ZFS pool (with optional -f flag)",
	},
	// zfs_set_property: [zfs set property=value dataset]
	"zfs_set_property": {
		Name:        "zfs_set_property",
		Path:        "zfs",
		AllowedArgs: []string{"set"}, // Will be handled by custom validator
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-z0-9_:]+=[a-zA-Z0-9_\-\.:/]+$`),
			validDatasetRe,
		},
		Description: "Set ZFS property (mountpoint, quota, compression, etc.)",
	},

	// Network Management
	"ip_addr_show": {
		Name:        "ip_addr_show",
		Path:        "ip",
		AllowedArgs: []string{"addr", "show"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9]+$`)},
		Description: "Show network interface addresses",
	},
	"ip_link_up": {
		Name:        "ip_link_up",
		Path:        "ip",
		AllowedArgs: []string{"link", "set", "up"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^(dev|[a-zA-Z0-9]+)$`)},
		Description: "Bring network interface up",
	},
	"ip_link_down": {
		Name:        "ip_link_down",
		Path:        "ip",
		AllowedArgs: []string{"link", "set", "down"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^(dev|[a-zA-Z0-9]+)$`)},
		Description: "Bring network interface down",
	},
	"ip_route_show": {
		Name:        "ip_route_show",
		Path:        "ip",
		AllowedArgs: []string{"route", "show"},
		Description: "Show routing table",
	},
	"ip_route_modify": {
		Name:        "ip_route_modify",
		Path:        "ip",
		Description: "Add or delete routing table entries",
	},
	"network_apply": {
		Name:        "network_apply",
		Path:        "netplan",
		AllowedArgs: []string{"apply"},
		Description: "Apply network configuration (netplan)",
	},

	// ZFS Replication Operations
	"zfs_send": {
		Name:        "zfs_send",
		Path:        "zfs",
		AllowedArgs: []string{"send", "-R"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+(/[a-zA-Z0-9_\-/]+)?@[a-zA-Z0-9_\-]+$`)}, // pool/dataset@snapshot
		Description: "ZFS send for replication",
	},
	"zfs_send_incremental": {
		Name:        "zfs_send_incremental",
		Path:        "zfs",
		AllowedArgs: []string{"send", "-R", "-i"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-zA-Z0-9_\-]+(/[a-zA-Z0-9_\-/]+)?@[a-zA-Z0-9_\-]+$`), // base snapshot
			regexp.MustCompile(`^[a-zA-Z0-9_\-]+(/[a-zA-Z0-9_\-/]+)?@[a-zA-Z0-9_\-]+$`), // new snapshot
		},
		Description: "ZFS incremental send",
	},
	"zfs_receive": {
		Name:        "zfs_receive",
		Path:        "zfs",
		AllowedArgs: []string{"receive", "-F"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+(/[a-zA-Z0-9_\-/]+)?$`)}, // pool/dataset
		Description: "ZFS receive for replication",
	},

	// SMB/Samba Operations
	"systemctl_reload_smbd": {
		Name:        "systemctl_reload_smbd",
		Path:        "systemctl",
		AllowedArgs: []string{"reload", "smbd"},
		Description: "Reload Samba daemon",
	},
	"testparm": {
		Name:        "testparm",
		Path:        "testparm",
		AllowedArgs: []string{"-s"},
		Description: "Test Samba configuration",
	},

	// NFS Operations
	"exportfs_reload": {
		Name:        "exportfs_reload",
		Path:        "exportfs",
		AllowedArgs: []string{"-ra"},
		Description: "Reload NFS exports",
	},
	"exportfs_list": {
		Name:        "exportfs_list",
		Path:        "exportfs",
		AllowedArgs: []string{"-v"},
		Description: "List NFS exports",
	},

	// File Operations
	"mkdir": {
		Name:        "mkdir",
		Path:        "mkdir",
		AllowedArgs: []string{"-p"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^/(mnt|home|tmp|var/lib/dplaneos|tank|data|opt|srv)(/.*)?$`)},
		Description: "Create directory",
	},
	"rm_recursive": {
		Name:        "rm_recursive",
		Path:        "rm",
		AllowedArgs: []string{"-rf"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^/(mnt|home|tmp|var/lib/dplaneos|tank|data|opt|srv)(/.*)?$`)},
		Description: "Remove directory recursively",
	},
	// chown and chmod (path normalization)
	"chown": {
		Name:        "chown",
		Path:        "chown",
		AllowedArgs: []string{"-R"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-z0-9_-]+(:[a-z0-9_-]+)?$`),
			regexp.MustCompile(`^/(mnt|home|tmp|var/lib/dplaneos|tank|data|media|opt|srv)(/.*)?$`),
		},
		Description: "Change file ownership",
	},
	"chmod": {
		Name:        "chmod",
		Path:        "chmod",
		AllowedArgs: []string{"-R"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[0-7]{3,4}$`),
			regexp.MustCompile(`^/(mnt|home|tmp|var/lib/dplaneos|tank|data|media|opt|srv)(/.*)?$`),
		},
		Description: "Change file permissions",
	},

	// Backup Operations
	"rsync": {
		Name: "rsync",
		Path: "rsync",
		AllowedArgs: []string{
			"-avz", "--delete", "--progress", "--partial", "--inplace", "--append", "--compress",
			"-e", "ssh -o StrictHostKeyChecking=accept-new",
		},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^/mnt/.*$`),
			regexp.MustCompile(`^[a-zA-Z0-9\._\-]+@[a-zA-Z0-9\.\-]+:/mnt/.*$`),
		},
		Description: "File synchronization",
	},

	// Power Management Operations
	"lsblk_list": {
		Name:        "lsblk_list",
		Path:        "lsblk",
		AllowedArgs: []string{"-d", "-n", "-o", "NAME,TYPE"},
		Description: "List block devices",
	},
	"hdparm_check": {
		Name:        "hdparm_check",
		Path:        "hdparm",
		AllowedArgs: []string{"-C"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^/dev/[a-z0-9]+$`)},
		Description: "Check disk power state",
	},
	"hdparm_spindown": {
		Name:        "hdparm_spindown",
		Path:        "hdparm",
		AllowedArgs: []string{"-y"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^/dev/[a-z0-9]+$`)},
		Description: "Spin down disk",
	},

	// Docker Operations
	"docker_ps": {
		Name:        "docker_ps",
		Path:        "docker",
		AllowedArgs: []string{"ps", "-a", "--format", "{{json .}}"},
		Description: "List containers",
	},
	"docker_inspect": {
		Name:        "docker_inspect",
		Path:        "docker",
		AllowedArgs: []string{"inspect"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)}, // container ID/name
		Description: "Inspect container",
	},
	"docker_start": {
		Name:        "docker_start",
		Path:        "docker",
		AllowedArgs: []string{"start"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)},
		Description: "Start container",
	},
	"docker_stop": {
		Name:        "docker_stop",
		Path:        "docker",
		AllowedArgs: []string{"stop"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)},
		Description: "Stop container",
	},

	// Network Operations
	"ip_addr": {
		Name:        "ip_addr",
		Path:        "ip",
		AllowedArgs: []string{"-j", "addr", "show"},
		Description: "Show network addresses",
	},
	"ip_route": {
		Name:        "ip_route",
		Path:        "ip",
		AllowedArgs: []string{"-j", "route", "show"},
		Description: "Show routes",
	},

	// System Operations
	"upsc_list": {
		Name:        "upsc_list",
		Path:        "upsc",
		AllowedArgs: []string{"-l"},
		Description: "List UPS devices",
	},
	"upsc_query": {
		Name:        "upsc_query",
		Path:        "upsc",
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-\.]+$`)}, // UPS name
		Description: "Query UPS data",
	},
	"systemctl_status": {
		Name:        "systemctl_status",
		Path:        "systemctl",
		AllowedArgs: []string{"status", "--no-pager"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-\.]+$`)}, // service name
		Description: "Get service status",
	},
	"journalctl": {
		Name:        "journalctl",
		Path:        "journalctl",
		AllowedArgs: []string{"-n", "--no-pager", "-o", "json"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^\d+$`)}, // line count
		Description: "Get system logs",
	},
	// ACL Management (v2.0.0)
	"getfacl": {
		Name:        "getfacl",
		Path:        "getfacl",
		AllowedArgs: []string{"-p"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^/mnt/`)},
		Description: "Get POSIX ACL entries",
	},
	"setfacl": {
		Name:        "setfacl",
		Path:        "setfacl",
		AllowedArgs: []string{"-m", "-x", "-R"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^(u|g|o|m)(:[a-zA-Z0-9_.\-]*)?:[rwx\-]{0,3}$`)},
		Description: "Set POSIX ACL entries",
	},
	"getent": {
		Name:        "getent",
		Path:        "getent",
		AllowedArgs: []string{"passwd", "group"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_.\-]+$`)},
		Description: "Resolve NSS user/group (local + LDAP)",
	},
	// Firewall (v2.0.0)
	"ufw": {
		Name:        "ufw",
		Path:        "ufw",
		Description: "Manage firewall rules",
	},
	// SSL/TLS (v2.0.0)
	"openssl": {
		Name:        "openssl",
		Path:        "openssl",
		Description: "SSL certificate operations",
	},
	"nginx_test": {
		Name:        "nginx_test",
		Path:        "nginx",
		AllowedArgs: []string{"-t", "-s", "reload"},
		Description: "Test and reload nginx config",
	},
	// Power Management (v2.0.0)
	"hdparm_status": {
		Name:        "hdparm_status",
		Path:        "hdparm",
		AllowedArgs: []string{"-C", "-B", "-S", "-y"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^/dev/sd[a-z]+$`), regexp.MustCompile(`^\d+$`)},
		Description: "Disk power management",
	},
	"lsblk_power": {
		Name:        "lsblk_power",
		Path:        "lsblk",
		AllowedArgs: []string{"-dpno", "NAME,SIZE,MODEL,ROTA,TRAN,STATE"},
		Description: "List block device power info",
	},
}

// ValidateCommand checks if a command request is allowed
func ValidateCommand(cmdName string, args []string) error {
	cmd, exists := CommandWhitelist[cmdName]
	if !exists {
		return fmt.Errorf("command not whitelisted: %s", cmdName)
	}

	// Use custom validation if available
	hasCustomValidator := false
	switch cmdName {
	case "zpool_create", "zfs_set_property", "ufw", "ip_route_modify", "openssl", "mkdir", "rm_recursive",
		"zpool_online", "zpool_add_cache", "zpool_add_log", "zpool_remove_device", "hdparm_check", "hdparm_spindown", "hdparm_status":
		hasCustomValidator = true
	}

	// Special handling for complex commands
	switch cmdName {
	case "zpool_create":
		if err := validateZpoolCreate(args); err != nil {
			return err
		}
	case "zfs_set_property":
		if err := validateZfsSetProperty(args); err != nil {
			return err
		}
	case "ufw":
		if err := validateUfw(args); err != nil {
			return err
		}
	case "ip_route_modify":
		if err := validateIpRoute(args); err != nil {
			return err
		}
	case "openssl":
		if err := validateOpenssl(args); err != nil {
			return err
		}
	case "mkdir", "rm_recursive":
		if err := validatePathBasedCommand(cmdName, args); err != nil {
			return err
		}
	case "zpool_online", "zpool_add_cache", "zpool_add_log", "zpool_remove_device", "hdparm_check", "hdparm_spindown", "hdparm_status":
		if err := validateDeviceBasedCommand(cmdName, args); err != nil {
			return err
		}
	}

	// Check if we have exact args or need pattern matching
	if len(cmd.AllowedArgs) > 0 {
		// Exact match mode
		if len(args) < len(cmd.AllowedArgs) {
			return fmt.Errorf("insufficient arguments for %s", cmdName)
		}

		for i, allowedArg := range cmd.AllowedArgs {
			if args[i] != allowedArg {
				return fmt.Errorf("invalid argument at position %d: expected '%s', got '%s'", i, allowedArg, args[i])
			}
		}
		// Mixed mode or pure AllowedArgs
		var remainingArgs []string
		for _, arg := range args {
			isAllowed := false
			for _, allowed := range cmd.AllowedArgs {
				if arg == allowed {
					isAllowed = true
					break
				}
			}
			if !isAllowed {
				remainingArgs = append(remainingArgs, arg)
			}
		}

		if len(cmd.ArgPatterns) > 0 {
			// Validate remaining args against patterns
			if len(remainingArgs) > len(cmd.ArgPatterns) {
				return fmt.Errorf("too many arguments for %s", cmdName)
			}
			for i, arg := range remainingArgs {
				if !cmd.ArgPatterns[i].MatchString(arg) {
					return fmt.Errorf("argument '%s' does not match allowed pattern", arg)
				}
			}
		} else if len(remainingArgs) > 0 && !hasCustomValidator {
			return fmt.Errorf("too many arguments for %s, no patterns defined for extra args", cmdName)
		}
	} else if len(cmd.ArgPatterns) > 0 {
		// Pattern-only mode
		if len(args) > len(cmd.ArgPatterns) {
			return fmt.Errorf("too many arguments for %s", cmdName)
		}
		for i, pat := range cmd.ArgPatterns {
			if i >= len(args) {
				break
			}
			if !pat.MatchString(args[i]) {
				return fmt.Errorf("argument '%s' does not match allowed pattern", args[i])
			}
		}
	} else if len(args) > 0 && !hasCustomValidator {
		// No AllowedArgs or ArgPatterns, but args were provided. This is usually an error.
		return fmt.Errorf("command %s does not accept arguments", cmdName)
	}

	return nil
}

// validateZpoolCreate validates zpool create command arguments
// Format: zpool create [type] poolname device1 [device2 ...]
func validateZpoolCreate(args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("zpool create requires at least: create poolname device")
	}

	if args[0] != "create" {
		return fmt.Errorf("first argument must be 'create'")
	}

	// Valid pool types
	validTypes := map[string]bool{
		"mirror": true, "raidz": true, "raidz1": true,
		"raidz2": true, "raidz3": true,
	}

	// Check if second arg is a type or pool name
	poolNameIdx := 1
	if validTypes[args[1]] {
		poolNameIdx = 2
	}

	if poolNameIdx >= len(args) {
		return fmt.Errorf("missing pool name")
	}

	// Validate pool name
	poolName := args[poolNameIdx]
	if err := ValidatePoolName(poolName); err != nil {
		return err
	}

	// Validate device paths
	for i := poolNameIdx + 1; i < len(args); i++ {
		if err := ValidateDevicePath(args[i]); err != nil {
			return err
		}
	}

	return nil
}

// validateZfsSetProperty enforces strict property allowlist and values
func validateZfsSetProperty(args []string) error {
	// Expect exactly 3 args: "set", "property=value", "dataset"
	if len(args) != 3 || args[0] != "set" {
		return fmt.Errorf("zfs set requires 3 arguments: set property=value dataset")
	}

	propVal := args[1]
	dataset := args[2]

	if err := ValidateDatasetName(dataset); err != nil {
		return err
	}

	parts := strings.SplitN(propVal, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid property=value format: %s", propVal)
	}
	prop := parts[0]
	val := parts[1]

	// Strict property allowlist
	allowedProps := map[string]bool{
		"compression": true, "quota": true, "refquota": true, "mountpoint": true,
		"atime": true, "dedup": true, "recordsize": true, "sync": true,
		"copies": true, "encryption": true, "keylocation": true, "keyformat": true,
	}
	if !allowedProps[prop] {
		return fmt.Errorf("property not allowed: %s", prop)
	}

	// Value validation
	switch prop {
	case "mountpoint":
		if val != "none" && val != "legacy" {
			if err := ValidateMountPoint(val); err != nil {
				return err
			}
		}
	case "quota", "refquota":
		if val != "none" {
			// Match numeric with optional unit: 10G, 500M, 1T
			quoteRe := regexp.MustCompile(`^[0-9]+[KMGTP]?$`)
			if !quoteRe.MatchString(val) {
				return fmt.Errorf("invalid quota value: %s", val)
			}
		}
	default:
		// General pattern for other properties
		valRe := regexp.MustCompile(`^[a-zA-Z0-9_\-\.:/]+$`)
		if !valRe.MatchString(val) {
			return fmt.Errorf("invalid property value: %s", val)
		}
	}

	return nil
}

// validateUfw enforces structured command sequences
func validateUfw(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("ufw requires arguments")
	}
	cmd := args[0]

	switch cmd {
	case "status":
		if len(args) == 1 {
			return nil
		}
		if len(args) == 2 && args[1] == "numbered" {
			return nil
		}
	case "allow", "deny":
		// allow/deny <port>[/proto]
		// allow/deny from <cidr> to any port <port> proto <proto>
		// allow/deny --force from <cidr> to any port <port> proto <proto>
		cmdStr := strings.Join(args, " ")
		if m := regexp.MustCompile(`^(?:--force\s+)?(allow|deny)\s+([0-9]+(?:/(?:tcp|udp))?)$`).FindStringSubmatch(cmdStr); len(m) > 0 {
			return nil
		}
		if m := regexp.MustCompile(`^(?:--force\s+)?(allow|deny)\s+from\s+[0-9a-fA-F\.\/:]+\s+to\s+any\s+port\s+[0-9]+(?:\s+proto\s+(?:tcp|udp))?$`).FindStringSubmatch(cmdStr); len(m) > 0 {
			return nil
		}
	case "delete":
		if len(args) == 2 {
			// delete <n>
			if _, err := strconv.Atoi(args[1]); err == nil {
				return nil
			}
		}
	case "enable", "disable":
		if len(args) == 1 {
			return nil
		}
		if len(args) == 2 && args[0] == "--force" && (args[1] == "enable" || args[1] == "disable") {
			return nil
		}
	}
	return fmt.Errorf("unauthorized ufw command structure: %s", strings.Join(args, " "))
}

// validateIpRoute enforces safe ip route commands
func validateIpRoute(args []string) error {
	// e.g. "route", "add", "10.0.0.0/24", "via", "1.2.3.4", "dev", "eth0"
	if len(args) == 0 || args[0] != "route" {
		return fmt.Errorf("ip route requires 'route' as the first argument")
	}

	cmdStr := strings.Join(args, " ")

	// route add <cidr> via <gateway> dev <iface> [metric <n>]
	if m := regexp.MustCompile(`^route\s+add\s+(?:[0-9\.\/]+|default)\s+via\s+[0-9\.]+\s+dev\s+[a-z0-9\.]+(?:\s+metric\s+[0-9]+)?$`).MatchString(cmdStr); m {
		return nil
	}

	// route del <cidr>
	if m := regexp.MustCompile(`^route\s+del\s+(?:[0-9\.\/]+|default)$`).MatchString(cmdStr); m {
		return nil
	}

	// route show [-j]
	if cmdStr == "route show" || cmdStr == "route show -j" || cmdStr == "-j route show" {
		return nil
	}

	return fmt.Errorf("unauthorized ip route command structure: %s", cmdStr)
}

// validateOpenssl validates -subj pattern and other allowed arguments
func validateOpenssl(args []string) error {
	allowedArgs := map[string]bool{
		"req": true, "x509": true, "-x509": true, "-newkey": true, "rsa:2048": true,
		"-keyout": true, "-out": true, "-days": true, "-nodes": true, "-subj": true,
		"-noout": true, "-subject": true, "-enddate": true, "-issuer": true, "-in": true,
		"-addext": true, "v3_req": true, "extensions": true,
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-subj" {
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for -subj")
			}
			val := args[i+1]
			// Allow commonName (CN) only
			if !regexp.MustCompile(`^/CN=[a-zA-Z0-9\.\-]+$`).MatchString(val) {
				return fmt.Errorf("invalid -subj value: %s (only /CN= is allowed)", val)
			}
			i++ // skip the value
			continue
		}
		if allowedArgs[arg] {
			continue
		}
		// Allow path-like values for -keyout, -out, -in
		if (arg == "-keyout" || arg == "-out" || arg == "-in") && i+1 < len(args) {
			pathArg := args[i+1]
			// Use IsValidPath but allow relative filenames for new files in AllowedBasePaths
			if !IsValidPath(pathArg) && !IsSafeFilename(pathArg) {
				return fmt.Errorf("invalid path for %s: %s", arg, pathArg)
			}
			i++ // skip the path value
			continue
		}
		// If it doesn't start with -, it might be a positional arg or a value we allow
		if !strings.HasPrefix(arg, "-") {
			if IsValidPath(arg) || IsSafeFilename(arg) || regexp.MustCompile(`^[a-zA-Z0-9_\-\.\*]+$`).MatchString(arg) {
				continue
			}
		}
		// Allow numeric values for -days
		if arg == "-days" && i+1 < len(args) {
			if _, err := strconv.Atoi(args[i+1]); err == nil {
				i++ // skip the numeric value
				continue
			}
		}
		// Allow specific extension values for -addext
		if arg == "-addext" && i+1 < len(args) {
			extVal := args[i+1]
			if regexp.MustCompile(`^subjectAltName=DNS:[a-zA-Z0-9\.\-]+(?:,DNS:[a-zA-Z0-9\.\-]+)*$`).MatchString(extVal) {
				i++ // skip the extension value
				continue
			}
		}

		return fmt.Errorf("unauthorized openssl argument: %s", arg)
	}
	return nil
}

func validatePathBasedCommand(cmdName string, args []string) error {
	// mkdir -p /path or rm -rf /path
	if len(args) < 1 {
		return fmt.Errorf("%s requires at least one argument", cmdName)
	}

	for _, arg := range args {
		// Skip flags
		if strings.HasPrefix(arg, "-") {
			continue
		}
		// Must be a valid absolute path under AllowedBasePaths
		if !IsValidPath(arg) {
			return fmt.Errorf("invalid path for %s: %s (traversal or denied base path)", cmdName, arg)
		}
	}
	return nil
}

func validateDeviceBasedCommand(cmdName string, args []string) error {
	// Various commands that take a device path as one of their arguments
	for _, arg := range args {
		// Only validate arguments that look like device paths
		if strings.HasPrefix(arg, "/dev/") {
			if err := ValidateDevicePath(arg); err != nil {
				return err
			}
		}
	}
	return nil
}

// SanitizeOutput removes potentially sensitive information
func SanitizeOutput(output string) string {
	// Remove potential credentials or sensitive paths
	output = regexp.MustCompile(`password=[^\s]+`).ReplaceAllString(output, "password=***")
	output = regexp.MustCompile(`token=[^\s]+`).ReplaceAllString(output, "token=***")
	output = regexp.MustCompile(`key=[^\s]+`).ReplaceAllString(output, "key=***")
	return output
}

// IsValidSessionToken validates the session token format
func IsValidSessionToken(token string) bool {
	// Session token should be alphanumeric and reasonable length
	if len(token) < 20 || len(token) > 100 {
		return false
	}
	matched, _ := regexp.MatchString(`^[a-zA-Z0-9]+$`, token)
	return matched
}

// ValidatePoolName ensures pool names contain only safe characters.
// ZFS pool names: alphanumeric, hyphens, underscores, dots. No spaces, no shell metacharacters.
// This MUST be called before any pool name is passed to exec.Command.
var validPoolName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_\-\.]{0,254}$`)

func ValidatePoolName(name string) error {
	if !validPoolName.MatchString(name) {
		return fmt.Errorf("invalid pool name: %q (must be alphanumeric, start with letter, max 255 chars)", name)
	}
	return nil
}

// ValidateSnapshotName validates a full snapshot identifier (pool/dataset@snapname).
func ValidateSnapshotName(name string) error {
	parts := strings.SplitN(name, "@", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid snapshot name: %q (must contain exactly one @)", name)
	}
	if err := ValidateDatasetName(parts[0]); err != nil {
		return fmt.Errorf("invalid snapshot dataset: %w", err)
	}
	snapPattern := regexp.MustCompile(`^[a-zA-Z0-9_\-\.]+$`)
	if !snapPattern.MatchString(parts[1]) {
		return fmt.Errorf("invalid snapshot suffix: %q", parts[1])
	}
	return nil
}

func ValidateDatasetName(name string) error {
	if len(name) == 0 || len(name) > 255 {
		return fmt.Errorf("invalid dataset name length")
	}

	// Must start with pool name
	parts := strings.Split(name, "/")
	if len(parts) < 1 {
		return fmt.Errorf("invalid dataset name format")
	}

	// Each component must be valid
	validPattern := regexp.MustCompile(`^[a-zA-Z0-9_\-\.]+$`)
	for _, part := range parts {
		if !validPattern.MatchString(part) {
			return fmt.Errorf("invalid characters in dataset name: %s", part)
		}
	}

	return nil
}

// ValidateDevicePath ensures device paths are safe for exec.Command.
// Only allows /dev/sd[a-z][0-9]*, /dev/sr[0-9]*, /dev/nvme[0-9]* patterns.
var validDevicePath = regexp.MustCompile(`^/dev/(sd[a-z][0-9]*|sr[0-9]+|nvme[0-9]+n[0-9]+p?[0-9]*|disk/by-id/[a-zA-Z0-9_\-\.]+?)$`)

func ValidateDevicePath(path string) error {
	if !validDevicePath.MatchString(path) {
		return fmt.Errorf("invalid device path: %q (must be /dev/sdX, /dev/srN, or /dev/nvmeNnNpN)", path)
	}
	return nil
}

// ValidateMountPoint ensures mount points are under safe directories only.
var validMountPoint = regexp.MustCompile(`^/(mnt|media)/[a-zA-Z0-9_\-\.]+(/[a-zA-Z0-9_\-\.]+)*$`)

func ValidateMountPoint(path string) error {
	if !validMountPoint.MatchString(path) {
		return fmt.Errorf("invalid mount point: %q (must be under /mnt/ or /media/)", path)
	}
	return nil
}

// AllowedBasePaths defines directories that are allowed for file operations
var AllowedBasePaths = []string{"/mnt", "/home", "/tmp", "/var/lib/dplaneos", "/tank", "/data", "/opt", "/srv"}

// IsValidPath checks if a path is safe for file operations.
// Returns false if the path contains traversal attempts or is outside allowed directories.
func IsValidPath(path string) bool {
	if path == "" {
		return false
	}

	// Check for null bytes or newlines
	if strings.ContainsAny(path, "\x00\n\r") {
		return false
	}

	// Normalize path using filepath.Clean and ToSlash for consistent forward slashes
	cleanPath := filepath.ToSlash(filepath.Clean(path))
	
	// Ensure it starts with / (on Windows, Clean might return \ or C:\ but we want /mnt style)
	if !strings.HasPrefix(cleanPath, "/") {
		// If it's a Windows-style absolute path like C:/, ignore it or handle it
		// But for D-PlaneOS we expect /mnt/...
		if !regexp.MustCompile(`^[a-zA-Z]:/`).MatchString(cleanPath) {
			cleanPath = "/" + cleanPath
		}
	}

	if strings.HasPrefix(cleanPath, "..") || strings.Contains(cleanPath, "/../") {
		return false
	}

	// Check for path traversal attempts in raw string too (extra safety)
	if strings.Contains(path, "..") || strings.Contains(path, "./") {
		return false
	}

	for _, base := range AllowedBasePaths {
		if strings.HasPrefix(cleanPath, base+"/") || cleanPath == base {
			return true
		}
	}

	return false
}

// IsSafeFilename checks if a filename doesn't contain path traversal or dangerous characters.
func IsSafeFilename(filename string) bool {
	if filename == "" {
		return false
	}

	// Check for null bytes
	if strings.Contains(filename, "\x00") {
		return false
	}

	// Check for path traversal
	if strings.Contains(filename, "..") {
		return false
	}

	// Check for path separators
	if strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
		return false
	}

	return true
}
