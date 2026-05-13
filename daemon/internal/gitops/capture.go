package gitops

import "dplaned/internal/nixwriter"

// capture.go - converts live system state into DesiredState entries so users
// can snapshot their current running config into state.yaml without hand-editing YAML.
//
// Valid category names:
//   nfs, smb, users, groups, stacks, replication, system

// CategoryCount holds the number of resources in a category.
type CategoryCount struct {
	InGit int  `json:"in_git"` // resources tracked in state.yaml
	Live  int  `json:"live"`   // resources present on the live system
}

// ManagedSummary is the per-category resource count map returned by the summary endpoint.
type ManagedSummary map[string]CategoryCount

// SummarizeManagedResources compares a desired state (from state.yaml) against
// the live system and returns per-category counts for the UI.
func SummarizeManagedResources(desired *DesiredState, live *LiveState) ManagedSummary {
	systemInGit := 0
	if desired != nil && desired.System != nil {
		systemInGit = 1
	}
	var (
		nfsGit, smbGit, usersGit, groupsGit, stacksGit, replGit int
	)
	if desired != nil {
		nfsGit = len(desired.NFS)
		smbGit = len(desired.Shares)
		usersGit = len(desired.Users)
		groupsGit = len(desired.Groups)
		stacksGit = len(desired.Stacks)
		replGit = len(desired.Replication)
	}
	return ManagedSummary{
		"nfs":         {InGit: nfsGit, Live: len(live.NFS)},
		"smb":         {InGit: smbGit, Live: len(live.Shares)},
		"users":       {InGit: usersGit, Live: len(live.Users)},
		"groups":      {InGit: groupsGit, Live: len(live.Groups)},
		"stacks":      {InGit: stacksGit, Live: len(live.Stacks)},
		"replication": {InGit: replGit, Live: len(live.Replication)},
		"system":      {InGit: systemInGit, Live: 1},
	}
}

// ValidCategories is the set of category names accepted by CaptureCategories.
var ValidCategories = map[string]bool{
	"nfs": true, "smb": true, "users": true,
	"groups": true, "stacks": true, "replication": true, "system": true,
}

// CaptureCategories converts live state into a partial DesiredState containing
// only the requested categories. Unspecified categories are left at zero-value.
func CaptureCategories(live *LiveState, categories []string) *DesiredState {
	set := make(map[string]bool, len(categories))
	for _, c := range categories {
		set[c] = true
	}

	out := &DesiredState{}

	if set["nfs"] {
		for _, n := range live.NFS {
			out.NFS = append(out.NFS, DesiredNFS{
				Path: n.Path, Clients: n.Clients, Options: n.Options, Enabled: n.Enabled,
			})
		}
	}

	if set["smb"] {
		for _, s := range live.Shares {
			out.Shares = append(out.Shares, DesiredShare{
				Name: s.Name, Path: s.Path, ReadOnly: s.ReadOnly,
				ValidUsers: s.ValidUsers, Comment: s.Comment, GuestOK: s.GuestOK,
			})
		}
	}

	if set["users"] {
		for _, u := range live.Users {
			out.Users = append(out.Users, DesiredUser{
				Username: u.Username,
				Email:    u.Email,
				Role:     u.Role,
				Active:   u.Active,
				// PasswordHash intentionally omitted: state.yaml should not carry live
				// password hashes. The apply engine skips hash updates when the field is empty.
			})
		}
	}

	if set["groups"] {
		for _, g := range live.Groups {
			out.Groups = append(out.Groups, DesiredGroup{
				Name:        g.Name,
				Description: g.Description,
				GID:         g.GID,
				Members:     append([]string(nil), g.Members...),
			})
		}
	}

	if set["stacks"] {
		for _, s := range live.Stacks {
			if s.YAML == "" {
				continue
			}
			out.Stacks = append(out.Stacks, DesiredStack{Name: s.Name, YAML: s.YAML})
		}
	}

	if set["replication"] {
		for _, r := range live.Replication {
			out.Replication = append(out.Replication, DesiredReplication{
				Name:              r.Name,
				SourceDataset:     r.SourceDataset,
				RemoteHost:        r.RemoteHost,
				RemoteUser:        r.RemoteUser,
				RemotePort:        r.RemotePort,
				RemotePool:        r.RemotePool,
				SSHKeyPath:        r.SSHKeyPath,
				Interval:          r.Interval,
				TriggerOnSnapshot: r.TriggerOnSnapshot,
				Compress:          r.Compress,
				RateLimitMB:       r.RateLimitMB,
				Enabled:           r.Enabled,
			})
		}
	}

	if set["system"] && live.System != nil {
		out.System = liveSystemToDesired(live.System)
	}

	return out
}

