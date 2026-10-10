// Package configstore keeps the revision history of DPlaneOS configuration
// (Design 0001, Phase 1): every change to a managed resource is recorded as a
// revision, so the GUI can show history and diffs, roll a resource back, and
// export the current configuration as state.yaml.
package configstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"dplaned/internal/gitops"
)

// Scope types (Design 0001, section 5.2). Until storage groups exist (Phase 3)
// the group scope is derived from the pool a resource lives on.
const (
	ScopeNode    = "node"
	ScopeGroup   = "group"
	ScopeCluster = "cluster"
)

// Resource kinds use the GitOps engine's names (gitops.ResourceKind), so a
// revision maps directly to plan items for rollback.
const (
	KindPool        = string(gitops.KindPool)
	KindDataset     = string(gitops.KindDataset)
	KindShare       = string(gitops.KindShare)
	KindNFS         = string(gitops.KindNFS)
	KindStack       = string(gitops.KindStack)
	KindSystem      = string(gitops.KindSystem)
	KindUser        = string(gitops.KindUser)
	KindGroup       = string(gitops.KindGroup)
	KindReplication = string(gitops.KindReplication)
	KindLDAP        = string(gitops.KindLDAP)
	KindACME        = string(gitops.KindACME)
	KindCertificate = string(gitops.KindCertificate)
	KindSMART       = string(gitops.KindSMART)
	KindNVMe        = string(gitops.KindNVMeFabric)

	// KindSettings is the part of state.yaml's system section that should be
	// the same on every node (timezone, DNS, NTP, firewall ports, Samba
	// globals, SSH): cluster scope. KindSystem keeps what belongs to one node
	// (hostname, network interfaces). Both are one "system" item for the
	// GitOps engine.
	KindSettings = "settings"
)

// settingsFields are the state.yaml system fields of KindSettings; the rest
// (hostname, networking) stay in KindSystem.
var settingsFields = map[string]bool{
	"timezone": true, "dns_servers": true, "ntp_servers": true,
	"firewall": true, "samba": true, "ssh": true,
}

// capturedKinds are recorded from the live system. Pools are not: the live
// view has no vdev topology, so pool revisions come only from imported
// state.yaml files, and a pool missing from a capture is never a deletion.
var capturedKinds = map[string]bool{
	KindDataset: true, KindShare: true, KindNFS: true, KindStack: true,
	KindSystem: true, KindSettings: true, KindUser: true, KindGroup: true, KindReplication: true,
	KindLDAP: true, KindACME: true, KindCertificate: true, KindSMART: true,
	KindNVMe: true,
}

// RollbackKinds can be rolled back from the GUI in Phase 1. Kinds that carry
// secrets (LDAP, ACME, certificates) and pools are excluded.
var RollbackKinds = map[string]bool{
	KindDataset: true, KindShare: true, KindNFS: true, KindStack: true,
	KindUser: true, KindGroup: true, KindReplication: true, KindSMART: true,
	KindSettings: true,
}

// Resource is one managed object in its state.yaml form.
type Resource struct {
	Kind    string
	Key     string
	Scope   string
	ScopeID string
	Payload map[string]any // state.yaml field names; nil = deleted
}

// ID identifies a resource across revisions.
func (r Resource) ID() string { return r.Kind + "/" + r.Key }

// poolOf returns the pool a dataset name or a mount path belongs to.
func poolOf(nameOrPath string) string {
	p := strings.TrimPrefix(nameOrPath, "/mnt/")
	p = strings.TrimPrefix(p, "/")
	if i := strings.Index(p, "/"); i >= 0 {
		p = p[:i]
	}
	return p
}

var mntPoolRe = regexp.MustCompile(`/mnt/([A-Za-z][A-Za-z0-9_.:-]*)(/|\s|"|'|$)`)

