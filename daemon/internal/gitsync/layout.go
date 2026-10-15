// Package gitsync is the Git backend of the configuration store (Design 0001
// section 5.8, phase 5; ADR-0007).
//
// A repository holds the configuration as state.yaml files, one per scope:
//
//	cluster/state.yaml          users, groups, directory, certificates, shared settings
//	groups/<group>/state.yaml   pools of a storage group and what lives on them
//	nodes/<node>/state.yaml     this node's settings and its pools outside any group
//
// or, on a standalone node, everything in one file ("single" layout, the
// format earlier versions wrote).
//
// The repository is treated like one more paired node: per resource, the
// version at the last synchronised commit (base), this node's current
// version (local) and the newest commit's version (remote) are compared.
// Local changes are pushed by the node that owns the scope; remote changes
// become revisions of the configuration store and go through the same merge
// engine as changes from other nodes, with its review screen for conflicts.
// Secrets never go to the repository: secret fields appear as references.
package gitsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"dplaned/internal/configstore"
	"dplaned/internal/gitops"
)

// Layouts.
const (
	LayoutSingle = "single" // one state.yaml for everything (standalone nodes)
	LayoutScoped = "scoped" // one state.yaml per scope
)

// Scope keys. A scope key names one file of the repository and the node that
// writes it (its Git owner).
const (
	ScopeAll     = "all"     // single layout: the whole configuration
	ScopeCluster = "cluster" // cluster scope
)

// GroupScope and NodeScope build the keys of group and node scopes.
func GroupScope(group string) string { return "group:" + group }
func NodeScope(node string) string   { return "node:" + node }

var scopeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ScopePath returns the file of a scope in the repository.
func ScopePath(layout, statePath, scope string) (string, error) {
	if layout == LayoutSingle {
		if scope != ScopeAll {
			return "", fmt.Errorf("the single layout has no scope %q", scope)
		}
		return cleanStatePath(statePath)
	}
	switch {
	case scope == ScopeCluster:
		return "cluster/state.yaml", nil
	case strings.HasPrefix(scope, "group:") && scopeNameRe.MatchString(strings.TrimPrefix(scope, "group:")):
		return "groups/" + strings.TrimPrefix(scope, "group:") + "/state.yaml", nil
	case strings.HasPrefix(scope, "node:") && scopeNameRe.MatchString(strings.TrimPrefix(scope, "node:")):
		return "nodes/" + strings.TrimPrefix(scope, "node:") + "/state.yaml", nil
	}
	return "", fmt.Errorf("invalid scope %q", scope)
}

func cleanStatePath(p string) (string, error) {
	if p == "" {
		p = "state.yaml"
	}
	c := path.Clean(p)
	if path.IsAbs(c) || c == "." || strings.HasPrefix(c, "../") || c == ".." || !strings.HasSuffix(c, ".yaml") && !strings.HasSuffix(c, ".yml") {
		return "", fmt.Errorf("invalid state file path %q (a relative .yaml path inside the repository)", p)
	}
	return c, nil
}

// ScopeOf returns the scope key a resource belongs to. groupOf maps a pool
// to its storage group ("" for none); node is this node's name.
func ScopeOf(layout string, r configstore.Resource, groupOf func(pool string) string, node string) string {
	if layout == LayoutSingle {
		return ScopeAll
	}
	switch r.Scope {
	case configstore.ScopeCluster:
		return ScopeCluster
	case configstore.ScopeGroup:
		if g := groupOf(r.ScopeID); g != "" {
			return GroupScope(g)
		}
	}
	// Node scope, and pools outside any storage group (they stay on their node).
	return NodeScope(node)
}

// ── Categories (which kinds the repository manages) ──────────────────────────

// Category names, as in the GitOps settings.
const (
	CatStorage  = "storage"  // pools, datasets
	CatAccess   = "access"   // SMB shares, NFS exports
	CatApps     = "apps"     // stacks
	CatIdentity = "identity" // users, groups, directory, certificates, ACME
	CatProtect  = "protection"
	CatSystem   = "system" // node and shared system settings
)

var categoryOf = map[string]string{
	configstore.KindPool: CatStorage, configstore.KindDataset: CatStorage,
	configstore.KindShare: CatAccess, configstore.KindNFS: CatAccess, configstore.KindNVMe: CatAccess,
	configstore.KindStack: CatApps,
	configstore.KindUser:  CatIdentity, configstore.KindGroup: CatIdentity, configstore.KindLDAP: CatIdentity,
	configstore.KindACME: CatIdentity, configstore.KindCertificate: CatIdentity,
	configstore.KindReplication: CatProtect, configstore.KindSMART: CatProtect,
	configstore.KindSystem: CatSystem, configstore.KindSettings: CatSystem,
}

// Managed reports whether the repository manages resources of kind.
func Managed(kind string, categories map[string]bool) bool {
	c, ok := categoryOf[kind]
	return ok && categories[c]
}

// ── Secrets as references ─────────────────────────────────────────────────────

// secretField is a payload field that holds a secret's fingerprint.
type secretField struct {
	field string // payload field
	yaml  string // state.yaml field the reference is written to ("" = none)
}

var secretFields = map[string][]secretField{
	configstore.KindUser:        {{"credentials", "password_hash"}},
	configstore.KindLDAP:        {{"bind_password", "bind_password"}},
	configstore.KindCertificate: {{"key", "key"}},
	configstore.KindACME:        {{"dns_config", ""}},
}

