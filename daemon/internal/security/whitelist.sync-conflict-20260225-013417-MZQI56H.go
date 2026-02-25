package security

import (
	"fmt"
	"regexp"
	"strings"
)

// Command represents a whitelisted system command
type Command struct {
	Name        string
	Path        string
	AllowedArgs []string        // Exact arg matches
	ArgPatterns []*regexp.Regexp // Regex patterns for args
	Description string
}

// CommandWhitelist defines all allowed system operations
var CommandWhitelist = map[string]Command{
	// ZFS Operations
	"zfs_list": {
		Name:        "zfs_list",
		Path:        "/usr/sbin/zfs",
		AllowedArgs: []string{"list", "-H", "-o", "name,used,avail,refer,mountpoint", "-t", "filesystem"},
		Description: "List ZFS filesystems",
	},
	"zfs_get": {
		Name:        "zfs_get",
		Path:        "/usr/sbin/zfs",
		AllowedArgs: []string{"get", "-H", "-o", "value"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-\./:]+$`)}, // dataset name
		Description: "Get ZFS property",
	},
	"zpool_list": {
		Name:        "zpool_list",
		Path:        "/usr/sbin/zpool",
		AllowedArgs: []string{"list", "-H", "-o", "name,size,alloc,free,health"},
		Description: "List ZFS pools",
	},
	"zpool_status": {
		Name:        "zpool_status",
		Path:        "/usr/sbin/zpool",
		AllowedArgs: []string{"status", "-P"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)}, // pool name
		Description: "Get pool status",
	},
	"zfs_create": {
		Name:        "zfs_create",
		Path:        "/usr/sbin/zfs",
		AllowedArgs: []string{"create"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+/[a-zA-Z0-9_\-/]+$`)}, // pool/dataset
		Description: "Create ZFS dataset",
	},
	"zfs_destroy": {
		Name:        "zfs_destroy",
		Path:        "/usr/sbin/zfs",
		AllowedArgs: []string{"destroy", "-r"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+/[a-zA-Z0-9_\-/]+$`)},
		Description: "Destroy ZFS dataset",
	},
	"zfs_snapshot": {
		Name:        "zfs_snapshot",
		Path:        "/usr/sbin/zfs",
		AllowedArgs: []string{"snapshot"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+/[a-zA-Z0-9_\-/]+@[a-zA-Z0-9_\-]+$`)},
		Description: "Create ZFS snapshot",
	},
	"zfs_list_snapshots": {
		Name:        "zfs_list_snapshots",
		Path:        "/usr/sbin/zfs",
		AllowedArgs: []string{"list", "-t", "snapshot", "-r"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+(/[a-zA-Z0-9_\-/]+)?$`)},
		Description: "List ZFS snapshots",
	},
	"zpool_create": {
		Name:        "zpool_create",
		Path:        "/usr/sbin/zpool",
		AllowedArgs: []string{"create"},
		Description: "Create ZFS pool (requires manual validation of remaining args)",
	},
	"zpool_destroy": {
		Name:        "zpool_destroy",
		Path:        "/usr/sbin/zpool",
		AllowedArgs: []string{"destroy"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)},
		Description: "Destroy ZFS pool",
	},
	"zpool_scrub": {
		Name:        "zpool_scrub",
		Path:        "/usr/sbin/zpool",
		AllowedArgs: []string{"scrub"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)},
		Description: "Start ZFS pool scrub",
	},
	"zpool_add_cache": {
		Name:        "zpool_add_cache",
		Path:        "/usr/sbin/zpool",
		AllowedArgs: []string{"add"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`),     // pool name
			regexp.MustCompile(`^cache$`),                 // "cache" keyword
			regexp.MustCompile(`^[a-zA-Z0-9_\-/]+$`),    // device path
		},
		Description: "Add L2ARC cache device to pool",
	},
	"zpool_add_log": {
		Name:        "zpool_add_log",
		Path:        "/usr/sbin/zpool",
		AllowedArgs: []string{"add"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`),     // pool name
			regexp.MustCompile(`^(?:log|mirror)$`),        // "log" or "mirror"
			regexp.MustCompile(`^[a-zA-Z0-9_\-/]+$`),    // device path
		},
		Description: "Add ZIL log device to pool",
	},
	"zpool_remove_device": {
		Name:        "zpool_remove_device",
		Path:        "/usr/sbin/zpool",
		AllowedArgs: []string{"remove"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`),    // pool name
			regexp.MustCompile(`^[a-zA-Z0-9_\-/]+$`),   // device path
		},
		Description: "Remove cache or log device from pool",
	},
	"zpool_import_scan": {
		Name:        "zpool_import_scan",
		Path:        "/usr/sbin/zpool",
		AllowedArgs: []string{"import"},
		Description: "Scan for importable ZFS pools",
	},
	"zpool_import": {
		Name:        "zpool_import",
		Path:        "/usr/sbin/zpool",
		AllowedArgs: []string{"import"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^(-f|[a-zA-Z0-9_\-]+)$`)},
		Description: "Import existing ZFS pool (with optional -f flag)",
	},
	"zfs_set_property": {
		Name:        "zfs_set_property",
		Path:        "/usr/sbin/zfs",
		AllowedArgs: []string{"set"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-zA-Z0-9_\-\./:]+=[a-zA-Z0-9_\-\.:/]+$`), // property=value (/ allowed for mountpoint=/tank/data)
			regexp.MustCompile(`^[a-zA-Z0-9_\-\./]+$`),                     // dataset name
		},
		Description: "Set ZFS property (mountpoint, quota, compression, etc.)",
	},
	
	// Network Management
	"ip_addr_show": {
		Name:        "ip_addr_show",
		Path:        "/usr/sbin/ip",
		AllowedArgs: []string{"addr", "show"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9]+$`)},
		Description: "Show network interface addresses",
	},
	"ip_link_up": {
		Name:        "ip_link_up",
		Path:        "/usr/sbin/ip",
		AllowedArgs: []string{"link", "set", "up"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^(dev|[a-zA-Z0-9]+)$`)},
		Description: "Bring network interface up",
	},
	"ip_link_down": {
		Name:        "ip_link_down",
		Path:        "/usr/sbin/ip",
		AllowedArgs: []string{"link", "set", "down"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^(dev|[a-zA-Z0-9]+)$`)},
		Description: "Bring network interface down",
	},
	"ip_route_show": {
		Name:        "ip_route_show",
		Path:        "/usr/sbin/ip",
		AllowedArgs: []string{"route", "show"},
		Description: "Show routing table",
	},
	"ip_route_modify": {
		Name:        "ip_route_modify",
		Path:        "/usr/sbin/ip",
		AllowedArgs: []string{"route", "add", "del", "via", "dev", "metric"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^([0-9\.\/]+|default|add|del|via|dev|metric|[a-zA-Z0-9]+)$`)},
		Description: "Add or delete routing table entries",
	},
	"network_apply": {
		Name:        "network_apply",
		Path:        "/usr/sbin/netplan",
		AllowedArgs: []string{"apply"},
		Description: "Apply network configuration (netplan)",
	},
	
	// ZFS Replication Operations
	"zfs_send": {
		Name:        "zfs_send",
		Path:        "/usr/sbin/zfs",
		AllowedArgs: []string{"send", "-R"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+(/[a-zA-Z0-9_\-/]+)?@[a-zA-Z0-9_\-]+$`)}, // pool/dataset@snapshot
		Description: "ZFS send for replication",
	},
	"zfs_send_incremental": {
		Name:        "zfs_send_incremental",
		Path:        "/usr/sbin/zfs",
		AllowedArgs: []string{"send", "-R", "-i"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-zA-Z0-9_\-]+(/[a-zA-Z0-9_\-/]+)?@[a-zA-Z0-9_\-]+$`), // base snapshot
			regexp.MustCompile(`^[a-zA-Z0-9_\-]+(/[a-zA-Z0-9_\-/]+)?@[a-zA-Z0-9_\-]+$`), // new snapshot
		},
		Description: "ZFS incremental send",
	},
	"zfs_receive": {
		Name:        "zfs_receive",
		Path:        "/usr/sbin/zfs",
		AllowedArgs: []string{"receive", "-F"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+(/[a-zA-Z0-9_\-/]+)?$`)}, // pool/dataset
		Description: "ZFS receive for replication",
	},

	// SMB/Samba Operations
	"systemctl_reload_smbd": {
		Name:        "systemctl_reload_smbd",
		Path:        "/usr/bin/systemctl",
		AllowedArgs: []string{"reload", "smbd"},
		Description: "Reload Samba daemon",
	},
	"testparm": {
		Name:        "testparm",
		Path:        "/usr/bin/testparm",
		AllowedArgs: []string{"-s"},
		Description: "Test Samba configuration",
	},

	// NFS Operations  
	"exportfs_reload": {
		Name:        "exportfs_reload",
		Path:        "/usr/sbin/exportfs",
		AllowedArgs: []string{"-ra"},
		Description: "Reload NFS exports",
	},
	"exportfs_list": {
		Name:        "exportfs_list",
		Path:        "/usr/sbin/exportfs",
		AllowedArgs: []string{"-v"},
		Description: "List NFS exports",
	},

	// File Operations
	"mkdir": {
		Name:        "mkdir",
		Path:        "/usr/bin/mkdir",
		AllowedArgs: []string{"-p"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^/[a-zA-Z0-9/_\-\. ]+$`)},
		Description: "Create directory",
	},
	"rm_recursive": {
		Name:        "rm_recursive",
		Path:        "/usr/bin/rm",
		AllowedArgs: []string{"-rf"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^/[a-zA-Z0-9/_\-\. ]+$`)},
		Description: "Remove directory recursively",
	},
	"chown": {
		Name:        "chown",
		Path:        "/usr/bin/chown",
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[a-z_][a-z0-9_-]*(:?[a-z_]?[a-z0-9_-]*)?$`), // user:group
			regexp.MustCompile(`^/[a-zA-Z0-9/_\-\. ]+$`), // path
		},
		Description: "Change file ownership",
	},
	"chmod": {
		Name:        "chmod",
		Path:        "/usr/bin/chmod",
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^[0-7]{3,4}$`), // octal permissions
			regexp.MustCompile(`^/[a-zA-Z0-9/_\-\. ]+$`), // path
		},
		Description: "Change file permissions",
	},

	// Backup Operations
	"rsync": {
		Name:        "rsync",
		Path:        "/usr/bin/rsync",
		AllowedArgs: []string{"-avz", "--progress"},
		ArgPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^/[a-zA-Z0-9/_\-\. ]+$`), // source
			regexp.MustCompile(`^(/[a-zA-Z0-9/_\-\. ]+|[a-z0-9\.\-_]+@[a-z0-9\.\-]+:/[a-zA-Z0-9/_\-\. ]+)$`), // dest (local or remote)
		},
		Description: "File synchronization",
	},

	// Power Management Operations
	"lsblk_list": {
		Name:        "lsblk_list",
		Path:        "/usr/bin/lsblk",
		AllowedArgs: []string{"-d", "-n", "-o", "NAME,TYPE"},
		Description: "List block devices",
	},
	"hdparm_check": {
		Name:        "hdparm_check",
		Path:        "/usr/sbin/hdparm",
		AllowedArgs: []string{"-C"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^/dev/[a-z0-9]+$`)},
		Description: "Check disk power state",
	},
	"hdparm_spindown": {
		Name:        "hdparm_spindown",
		Path:        "/usr/sbin/hdparm",
		AllowedArgs: []string{"-y"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^/dev/[a-z0-9]+$`)},
		Description: "Spin down disk",
	},

	// Docker Operations
	"docker_ps": {
		Name:        "docker_ps",
		Path:        "/usr/bin/docker",
		AllowedArgs: []string{"ps", "-a", "--format", "{{json .}}"},
		Description: "List containers",
	},
	"docker_inspect": {
		Name:        "docker_inspect",
		Path:        "/usr/bin/docker",
		AllowedArgs: []string{"inspect"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)}, // container ID/name
		Description: "Inspect container",
	},
	"docker_start": {
		Name:        "docker_start",
		Path:        "/usr/bin/docker",
		AllowedArgs: []string{"start"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)},
		Description: "Start container",
	},
	"docker_stop": {
		Name:        "docker_stop",
		Path:        "/usr/bin/docker",
		AllowedArgs: []string{"stop"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)},
		Description: "Stop container",
	},

	// Network Operations
	"ip_addr": {
		Name:        "ip_addr",
		Path:        "/usr/sbin/ip",
		AllowedArgs: []string{"-j", "addr", "show"},
		Description: "Show network addresses",
	},
	"ip_route": {
		Name:        "ip_route",
		Path:        "/usr/sbin/ip",
		AllowedArgs: []string{"-j", "route", "show"},
		Description: "Show routes",
	},

	// System Operations
	"upsc_list": {
		Name:        "upsc_list",
		Path:        "/usr/bin/upsc",
		AllowedArgs: []string{"-l"},
		Description: "List UPS devices",
	},
	"upsc_query": {
		Name:        "upsc_query",
		Path:        "/usr/bin/upsc",
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-\.]+$`)}, // UPS name
		Description: "Query UPS data",
	},
	"systemctl_status": {
		Name:        "systemctl_status",
		Path:        "/usr/bin/systemctl",
		AllowedArgs: []string{"status", "--no-pager"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_\-\.]+$`)}, // service name
		Description: "Get service status",
	},
	"journalctl": {
		Name:        "journalctl",
		Path:        "/usr/bin/journalctl",
		AllowedArgs: []string{"-n", "--no-pager", "-o", "json"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^\d+$`)}, // line count
		Description: "Get system logs",
	},
	// ACL Management (v2.0.0)
	"getfacl": {
		Name:        "getfacl",
		Path:        "/usr/bin/getfacl",
		AllowedArgs: []string{"-p"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^/mnt/`)},
		Description: "Get POSIX ACL entries",
	},
	"setfacl": {
		Name:        "setfacl",
		Path:        "/usr/bin/setfacl",
		AllowedArgs: []string{"-m", "-x", "-R"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^(u|g|o|m)(:[a-zA-Z0-9_.\-]*)?:[rwx\-]{0,3}$`)},
		Description: "Set POSIX ACL entries",
	},
	"getent": {
		Name:        "getent",
		Path:        "/usr/bin/getent",
		AllowedArgs: []string{"passwd", "group"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[a-zA-Z0-9_.\-]+$`)},
		Description: "Resolve NSS user/group (local + LDAP)",
	},
	// Firewall (v2.0.0)
	"ufw": {
		Name:        "ufw",
		Path:        "/usr/sbin/ufw",
		AllowedArgs: []string{"status", "numbered", "allow", "deny", "delete", "enable", "disable", "--force", "from", "to", "any", "port", "proto"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^[0-9]+(/tcp|/udp)?$`)},
		Description: "Manage firewall rules",
	},
	// SSL/TLS (v2.0.0)
	"openssl": {
		Name:        "openssl",
		Path:        "/usr/bin/openssl",
		AllowedArgs: []string{"req", "x509", "-x509", "-newkey", "rsa:2048", "-keyout", "-out", "-days", "-nodes", "-subj", "-noout", "-subject", "-enddate", "-issuer", "-in", "-addext"},
		Description: "SSL certificate operations",
	},
	"nginx_test": {
		Name:        "nginx_test",
		Path:        "/usr/sbin/nginx",
		AllowedArgs: []string{"-t", "-s", "reload"},
		Description: "Test and reload nginx config",
	},
	// Power Management (v2.0.0)
	"hdparm_status": {
		Name:        "hdparm_status",
		Path:        "/usr/sbin/hdparm",
		AllowedArgs: []string{"-C", "-B", "-S", "-y"},
		ArgPatterns: []*regexp.Regexp{regexp.MustCompile(`^/dev/sd[a-z]+$`), regexp.MustCompile(`^\d+$`)},
		Description: "Disk power management",
	},
	"lsblk_power": {
		Name:        "lsblk_power",
		Path:        "/usr/bin/lsblk",
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

	// Special handling for complex commands
	switch cmdName {
	case "zpool_create":
		return validateZpoolCreate(args)
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

		// Validate remaining args with patterns if available
		remainingArgs := args[len(cmd.AllowedArgs):]
		if len(remainingArgs) > 0 && len(cmd.ArgPatterns) > 0 {
			for i, arg := range remainingArgs {
				if i >= len(cmd.ArgPatterns) {
					return fmt.Errorf("too many arguments for %s", cmdName)
				}
				if !cmd.ArgPatterns[i].MatchString(arg) {
					return fmt.Errorf("argument '%s' does not match allowed pattern", arg)
				}
			}
		}
	} else if len(cmd.ArgPatterns) > 0 {
		// Pattern-only mode
		if len(args) != len(cmd.ArgPatterns) {
			return fmt.Errorf("wrong number of arguments for %s: expected %d, got %d", cmdName, len(cmd.ArgPatterns), len(args))
		}

		for i, pattern := range cmd.ArgPatterns {
			if !pattern.MatchString(args[i]) {
				return fmt.Errorf("argument '%s' does not match allowed pattern", args[i])
			}
		}
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
	if !regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`).MatchString(poolName) {
		return fmt.Errorf("invalid pool name: %s", poolName)
	}

	// Validate device paths
	devicePattern := regexp.MustCompile(`^(/dev/[a-zA-Z0-9/_\-]+|[a-zA-Z0-9_\-]+)$`)
	for i := poolNameIdx + 1; i < len(args); i++ {
		if !devicePattern.MatchString(args[i]) {
			return fmt.Errorf("invalid device path: %s", args[i])
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

// ValidateDatasetName ensures dataset names are safe
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
var validDevicePath = regexp.MustCompile(`^/dev/(sd[a-z][0-9]*|sr[0-9]+|nvme[0-9]+n[0-9]+p?[0-9]*)$`)

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
