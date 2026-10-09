package groups

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/config"
	"dplaned/internal/nvmet"
)

// Group resources (Design 0001 phase 3d).
//
// A Docker stack whose volumes are on a group's pools, and an NVMe-oF export
// of one of its zvols, belong to the group, not to a node: they run only on
// the group's owner. The owner publishes their definitions; every candidate
// keeps a copy, and the activation loop on each node starts them where this
// node owns the group and stops them everywhere else. Before a node gives a
// group up (planned move, release), its resources stop first: a pool with
// running containers cannot be exported.

// Resources of a group.
type Resources struct {
	Stacks []StackDef     `json:"stacks"`
	NVMe   []nvmet.Export `json:"nvme"`
}

// StackDef is a compose project.
type StackDef struct {
	Name string `json:"name"`
	YAML string `json:"yaml"`
}

var mntPathRe = regexp.MustCompile(`/mnt/([A-Za-z][A-Za-z0-9_.:-]*)(/|\s|"|'|$)`)
var stackNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// stackPools lists the pools a compose file uses (/mnt/<pool>/...).
func stackPools(yaml string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range mntPathRe.FindAllStringSubmatch(yaml, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// belongs reports whether any of pools is in g.
func (g Group) usesPool(pools ...string) bool {
	for _, p := range pools {
		if g.HasPool(p) {
			return true
		}
	}
	return false
}

func zvolPool(zvol string) string {
	z := strings.TrimPrefix(zvol, "/dev/zvol/")
	if i := strings.Index(z, "/"); i >= 0 {
		return z[:i]
	}
	return z
}

// filterResources picks the stacks and exports of g out of all definitions.
func filterResources(g Group, stacks []StackDef, exports []nvmet.Export) Resources {
	r := Resources{Stacks: []StackDef{}, NVMe: []nvmet.Export{}}
	for _, s := range stacks {
		if g.usesPool(stackPools(s.YAML)...) {
			r.Stacks = append(r.Stacks, s)
		}
	}
	for _, e := range exports {
		if g.HasPool(zvolPool(e.Zvol)) {
			r.NVMe = append(r.NVMe, e)
		}
	}
	return r
}

// appliedExports is the list of exports this node applies: its own list
// without the exports of groups it does not own, plus the exports of groups
// it owns that it only knows from their previous owner.
func appliedExports(local []nvmet.Export, groups []Group, self string, known map[string][]nvmet.Export) []nvmet.Export {
	ownerOf := func(pool string) (Group, bool) {
		for _, g := range groups {
			if g.HasPool(pool) {
				return g, true
			}
		}
		return Group{}, false
	}
	have := map[string]bool{}
	var out []nvmet.Export
	for _, e := range local {
		if g, ok := ownerOf(zvolPool(e.Zvol)); ok && g.Owner != self {
			continue
		}
		have[e.SubsystemNQN] = true
		out = append(out, e)
	}
	for _, g := range groups {
		if g.Owner != self {
			continue
		}
		for _, e := range known[g.Name] {
			if !have[e.SubsystemNQN] {
				have[e.SubsystemNQN] = true
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubsystemNQN < out[j].SubsystemNQN })
	return out
}

// ── Local definitions ─────────────────────────────────────────────────────────

// ResOps are the side effects of group resources; replaced in tests.
type ResOps struct {
	LocalStacks  func() ([]StackDef, error)
	WriteStack   func(s StackDef) error
	StackRunning func(name string) (bool, error)
	StackUp      func(name string) error
	StackDown    func(name string) error
	LocalExports func() ([]nvmet.Export, error)
	SaveExports  func([]nvmet.Export) error
	ApplyExports func([]nvmet.Export) error
}

func stackPaths(name string) (string, string) {
	dir := filepath.Join(config.StacksDir, name)
	return dir, filepath.Join(dir, "docker-compose.yml")
}

func compose(name string, sub ...string) ([]byte, error) {
	dir, file := stackPaths(name)
	args := append([]string{"compose", "--project-directory", dir, "-f", file}, sub...)
	return cmdutil.RunSlow("docker_compose", args...)
}

// DefaultResOps use the stacks directory, docker compose and nvmet.
var DefaultResOps = ResOps{
	LocalStacks: func() ([]StackDef, error) {
		entries, err := os.ReadDir(config.StacksDir)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var out []StackDef
		for _, e := range entries {
			if !e.IsDir() || !stackNameRe.MatchString(e.Name()) {
				continue
			}
			_, file := stackPaths(e.Name())
			b, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			out = append(out, StackDef{Name: e.Name(), YAML: string(b)})
		}
		return out, nil
	},
	WriteStack: func(s StackDef) error {
		if !stackNameRe.MatchString(s.Name) {
			return fmt.Errorf("invalid stack name %q", s.Name)
		}
		dir, file := stackPaths(s.Name)
		if cur, err := os.ReadFile(file); err == nil && bytes.Equal(cur, []byte(s.YAML)) {
			return nil
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		return os.WriteFile(file, []byte(s.YAML), 0o644)
	},
	StackRunning: func(name string) (bool, error) {
		out, err := compose(name, "ps", "--format", "json")
		if err != nil {
			return false, fmt.Errorf("%v: %s", err, bytes.TrimSpace(out))
		}
		return bytes.Contains(out, []byte(`"running"`)), nil
	},
	StackUp: func(name string) error {
		if out, err := compose(name, "up", "-d", "--remove-orphans"); err != nil {
			return fmt.Errorf("docker compose up %s: %v: %s", name, err, bytes.TrimSpace(out))
		}
		return nil
	},
	StackDown: func(name string) error {
		if out, err := compose(name, "down"); err != nil {
			return fmt.Errorf("docker compose down %s: %v: %s", name, err, bytes.TrimSpace(out))
		}
		return nil
	},
	LocalExports: func() ([]nvmet.Export, error) {
		e, err := nvmet.LoadExports(nvmet.TargetsFile)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return e, err
	},
	SaveExports:  func(e []nvmet.Export) error { return nvmet.SaveExports(nvmet.TargetsFile, e) },
	ApplyExports: nvmet.Apply,
}

// ── Publishing and storing definitions ────────────────────────────────────────

// LocalResources returns the resources of a group from this node's own
// definitions (served to the other candidates when this node owns it).
func (m *Manager) LocalResources(name string) (Resources, error) {
	g, err := Get(m.db, name)
	if err != nil {
		return Resources{}, err
	}
	stacks, err := m.res.LocalStacks()
	if err != nil {
		return Resources{}, err
	}
	exports, err := m.res.LocalExports()
	if err != nil {
		return Resources{}, err
	}
	return filterResources(*g, stacks, exports), nil
}

func storeResources(db *sql.DB, group string, r Resources) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM group_resources WHERE group_name = $1`, group); err != nil {
		return err
	}
	for _, s := range r.Stacks {
		b, _ := json.Marshal(map[string]string{"yaml": s.YAML})
		if _, err := tx.Exec(`INSERT INTO group_resources (group_name, kind, key, payload) VALUES ($1, 'stack', $2, $3)`, group, s.Name, b); err != nil {
			return err
		}
	}
	for _, e := range r.NVMe {
		b, _ := json.Marshal(e)
		if _, err := tx.Exec(`INSERT INTO group_resources (group_name, kind, key, payload) VALUES ($1, 'nvme', $2, $3)`, group, e.SubsystemNQN, b); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func loadResources(db *sql.DB, group string) (Resources, error) {
	r := Resources{Stacks: []StackDef{}, NVMe: []nvmet.Export{}}
	rows, err := db.Query(`SELECT kind, key, payload FROM group_resources WHERE group_name = $1 ORDER BY kind, key`, group)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, key string
		var raw []byte
		if err := rows.Scan(&kind, &key, &raw); err != nil {
			return r, err
		}
		switch kind {
		case "stack":
			var p struct {
				YAML string `json:"yaml"`
			}
			if json.Unmarshal(raw, &p) == nil {
				r.Stacks = append(r.Stacks, StackDef{Name: key, YAML: p.YAML})
			}
		case "nvme":
			var e nvmet.Export
			if json.Unmarshal(raw, &e) == nil {
				r.NVMe = append(r.NVMe, e)
			}
		}
	}
	return r, rows.Err()
}

// ── Activation ────────────────────────────────────────────────────────────────

// deactivate stops a group's stacks on this node and leaves its exports out.
// Called before the pools are exported or made read-only.
func (m *Manager) deactivate(g Group) error {
	r, err := m.knownResources(g)
	if err != nil {
		return err
	}
	var errs []error
	if err := m.holdAddress(g, false); err != nil {
		errs = append(errs, err)
	}
	for _, s := range r.Stacks {
		if running, err := m.res.StackRunning(s.Name); err == nil && !running {
			continue
		}
		if err := m.res.StackDown(s.Name); err != nil {
			errs = append(errs, err)
		}
	}
	if len(r.NVMe) > 0 {
		if err := m.applyExports(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// knownResources returns the definitions that apply to g on this node: on
// the owner its own definitions (authoritative: a stack deleted there stays
// deleted); elsewhere the owner's published ones plus any local stack or
// export that uses the group's pools (to keep them stopped).
func (m *Manager) knownResources(g Group) (Resources, error) {
	local, err := m.LocalResources(g.Name)
	if err != nil {
		return local, err
	}
	if g.Owner == m.self() {
		return local, nil
	}
	stored, err := loadResources(m.db, g.Name)
	if err != nil {
		return stored, err
	}
	seen := map[string]bool{}
	for _, s := range stored.Stacks {
		seen["s/"+s.Name] = true
	}
	for _, e := range stored.NVMe {
		seen["n/"+e.SubsystemNQN] = true
	}
	for _, s := range local.Stacks {
		if !seen["s/"+s.Name] {
			stored.Stacks = append(stored.Stacks, s)
		}
	}
	for _, e := range local.NVMe {
		if !seen["n/"+e.SubsystemNQN] {
			stored.NVMe = append(stored.NVMe, e)
		}
	}
	return stored, nil
}

// materialize writes the published definitions of g to this node's stacks
// directory and export list; called once when this node becomes the owner.
func (m *Manager) materialize(g Group) error {
	stored, err := loadResources(m.db, g.Name)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range stored.Stacks {
		if err := m.res.WriteStack(s); err != nil {
			errs = append(errs, fmt.Errorf("stack %s: %w", s.Name, err))
		}
	}
	if len(stored.NVMe) > 0 {
		local, err := m.res.LocalExports()
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		have := map[string]bool{}
		for _, e := range local {
			have[e.SubsystemNQN] = true
		}
		changed := false
		for _, e := range stored.NVMe {
			if !have[e.SubsystemNQN] {
				local = append(local, e)
				changed = true
			}
		}
		if changed {
			if err := m.res.SaveExports(local); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// applyExports applies this node's export list (only when it changed).
func (m *Manager) applyExports() error {
	gs, err := List(m.db)
	if err != nil {
		return err
	}
	local, err := m.res.LocalExports()
	if err != nil {
		return err
	}
	known := map[string][]nvmet.Export{}
	for _, g := range gs {
		r, _ := loadResources(m.db, g.Name)
		known[g.Name] = r.NVMe
	}
	list := appliedExports(local, gs, m.self(), known)
	b, _ := json.Marshal(list)
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:])
	m.fmu.Lock()
	same := h == m.exportsHash
	m.fmu.Unlock()
	if same {
		return nil
	}
	if len(list) == 0 && len(local) == 0 && m.exportsHash == "" {
		m.fmu.Lock()
		m.exportsHash = h
		m.fmu.Unlock()
		return nil // nothing configured, nothing applied yet
	}
	if err := m.res.ApplyExports(list); err != nil {
		return fmt.Errorf("applying NVMe-oF exports: %w", err)
	}
	m.fmu.Lock()
	m.exportsHash = h
	m.fmu.Unlock()
	return nil
}

// ActivateTick publishes, stores and runs group resources on this node.
func (m *Manager) ActivateTick() {
	gs, err := List(m.db)
	if err != nil {
		return
	}
	self := m.self()
	for _, g := range gs {
		var problems []string
		if g.Owner == self {
			if ok, _ := m.CanWritePool(g.Pools[0].Name); !ok {
				// Not serving (no quorum, pool missing): no apps, no address.
				if err := m.holdAddress(g, false); err != nil {
					log.Printf("GROUPS: %s: %v", g.Name, err)
				}
				continue
			}
			if err := m.holdAddress(g, true); err != nil {
				problems = append(problems, "floating address: "+err.Error())
			}
			r, err := m.knownResources(g)
			if err != nil {
				problems = append(problems, err.Error())
			} else {
				if err := storeResources(m.db, g.Name, r); err != nil {
					problems = append(problems, err.Error())
				}
				for _, s := range r.Stacks {
					if err := m.res.WriteStack(s); err != nil {
						problems = append(problems, fmt.Sprintf("stack %s: %v", s.Name, err))
						continue
					}
					if running, err := m.res.StackRunning(s.Name); err == nil && running {
						continue
					}
					if err := m.res.StackUp(s.Name); err != nil {
						problems = append(problems, err.Error())
					}
				}
			}
		} else if g.IsCandidate(self) {
			if err := m.holdAddress(g, false); err != nil {
				problems = append(problems, "floating address: "+err.Error())
			}
			r, _ := loadResources(m.db, g.Name)
			for _, s := range r.Stacks {
				if running, err := m.res.StackRunning(s.Name); err == nil && running {
					log.Printf("GROUPS: stopping stack %s: group %s is owned by another node", s.Name, g.Name)
					if err := m.res.StackDown(s.Name); err != nil {
						problems = append(problems, err.Error())
					}
				}
			}
		}
		m.fmu.Lock()
		if m.resProblems == nil {
			m.resProblems = map[string][]string{}
		}
		m.resProblems[g.Name] = problems
		m.fmu.Unlock()
	}
	if err := m.applyExports(); err != nil {
		log.Printf("GROUPS: %v", err)
	}
}

// StartActivation runs ActivateTick every interval.
func (m *Manager) StartActivation(interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			m.ActivateTick()
		}
	}()
}