// stackScope places a Docker stack with the pool its volumes live on: an app
// using pool data must follow that pool on failover (and run only where the
// pool is imported). A stack without pool volumes belongs to its node.
func stackScope(composeYAML, nodeID string) (string, string) {
	pools := map[string]bool{}
	for _, m := range mntPoolRe.FindAllStringSubmatch(composeYAML, -1) {
		pools[m[1]] = true
	}
	if len(pools) == 0 {
		return ScopeNode, nodeID
	}
	names := make([]string, 0, len(pools))
	for p := range pools {
		names = append(names, p)
	}
	sort.Strings(names)
	return ScopeGroup, names[0] // several pools: the first; storage groups (phase 3) will hold all of them
}

// zvolPool returns the pool of an NVMe-oF export's zvol ("tank/vol" or
// "/dev/zvol/tank/vol").
func zvolPool(zvol string) string {
	return poolOf(strings.TrimPrefix(zvol, "/dev/zvol/"))
}

// Extract splits a desired state into resources. nodeID identifies this node
// for node-scoped resources.
func Extract(ds *gitops.DesiredState, nodeID string) []Resource {
	var out []Resource
	add := func(kind, key, scope, scopeID string, v any) {
		out = append(out, Resource{Kind: kind, Key: key, Scope: scope, ScopeID: scopeID, Payload: toMap(v)})
	}
	for _, p := range ds.Pools {
		add(KindPool, p.Name, ScopeGroup, p.Name, p)
	}
	for _, d := range ds.Datasets {
		add(KindDataset, d.Name, ScopeGroup, poolOf(d.Name), d)
	}
	for _, s := range ds.Shares {
		add(KindShare, s.Name, ScopeGroup, poolOf(s.Path), s)
	}
	for _, n := range ds.NFS {
		add(KindNFS, n.Path, ScopeGroup, poolOf(n.Path), n)
	}
	for _, s := range ds.Stacks {
		scope, id := stackScope(s.YAML, nodeID)
		add(KindStack, s.Name, scope, id, s)
	}
	if ds.System != nil {
		node, shared := map[string]any{}, map[string]any{}
		for k, v := range toMap(ds.System) {
			if settingsFields[k] {
				shared[k] = v
			} else {
				node[k] = v
			}
		}
		add(KindSystem, "system", ScopeNode, nodeID, node)
		add(KindSettings, "settings", ScopeCluster, "local", shared)
	}
	for _, u := range ds.Users {
		add(KindUser, u.Username, ScopeCluster, "local", u)
	}
	for _, g := range ds.Groups {
		add(KindGroup, g.Name, ScopeCluster, "local", g)
	}
	for _, r := range ds.Replication {
		add(KindReplication, r.Name, ScopeGroup, poolOf(r.SourceDataset), r)
	}
	if ds.LDAP != nil {
		l := *ds.LDAP
		l.BindPassword = fingerprint(l.BindPassword)
		add(KindLDAP, "ldap", ScopeCluster, "local", l)
	}
	if ds.ACME != nil {
		a := *ds.ACME
		if len(a.DNSConfig) > 0 {
			red := make(map[string]string, len(a.DNSConfig))
			for k, v := range a.DNSConfig {
				red[k] = fingerprint(v)
			}
			a.DNSConfig = red
		}
		add(KindACME, "acme", ScopeCluster, "local", a)
	}
	for _, c := range ds.Certificates {
		c.Key = fingerprint(c.Key)
		add(KindCertificate, c.Name, ScopeCluster, "local", c)
	}
	for _, t := range ds.SMART {
		add(KindSMART, fmt.Sprintf("%s-%s", t.Device, t.Type), ScopeNode, nodeID, t)
	}
	if ds.Fabrics != nil {
		for _, e := range ds.Fabrics.NVMe {
			add(KindNVMe, e.SubsystemNQN, ScopeGroup, zvolPool(e.Zvol), e)
		}
	}
	return out
}

// isFingerprint reports whether a secret field holds a fingerprint.
func isFingerprint(s string) bool { return strings.HasPrefix(s, "sha256:") }