// MergeCapture overlays captured category data onto an existing DesiredState,
// replacing only the sections named in categories. Other sections are unchanged.
// Returns a new DesiredState; the originals are not mutated.
func MergeCapture(existing *DesiredState, captured *DesiredState, categories []string) *DesiredState {
	set := make(map[string]bool, len(categories))
	for _, c := range categories {
		set[c] = true
	}

	merged := *existing // shallow copy - sections are replaced below
	if set["nfs"] {
		merged.NFS = captured.NFS
	}
	if set["smb"] {
		merged.Shares = captured.Shares
	}
	if set["users"] {
		merged.Users = captured.Users
	}
	if set["groups"] {
		merged.Groups = captured.Groups
	}
	if set["stacks"] {
		merged.Stacks = captured.Stacks
	}
	if set["replication"] {
		merged.Replication = captured.Replication
	}
	if set["system"] {
		merged.System = captured.System
	}
	return &merged
}

// liveSystemToDesired maps a nixwriter.DPlaneState snapshot to DesiredSystem.
func liveSystemToDesired(s *nixwriter.DPlaneState) *DesiredSystem {
	sys := &DesiredSystem{
		Hostname:   s.Hostname,
		Timezone:   s.Timezone,
		DNSServers: append([]string(nil), s.DNSServers...),
		NTPServers: append([]string(nil), s.NTPServers...),
		Firewall: DesiredFirewall{
			TCP: append([]int(nil), s.FirewallTCP...),
			UDP: append([]int(nil), s.FirewallUDP...),
		},
		Samba: DesiredSambaGlobal{
			Workgroup:    s.SambaWorkgroup,
			ServerString: s.SambaServerString,
			TimeMachine:  s.SambaTimeMachine,
			AllowGuest:   s.SambaAllowGuest,
			ExtraGlobal:  s.SambaExtraGlobal,
		},
		SSH: DesiredSSH{
			Port:            s.SSHPort,
			PasswordAuth:    s.SSHPasswordAuth,
			PermitRootLogin: s.SSHPermitRootLogin,
		},
	}

	if len(s.NetworkStatics) > 0 {
		sys.Networking.Statics = make(map[string]DesiredNetworkStatic, len(s.NetworkStatics))
		for iface, e := range s.NetworkStatics {
			sys.Networking.Statics[iface] = DesiredNetworkStatic{CIDR: e.CIDR, Gateway: e.Gateway}
		}
	}
	if len(s.NetworkBonds) > 0 {
		sys.Networking.Bonds = make(map[string]DesiredNetworkBond, len(s.NetworkBonds))
		for name, e := range s.NetworkBonds {
			sys.Networking.Bonds[name] = DesiredNetworkBond{Slaves: append([]string(nil), e.Slaves...), Mode: e.Mode}
		}
	}
	if len(s.NetworkVLANs) > 0 {
		sys.Networking.VLANs = make(map[string]DesiredNetworkVLAN, len(s.NetworkVLANs))
		for name, e := range s.NetworkVLANs {
			sys.Networking.VLANs[name] = DesiredNetworkVLAN{Parent: e.Parent, VID: e.VID}
		}
	}

	return sys
}
