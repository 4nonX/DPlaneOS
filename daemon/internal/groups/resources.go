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
	"dplaned/internal/iscsit"
	"dplaned/internal/nvmet"
)

// Group resources (Design 0001 phase 3d).
//
// A Docker stack whose volumes are on a group's pools, and an iSCSI target or
// NVMe-oF export of one of its zvols, belong to the group, not to a node:
// they run only on the group's owner. The owner publishes their definitions; every candidate
// keeps a copy, and the activation loop on each node starts them where this
// node owns the group and stops them everywhere else. Before a node gives a
// group up (planned move, release), its resources stop first: a pool with
// running containers cannot be exported.

// Resources of a group.
type Resources struct {
	Stacks []StackDef      `json:"stacks"`
	NVMe   []nvmet.Export  `json:"nvme"`
	ISCSI  []iscsit.Target `json:"iscsi"`
}

func emptyResources() Resources {
	return Resources{Stacks: []StackDef{}, NVMe: []nvmet.Export{}, ISCSI: []iscsit.Target{}}
}

// summary is what a status shows of a group's resources: which they are,
// without what is secret in them (the CHAP secrets of iSCSI initiators, and
// compose files, which often carry passwords).
func (r Resources) summary() Resources {
	out := emptyResources()
	for _, s := range r.Stacks {
		out.Stacks = append(out.Stacks, StackDef{Name: s.Name})
	}
	out.NVMe = append(out.NVMe, r.NVMe...)
	for _, t := range r.ISCSI {
		c := t
		c.ACLs = make([]iscsit.ACL, 0, len(t.ACLs))
		for _, a := range t.ACLs {
			c.ACLs = append(c.ACLs, iscsit.ACL{InitiatorIQN: a.InitiatorIQN, CHAPUser: a.CHAPUser})
		}
		out.ISCSI = append(out.ISCSI, c)
	}
	return out
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

// filterResources picks the stacks, exports and targets of g out of all
// definitions.
func filterResources(g Group, stacks []StackDef, exports []nvmet.Export, targets []iscsit.Target) Resources {
	r := emptyResources()
	for _, t := range targets {
		if g.HasPool(zvolPool(t.Zvol)) {
			r.ISCSI = append(r.ISCSI, t)
		}
	}
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

func groupOfPool(groups []Group, pool string) (Group, bool) {
	for _, g := range groups {
		if g.HasPool(pool) {
			return g, true
		}
	}
	return Group{}, false
}

// appliedTargets is appliedExports for iSCSI targets.
func appliedTargets(local []iscsit.Target, groups []Group, self string, known map[string][]iscsit.Target) []iscsit.Target {
	have := map[string]bool{}
	var out []iscsit.Target
	for _, t := range local {
		if g, ok := groupOfPool(groups, zvolPool(t.Zvol)); ok && g.Owner != self {
			continue
		}
		have[t.IQN] = true
		out = append(out, t)
	}
	for _, g := range groups {
		if g.Owner != self {
			continue
		}
		for _, t := range known[g.Name] {
			if !have[t.IQN] {
				have[t.IQN] = true
				out = append(out, t)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IQN < out[j].IQN })
	return out
}

// appliedExports is the list of exports this node applies: its own list
// without the exports of groups it does not own, plus the exports of groups
// it owns that it only knows from their previous owner.
func appliedExports(local []nvmet.Export, groups []Group, self string, known map[string][]nvmet.Export) []nvmet.Export {
	ownerOf := func(pool string) (Group, bool) { return groupOfPool(groups, pool) }
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
	LocalTargets func() ([]iscsit.Target, error)
	SaveTargets  func([]iscsit.Target) error
	ApplyTargets func([]iscsit.Target) error
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

// DefaultResOps use the stacks directory, docker compose, nvmet and LIO.
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
	LocalTargets: func() ([]iscsit.Target, error) { return iscsit.Load(iscsit.TargetsFile) },
	SaveTargets:  func(t []iscsit.Target) error { return iscsit.Save(iscsit.TargetsFile, t) },
	ApplyTargets: iscsit.Apply,
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
	targets, err := m.res.LocalTargets()
	if err != nil {
		return Resources{}, err
	}
	return filterResources(*g, stacks, exports, targets), nil
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
	for _, t := range r.ISCSI {
		b, _ := json.Marshal(t)
		if _, err := tx.Exec(`INSERT INTO group_resources (group_name, kind, key, payload) VALUES ($1, 'iscsi', $2, $3)`, group, t.IQN, b); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func loadResources(db *sql.DB, group string) (Resources, error) {
	r := emptyResources()
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
		case "iscsi":
			var t iscsit.Target
			if json.Unmarshal(raw, &t) == nil {
				r.ISCSI = append(r.ISCSI, t)
			}
		}
	}
	return r, rows.Err()
}

// ── Activation ────────────────────────────────────────────────────────────────

// deactivate stops a group's stacks on this node and stops serving its
// exports and targets (they hold their volumes open: a pool with one of
// them served cannot be exported). Called before the pools are exported or
// made read-only, with the group marked as moving.
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
	if err := m.applyExports(true); err != nil {
		errs = append(errs, err)
	}
	if err := m.applyTargets(); err != nil {
		errs = append(errs, err)
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
	for _, t := range stored.ISCSI {
		seen["i/"+t.IQN] = true
	}
	for _, t := range local.ISCSI {
		if !seen["i/"+t.IQN] {
			stored.ISCSI = append(stored.ISCSI, t)
		}
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
	if len(stored.ISCSI) > 0 {
		local, err := m.res.LocalTargets()
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		have := map[string]bool{}
		for _, t := range local {
			have[t.IQN] = true
		}
		changed := false
		for _, t := range stored.ISCSI {
			if !have[t.IQN] {
				local = append(local, t)
				changed = true
			}
		}
		if changed {
			if err := m.res.SaveTargets(local); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// servingGroups lists the groups as this node serves them: a group that is
// being moved away is not served any more, whoever its owner still is (its
// exports and targets must let go of their volumes before the pools can be
// exported).
func (m *Manager) servingGroups() ([]Group, error) {
	gs, err := List(m.db)
	if err != nil {
		return nil, err
	}
	for i := range gs {
		if m.isMoving(gs[i].Name) && gs[i].Owner == m.self() {
			gs[i].Owner = ""
		}
	}
	return gs, nil
}

// applyExports applies this node's NVMe-oF exports: only when the list
// changed, unless force is set (after a change through the API, or when
// something must be stopped now).
func (m *Manager) applyExports(force bool) error {
	gs, err := m.servingGroups()
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
	first := m.exportsHash == ""
	m.fmu.Unlock()
	if same && !force {
		return nil
	}
	if len(list) == 0 && len(local) == 0 && first && !force {
		m.fmu.Lock()
		m.exportsHash = h
		m.fmu.Unlock()
		return nil // nothing configured, nothing applied yet
	}
	if err := m.res.ApplyExports(list); err != nil {
		// Not remembered as applied: the next pass tries again (a volume
		// that was not there yet).
		m.fmu.Lock()
		m.exportsHash = "retry"
		m.fmu.Unlock()
		return fmt.Errorf("applying NVMe-oF exports: %w", err)
	}
	m.fmu.Lock()
	m.exportsHash = h
	m.fmu.Unlock()
	return nil
}

// applyTargets applies this node's iSCSI targets. Applying a list that is
// already served runs nothing, so this is done on every pass.
func (m *Manager) applyTargets() error {
	gs, err := m.servingGroups()
	if err != nil {
		return err
	}
	local, err := m.res.LocalTargets()
	if err != nil {
		return err
	}
	known := map[string][]iscsit.Target{}
	for _, g := range gs {
		r, _ := loadResources(m.db, g.Name)
		known[g.Name] = r.ISCSI
	}
	if err := m.res.ApplyTargets(appliedTargets(local, gs, m.self(), known)); err != nil {
		return fmt.Errorf("applying iSCSI targets: %w", err)
	}
	return nil
}

// ApplyExports applies this node's NVMe-oF exports now (the API calls it
// after a change).
func (m *Manager) ApplyExports() error { return m.applyExports(true) }

// ApplyTargets applies this node's iSCSI targets now (the API calls it
// after a change).
func (m *Manager) ApplyTargets() error { return m.applyTargets() }

// ActivateTick publishes, stores and runs group resources on this node.
func (m *Manager) ActivateTick() {
	gs, err := List(m.db)
	if err != nil {
		return
	}
	self := m.self()
	for _, g := range gs {
		if m.isMoving(g.Name) {
			continue
		}
		var problems []string
		if g.Owner == self {
			v, err := m.currentView()
			if err != nil {
				continue // cannot tell (e.g. zpool list timed out): change nothing
			}
			if !Evaluate(g, v).CanWrite {
				// Not serving (no quorum, pool missing): no apps, no address.
				if err := m.holdAddress(g, false); err != nil {
					log.Printf("GROUPS: %s: %v", g.Name, err)
				}
				continue
			}
			// A planned move may have started since this pass read the
			// group (the view above can take seconds): it releases the
			// address and the apps, and this pass must not take them back.
			if m.isMoving(g.Name) {
				continue
			}
			if err := m.holdAddress(g, true); err != nil {
				problems = append(problems, "floating address: "+err.Error())
			}
			if m.isMoving(g.Name) {
				if err := m.holdAddress(g, false); err != nil {
					log.Printf("GROUPS: %s: %v", g.Name, err)
				}
				continue
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
					if m.isMoving(g.Name) {
						break
					}
					if err := m.res.StackUp(s.Name); err != nil {
						problems = append(problems, err.Error())
					}
					if m.isMoving(g.Name) {
						// The move stops the group's apps; one started
						// while it did so is stopped again.
						if err := m.res.StackDown(s.Name); err != nil {
							log.Printf("GROUPS: %s: stopping stack %s: %v", g.Name, s.Name, err)
						}
						break
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
	if err := m.applyExports(false); err != nil {
		m.logOnce("exports", err)
	} else {
		m.logOnce("exports", nil)
	}
	if err := m.applyTargets(); err != nil {
		m.logOnce("targets", err)
	} else {
		m.logOnce("targets", nil)
	}
}

// logOnce logs a problem of the activation loop when it appears or changes,
// not on every pass.
func (m *Manager) logOnce(what string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	m.fmu.Lock()
	if m.lastLogged == nil {
		m.lastLogged = map[string]string{}
	}
	changed := m.lastLogged[what] != msg
	m.lastLogged[what] = msg
	m.fmu.Unlock()
	if changed && msg != "" {
		log.Printf("GROUPS: %s", msg)
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