// fingerprint replaces a secret with a stable digest so history and diffs
// show that it changed without ever storing it.
func fingerprint(secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

// Assemble builds a desired state from resources (deleted ones skipped).
// Secret fields hold fingerprints and are cleared; certificates are left out
// because their private keys are not stored. The result is valid state.yaml
// content with ignore_extraneous: true (it may not list everything).
func Assemble(resources []Resource) (*gitops.DesiredState, error) {
	ds := &gitops.DesiredState{Version: "1", IgnoreExtraneous: true}
	var system map[string]any // KindSystem and KindSettings merged
	sorted := append([]Resource(nil), resources...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID() < sorted[j].ID() })
	for _, r := range sorted {
		if r.Payload == nil {
			continue
		}
		var err error
		switch r.Kind {
		case KindPool:
			var v gitops.DesiredPool
			if err = fromMap(r.Payload, &v); err == nil {
				ds.Pools = append(ds.Pools, v)
			}
		case KindDataset:
			var v gitops.DesiredDataset
			if err = fromMap(r.Payload, &v); err == nil {
				ds.Datasets = append(ds.Datasets, v)
			}
		case KindShare:
			var v gitops.DesiredShare
			if err = fromMap(r.Payload, &v); err == nil {
				ds.Shares = append(ds.Shares, v)
			}
		case KindNFS:
			var v gitops.DesiredNFS
			if err = fromMap(r.Payload, &v); err == nil {
				ds.NFS = append(ds.NFS, v)
			}
		case KindStack:
			var v gitops.DesiredStack
			if err = fromMap(r.Payload, &v); err == nil {
				ds.Stacks = append(ds.Stacks, v)
			}
		case KindSystem, KindSettings:
			if system == nil {
				system = map[string]any{}
			}
			for k, v := range r.Payload {
				if settingsFields[k] == (r.Kind == KindSettings) {
					system[k] = v
				}
			}
		case KindUser:
			var v gitops.DesiredUser
			if err = fromMap(r.Payload, &v); err == nil {
				ds.Users = append(ds.Users, v)
			}
		case KindGroup:
			var v gitops.DesiredGroup
			if err = fromMap(r.Payload, &v); err == nil {
				ds.Groups = append(ds.Groups, v)
			}
		case KindReplication:
			var v gitops.DesiredReplication
			if err = fromMap(r.Payload, &v); err == nil {
				ds.Replication = append(ds.Replication, v)
			}
		case KindLDAP:
			var v gitops.DesiredLDAP
			if err = fromMap(r.Payload, &v); err == nil {
				if isFingerprint(v.BindPassword) {
					v.BindPassword = ""
				}
				ds.LDAP = &v
			}
		case KindACME:
			var v gitops.DesiredACME
			if err = fromMap(r.Payload, &v); err == nil {
				for _, val := range v.DNSConfig {
					if isFingerprint(val) {
						v.DNSConfig = nil // fingerprints only: the credentials are not here
						break
					}
				}
				ds.ACME = &v
			}
		case KindCertificate:
			// Private keys are not stored in revisions; a certificate is
			// assembled only when its key was filled in (from the other node).
			var v gitops.DesiredCertificate
			if err = fromMap(r.Payload, &v); err == nil && v.Key != "" && !isFingerprint(v.Key) {
				ds.Certificates = append(ds.Certificates, v)
			}
		case KindSMART:
			var v gitops.DesiredSMARTTask
			if err = fromMap(r.Payload, &v); err == nil {
				ds.SMART = append(ds.SMART, v)
			}
		case KindNVMe:
			var v gitops.DesiredNVMeExport
			if err = fromMap(r.Payload, &v); err == nil {
				if ds.Fabrics == nil {
					ds.Fabrics = &gitops.DesiredFabrics{}
				}
				ds.Fabrics.NVMe = append(ds.Fabrics.NVMe, v)
			}
		default:
			err = fmt.Errorf("unknown kind")
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.ID(), err)
		}
	}
	if system != nil {
		var v gitops.DesiredSystem
		if err := fromMap(system, &v); err != nil {
			return nil, fmt.Errorf("system: %w", err)
		}
		ds.System = &v
	}
	return ds, nil
}

