package config

const (
	// DBDir is the base directory for D-PlaneOS data
	DBDir = "/var/lib/dplaneos"

	// SMbSharesConf is the path to SMB shares configuration
	SMBSharesConf = DBDir + "/smb-shares.conf"

	// StacksDir is the directory for Docker stacks
	StacksDir = DBDir + "/stacks"

	// GitStacksDir is the directory for Git sync stacks
	GitStacksDir = DBDir + "/git-stacks"

	// CustomIconsDir is the directory for custom Docker icons
	CustomIconsDir = DBDir + "/custom_icons"

	// MetricsDir is the directory for Prometheus metrics
	MetricsDir = DBDir + "/metrics"

	// AuditKeyPath is the path to the audit signing key
	AuditKeyPath = DBDir + "/audit.key"

	// StateJSONPath is the path to the NixOS state JSON
	StateJSONPath = DBDir + "/dplane-state.json"

	// GitOpsStatePath is the directory for GitOps state
	GitOpsStateDir = DBDir + "/gitops"
)