func secretRef(kind, key, field string) string {
	return gitops.SecretRefPrefix + kind + "/" + key + "/" + field
}

// ToGit returns the payload as written to the repository: secrets replaced
// by references.
func ToGit(r configstore.Resource) configstore.Resource {
	if r.Payload == nil || secretFields[r.Kind] == nil {
		return r
	}
	p := copyMap(r.Payload)
	for _, f := range secretFields[r.Kind] {
		switch r.Kind {
		case configstore.KindUser:
			// A payload of this store carries a fingerprint; one read from the
			// repository already carries the reference (or a hash written
			// there), which stays as it is.
			delete(p, f.field)
			if fp, _ := r.Payload[f.field].(string); fp != "" {
				p[f.yaml] = secretRef(r.Kind, r.Key, "password")
			}
		case configstore.KindACME:
			if dns, ok := p[f.field].(map[string]any); ok {
				refs := make(map[string]any, len(dns))
				for k := range dns {
					refs[k] = secretRef(r.Kind, r.Key, "dns_config."+k)
				}
				p[f.field] = refs
			}
		default:
			if fp, _ := p[f.field].(string); fp != "" {
				p[f.field] = secretRef(r.Kind, r.Key, f.field)
			}
		}
	}
	r.Payload = p
	return r
}

// FromGit turns a resource read from the repository into a payload of this
// node's store: references are replaced by the secret fingerprints this
// node holds (local; nil if the resource is new here), so the node keeps its
// secrets. A reference to a secret this node does not have becomes empty.
func FromGit(r configstore.Resource, local map[string]any) configstore.Resource {
	if r.Payload == nil || secretFields[r.Kind] == nil {
		return r
	}
	p := copyMap(r.Payload)
	for _, f := range secretFields[r.Kind] {
		switch r.Kind {
		case configstore.KindUser:
			v, _ := p[f.yaml].(string)
			if gitops.IsSecretRef(v) {
				delete(p, f.yaml)
				if fp, ok := local[f.field]; ok {
					p[f.field] = fp
				}
			}
		case configstore.KindACME:
			dns, ok := p[f.field].(map[string]any)
			if !ok {
				continue
			}
			localDNS, _ := local[f.field].(map[string]any)
			for k, v := range dns {
				if s, _ := v.(string); gitops.IsSecretRef(s) {
					dns[k] = localDNS[k]
					if dns[k] == nil {
						dns[k] = ""
					}
				}
			}
		default:
			if v, _ := p[f.field].(string); gitops.IsSecretRef(v) {
				p[f.field] = local[f.field]
				if p[f.field] == nil {
					p[f.field] = ""
				}
			}
		}
	}
	r.Payload = p
	return r
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if inner, ok := v.(map[string]any); ok {
			v = copyMap(inner)
		}
		out[k] = v
	}
	return out
}

// Hash is the identity of a payload for comparisons, with secrets replaced
// by references (the repository never sees them, so they never differ).
// nil (absent or deleted) hashes to "".
func Hash(r configstore.Resource) string {
	if r.Payload == nil {
		return ""
	}
	raw, _ := json.Marshal(canonical(ToGit(r).Payload)) // map keys are sorted
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// canonical drops empty values: state.yaml does not tell an empty string,
// list or map from an absent one, so none may count as a difference.
func canonical(v any) any {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
	case map[string]any:
		out := map[string]any{}
		for k, e := range t {
			if c := canonical(e); c != nil {
				out[k] = c
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(t) == 0 {
			return nil
		}
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = canonical(e)
		}
		return out
	case []string:
		if len(t) == 0 {
			return nil
		}
	}
	return v
}

// ── Rendering and parsing scope files ─────────────────────────────────────────

// Render writes the resources of one scope as a state.yaml file.
func Render(rs []configstore.Resource) (string, error) {
	out := make([]configstore.Resource, 0, len(rs))
	for _, r := range rs {
		if r.Payload != nil {
			out = append(out, ToGit(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	ds, err := configstore.Assemble(out)
	if err != nil {
		return "", err
	}
	return gitops.PrintStateYAML(ds), nil
}

// CheckRoundTrip reads content back and checks that each of want comes out
// as it went in (secrets aside). A difference is a gap in the state.yaml
// format; the file is not written rather than written wrong.
func CheckRoundTrip(content, node string, want []configstore.Resource) error {
	back, err := Parse(content, node)
	if err != nil {
		return fmt.Errorf("the file this node would write does not read back: %w", err)
	}
	byID := map[string]configstore.Resource{}
	for _, r := range back {
		byID[r.ID()] = r
	}
	var lost []string
	for _, w := range want {
		got, ok := byID[w.ID()]
		if !ok || Hash(FromGit(got, w.Payload)) != Hash(w) {
			lost = append(lost, w.ID())
		}
	}
	if len(lost) > 0 {
		sort.Strings(lost)
		return fmt.Errorf("state.yaml cannot hold these exactly as they are; not written: %s", strings.Join(lost, ", "))
	}
	return nil
}

// Parse reads a scope file into resources (with references, as in the file).
func Parse(content, node string) ([]configstore.Resource, error) {
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}
	ds, err := gitops.ParseStateYAML(content)
	if err != nil {
		return nil, err
	}
	return configstore.Extract(ds, node), nil
}