// ── state.yaml field names <-> Go values ──────────────────────────────────────

func yamlName(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("yaml")
	if tag == "-" || !f.IsExported() {
		return "", false
	}
	name := strings.Split(tag, ",")[0]
	if name == "" {
		name = strings.ToLower(f.Name)
	}
	return name, true
}

// toMap converts a value to plain maps/slices keyed by state.yaml names.
func toMap(v any) map[string]any {
	m, _ := toPlain(reflect.ValueOf(v)).(map[string]any)
	return m
}

func toPlain(v reflect.Value) any {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return toPlain(v.Elem())
	case reflect.Struct:
		m := map[string]any{}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if name, ok := yamlName(t.Field(i)); ok {
				m[name] = toPlain(v.Field(i))
			}
		}
		return m
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = toPlain(v.Index(i))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		m := map[string]any{}
		for _, k := range v.MapKeys() {
			m[fmt.Sprint(k.Interface())] = toPlain(v.MapIndex(k))
		}
		return m
	default:
		return v.Interface()
	}
}

// fromMap fills out (a pointer to a struct) from a map produced by toMap or
// decoded from JSON (numbers as float64).
func fromMap(m map[string]any, out any) error {
	return fromPlain(m, reflect.ValueOf(out).Elem())
}

func fromPlain(src any, dst reflect.Value) error {
	if src == nil {
		dst.Set(reflect.Zero(dst.Type()))
		return nil
	}
	switch dst.Kind() {
	case reflect.Pointer:
		p := reflect.New(dst.Type().Elem())
		if err := fromPlain(src, p.Elem()); err != nil {
			return err
		}
		dst.Set(p)
	case reflect.Struct:
		m, ok := src.(map[string]any)
		if !ok {
			return fmt.Errorf("want object for %s, got %T", dst.Type(), src)
		}
		t := dst.Type()
		for i := 0; i < t.NumField(); i++ {
			name, ok := yamlName(t.Field(i))
			if !ok {
				continue
			}
			if v, present := m[name]; present {
				if err := fromPlain(v, dst.Field(i)); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
			}
		}
	case reflect.Slice:
		items, ok := src.([]any)
		if !ok {
			return fmt.Errorf("want list for %s, got %T", dst.Type(), src)
		}
		s := reflect.MakeSlice(dst.Type(), len(items), len(items))
		for i, it := range items {
			if err := fromPlain(it, s.Index(i)); err != nil {
				return err
			}
		}
		dst.Set(s)
	case reflect.Map:
		m, ok := src.(map[string]any)
		if !ok {
			return fmt.Errorf("want object for %s, got %T", dst.Type(), src)
		}
		mv := reflect.MakeMapWithSize(dst.Type(), len(m))
		for k, v := range m {
			ev := reflect.New(dst.Type().Elem()).Elem()
			if err := fromPlain(v, ev); err != nil {
				return err
			}
			mv.SetMapIndex(reflect.ValueOf(k).Convert(dst.Type().Key()), ev)
		}
		dst.Set(mv)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch n := src.(type) {
		case float64:
			dst.SetInt(int64(n))
		case int:
			dst.SetInt(int64(n))
		case int64:
			dst.SetInt(n)
		default:
			return fmt.Errorf("want number for %s, got %T", dst.Type(), src)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		switch n := src.(type) {
		case float64:
			dst.SetUint(uint64(n))
		case uint64:
			dst.SetUint(n)
		case int:
			dst.SetUint(uint64(n))
		default:
			return fmt.Errorf("want number for %s, got %T", dst.Type(), src)
		}
	case reflect.Float32, reflect.Float64:
		n, ok := src.(float64)
		if !ok {
			return fmt.Errorf("want number for %s, got %T", dst.Type(), src)
		}
		dst.SetFloat(n)
	default:
		sv := reflect.ValueOf(src)
		if !sv.Type().ConvertibleTo(dst.Type()) {
			return fmt.Errorf("want %s, got %T", dst.Type(), src)
		}
		dst.Set(sv.Convert(dst.Type()))
	}
	return nil
}
