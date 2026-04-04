package gitops

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ZpoolCreateVdevArgs builds the vdev portion of `zpool create` (everything after pool name).
// It does not include "zpool" or "create" or the pool name.
func ZpoolCreateVdevArgs(top PoolTopology) ([]string, error) {
	if err := ValidatePoolTopology(&top); err != nil {
		return nil, err
	}
	var out []string
	for i := range top.Data {
		toks, err := vdevGroupToArgs(top.Data[i], tierData)
		if err != nil {
			return nil, fmt.Errorf("topology.data[%d]: %w", i, err)
		}
		out = append(out, toks...)
	}
	for i := range top.Special {
		out = append(out, "special")
		toks, err := vdevGroupToArgs(top.Special[i], tierSpecial)
		if err != nil {
			return nil, fmt.Errorf("topology.special[%d]: %w", i, err)
		}
		out = append(out, toks...)
	}
	for i := range top.Log {
		out = append(out, "log")
		toks, err := vdevGroupToArgs(top.Log[i], tierLog)
		if err != nil {
			return nil, fmt.Errorf("topology.log[%d]: %w", i, err)
		}
		out = append(out, toks...)
	}
	for i := range top.Cache {
		out = append(out, "cache")
		toks, err := vdevGroupToArgs(top.Cache[i], tierCache)
		if err != nil {
			return nil, fmt.Errorf("topology.cache[%d]: %w", i, err)
		}
		out = append(out, toks...)
	}
	for i := range top.Spare {
		out = append(out, "spare")
		toks, err := vdevGroupToArgs(top.Spare[i], tierSpare)
		if err != nil {
			return nil, fmt.Errorf("topology.spare[%d]: %w", i, err)
		}
		out = append(out, toks...)
	}
	return out, nil
}

// ZpoolCreateFullArgs returns argv for exec.Command("zpool", args...), including create, -f, pool options, pool name, vdevs.
func ZpoolCreateFullArgs(dp DesiredPool, force bool) ([]string, error) {
	if err := ValidatePoolTopology(&dp.Topology); err != nil {
		return nil, err
	}
	args := []string{"create"}
	if force {
		args = append(args, "-f")
	}
	if dp.Ashift > 0 {
		args = append(args, "-o", fmt.Sprintf("ashift=%d", dp.Ashift))
	}
	var optKeys []string
	for k := range dp.Options {
		optKeys = append(optKeys, k)
	}
	sort.Strings(optKeys)
	for _, k := range optKeys {
		v := strings.TrimSpace(dp.Options[k])
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if strings.ContainsAny(k, " \t\n;|&$`") || strings.ContainsAny(v, "\n\r") {
			return nil, fmt.Errorf("invalid pool option %q=%q", k, v)
		}
		args = append(args, "-O", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, dp.Name)
	vdev, err := ZpoolCreateVdevArgs(dp.Topology)
	if err != nil {
		return nil, err
	}
	args = append(args, vdev...)
	return args, nil
}

type vdevTier int

const (
	tierData vdevTier = iota
	tierSpecial
	tierLog
	tierCache
	tierSpare
)

func vdevGroupToArgs(g VdevGroup, tier vdevTier) ([]string, error) {
	t := strings.ToLower(strings.TrimSpace(g.Type))
	if t == "" && tier == tierSpare {
		t = "stripe"
	}
	if t == "" {
		return nil, fmt.Errorf("vdev type is required")
	}
	if len(g.Disks) == 0 {
		return nil, fmt.Errorf("vdev has no disks")
	}
	for _, d := range g.Disks {
		if err := validateDiskPathStrict(d); err != nil {
			return nil, err
		}
	}

	switch t {
	case "draid":
		spec := strings.TrimSpace(g.DraidSpec)
		if spec == "" {
			return nil, fmt.Errorf("draid vdev requires draid_spec (e.g. draid2:8d:1s)")
		}
		if !draidSpecRe.MatchString(spec) {
			return nil, fmt.Errorf("invalid draid_spec %q", spec)
		}
		out := append([]string{spec}, g.Disks...)
		return out, nil
	case "stripe":
		// Top-level stripe: consecutive leaf vdevs
		return append([]string{}, g.Disks...), nil
	case "mirror":
		if len(g.Disks) < 2 {
			return nil, fmt.Errorf("mirror requires at least 2 disks")
		}
		return append([]string{"mirror"}, g.Disks...), nil
	case "raidz", "raidz1":
		if len(g.Disks) < 2 {
			return nil, fmt.Errorf("raidz requires at least 2 disks")
		}
		return append([]string{"raidz"}, g.Disks...), nil
	case "raidz2":
		if len(g.Disks) < 3 {
			return nil, fmt.Errorf("raidz2 requires at least 3 disks")
		}
		return append([]string{"raidz2"}, g.Disks...), nil
	case "raidz3":
		if len(g.Disks) < 4 {
			return nil, fmt.Errorf("raidz3 requires at least 4 disks")
		}
		return append([]string{"raidz3"}, g.Disks...), nil
	default:
		return nil, fmt.Errorf("unknown vdev type %q", g.Type)
	}
}

var draidSpecRe = regexp.MustCompile(`^draid[123](:\d+d:\d+s)+$`)

func validateDiskPathStrict(d string) error {
	d = strings.TrimSpace(d)
	if !strings.HasPrefix(d, byIDPrefix) && !strings.HasPrefix(d, "/dev/loop") {
		return fmt.Errorf("disk %q must be /dev/disk/by-id/ or /dev/loop", d)
	}
	if strings.ContainsAny(d, ";|&$`'\t\n") {
		return fmt.Errorf("disk %q contains illegal characters", d)
	}
	return nil
}

// ValidatePoolTopology checks structural rules (does not enforce duplicate-disk checks across groups).
func ValidatePoolTopology(top *PoolTopology) error {
	if top == nil {
		return fmt.Errorf("topology is nil")
	}
	if len(top.Data) == 0 {
		return fmt.Errorf("topology.data must contain at least one vdev group")
	}
	for i := range top.Data {
		if _, err := vdevGroupToArgs(top.Data[i], tierData); err != nil {
			return fmt.Errorf("data[%d]: %w", i, err)
		}
	}
	for i := range top.Special {
		if _, err := vdevGroupToArgs(top.Special[i], tierSpecial); err != nil {
			return fmt.Errorf("special[%d]: %w", i, err)
		}
	}
	for i := range top.Log {
		if _, err := vdevGroupToArgs(top.Log[i], tierLog); err != nil {
			return fmt.Errorf("log[%d]: %w", i, err)
		}
	}
	for i := range top.Cache {
		if _, err := vdevGroupToArgs(top.Cache[i], tierCache); err != nil {
			return fmt.Errorf("cache[%d]: %w", i, err)
		}
	}
	for i := range top.Spare {
		if _, err := vdevGroupToArgs(top.Spare[i], tierSpare); err != nil {
			return fmt.Errorf("spare[%d]: %w", i, err)
		}
	}
	return nil
}

// TopologyDiskFingerprint returns sorted unique disk paths for drift comparison (best-effort).
func TopologyDiskFingerprint(top PoolTopology) []string {
	seen := map[string]bool{}
	var collect func([]VdevGroup)
	collect = func(groups []VdevGroup) {
		for _, g := range groups {
			for _, d := range g.Disks {
				d = strings.TrimSpace(d)
				if d != "" && !seen[d] {
					seen[d] = true
				}
			}
		}
	}
	collect(top.Data)
	collect(top.Special)
	collect(top.Log)
	collect(top.Cache)
	collect(top.Spare)
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
