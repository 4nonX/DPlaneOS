package handlers

import (
	"bufio"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/configstore"
	"dplaned/internal/groups"
	"dplaned/internal/hasplit"
	"dplaned/internal/quorum"
	"dplaned/internal/secrets"
)

// Moving an HA pair off the shared Patroni database (Design 0001 phase 3e).
// See internal/hasplit for the sequence.

const patroniAPI = "http://127.0.0.1:8008"

// HASplitHandler serves /api/ha/split and runs the migration on this node.
type HASplitHandler struct {
	db     *sql.DB
	q      *QuorumHandler
	groups *groups.Manager

	mu       sync.Mutex
	launched bool   // the NixOS switch was started by this process
	status   string // last progress message of the watcher
}

func NewHASplitHandler(db *sql.DB, q *QuorumHandler, gm *groups.Manager) *HASplitHandler {
	return &HASplitHandler{db: db, q: q, groups: gm}
}

type splitPlan struct {
	State       string     `json:"state"`
	Coordinator string     `json:"coordinator"`
	GroupName   string     `json:"group_name"`
	Topology    string     `json:"topology"`
	Pools       []string   `json:"pools"`
	Address     string     `json:"address"`
	Interface   string     `json:"interface"`
	Error       string     `json:"error"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	secret      string
}

type splitNode struct {
	MachineID    string    `json:"machine_id"`
	Hostname     string    `json:"hostname"`
	IP           string    `json:"ip"`
	ConfigNodeID string    `json:"config_node_id"`
	URL          string    `json:"url"`
	PatroniRole  string    `json:"patroni_role"`
	Status       string    `json:"status"`
	Error        string    `json:"error"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func loadSplit(db *sql.DB) (*splitPlan, []splitNode, error) {
	var p splitPlan
	var pools []byte
	err := db.QueryRow(`SELECT state, coordinator, secret, group_name, topology, pools, address, interface, error, started_at, finished_at
		FROM ha_split WHERE id = 1`).Scan(&p.State, &p.Coordinator, &p.secret, &p.GroupName, &p.Topology, &pools,
		&p.Address, &p.Interface, &p.Error, &p.StartedAt, &p.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	_ = json.Unmarshal(pools, &p.Pools)
	rows, err := db.Query(`SELECT machine_id, hostname, ip, config_node_id, url, patroni_role, status, error, updated_at
		FROM ha_split_nodes ORDER BY machine_id`)
	if err != nil {
		return &p, nil, err
	}
	defer rows.Close()
	var nodes []splitNode
	for rows.Next() {
		var n splitNode
		if err := rows.Scan(&n.MachineID, &n.Hostname, &n.IP, &n.ConfigNodeID, &n.URL, &n.PatroniRole, &n.Status, &n.Error, &n.UpdatedAt); err != nil {
			return &p, nil, err
		}
		nodes = append(nodes, n)
	}
	return &p, nodes, rows.Err()
}

func setNodeStatus(db *sql.DB, machineID, status, msg string) {
	if _, err := db.Exec(`UPDATE ha_split_nodes SET status = $2, error = $3, updated_at = NOW() WHERE machine_id = $1`, machineID, status, msg); err != nil {
		log.Printf("HA SPLIT: recording status %s: %v", status, err)
	}
}

func setPlanError(db *sql.DB, msg string) {
	_, _ = db.Exec(`UPDATE ha_split SET error = $1 WHERE id = 1`, msg)
}

// ── Reading the current setup ─────────────────────────────────────────────────

func patroniGet(path string) ([]byte, error) {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(patroniAPI + path)
	if err != nil {
		return nil, fmt.Errorf("Patroni is not reachable on this node: %w", err)
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// patroniView returns the cluster members and this node's member.
func patroniView() ([]hasplit.Member, hasplit.Member, hasplit.Member, []string, error) {
	b, err := patroniGet("/cluster")
	if err != nil {
		return nil, hasplit.Member{}, hasplit.Member{}, nil, err
	}
	members, err := hasplit.ParseCluster(b)
	if err != nil {
		return nil, hasplit.Member{}, hasplit.Member{}, nil, err
	}
	b, err = patroniGet("/patroni")
	if err != nil {
		return nil, hasplit.Member{}, hasplit.Member{}, nil, err
	}
	self, err := hasplit.SelfName(b)
	if err != nil {
		return nil, hasplit.Member{}, hasplit.Member{}, nil, err
	}
	me, other, problems := hasplit.Preflight(members, self)
	return members, me, other, problems, nil
}

// rootPools are pools holding the operating system (never part of a group).
func rootPools() map[string]bool {
	out := map[string]bool{}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || fields[2] != "zfs" {
			continue
		}
		switch fields[1] {
		case "/", "/nix", "/boot", "/var", "/persist":
			out[strings.SplitN(fields[0], "/", 2)[0]] = true
		}
	}
	return out
}

var keepalivedConfRe = regexp.MustCompile(`-f\s+(\S+)`)

// keepalivedVIP reads the floating address from the running keepalived configuration.
func keepalivedVIP() (string, string) {
	paths := []string{"/etc/keepalived/keepalived.conf"}
	if unit, err := os.ReadFile("/etc/systemd/system/keepalived.service"); err == nil {
		if m := keepalivedConfRe.FindSubmatch(unit); m != nil {
			paths = append([]string{string(m[1])}, paths...)
		}
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		vip, iface := hasplit.ParseKeepalived(string(b))
		if vip == "" {
			continue
		}
		var nets []*net.IPNet
		if ifc, err := net.InterfaceByName(iface); err == nil {
			if addrs, err := ifc.Addrs(); err == nil {
				for _, a := range addrs {
					if n, ok := a.(*net.IPNet); ok {
						nets = append(nets, n)
					}
				}
			}
		}
		return hasplit.WithPrefix(vip, nets), iface
	}
	return "", ""
}

func selfURL(ip string) string {
	scheme := "http"
	if NixWriter != nil && NixWriter.State().TLSCert != "" {
		scheme = "https"
	}
	host := ip
	if strings.Contains(ip, ":") {
		host = "[" + ip + "]"
	}
	return scheme + "://" + host
}

func randomSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ── API ───────────────────────────────────────────────────────────────────────

type splitPreflight struct {
	Problems   []string `json:"problems"`
	Self       string   `json:"self"`
	Other      string   `json:"other"`
	SelfRole   string   `json:"self_role"`
	Pools      []string `json:"pools"`
	Topology   string   `json:"topology"`
	Address    string   `json:"address"`
	Interface  string   `json:"interface"`
	Interfaces []string `json:"interfaces"`
}

func (h *HASplitHandler) preflight() splitPreflight {
	pf := splitPreflight{Problems: []string{}, Pools: []string{}, Interfaces: addressInterfaces()}
	if NixWriter == nil {
		pf.Problems = append(pf.Problems, "this system is not a DPlaneOS NixOS appliance")
		return pf
	}
	if !NixWriter.State().HAEnable {
		pf.Problems = append(pf.Problems, "this node does not use the shared database")
		return pf
	}
	_, me, other, problems, err := patroniView()
	if err != nil {
		pf.Problems = append(pf.Problems, err.Error())
		return pf
	}
	pf.Problems = append(pf.Problems, problems...)
	pf.Self, pf.Other, pf.SelfRole = me.Host, other.Host, me.Role
	if names, err := h.groups.ImportedPoolNames(); err == nil {
		root := rootPools()
		for _, n := range names {
			if !root[n] {
				pf.Pools = append(pf.Pools, n)
			}
		}
	}
	if len(pf.Pools) == 0 {
		pf.Problems = append(pf.Problems, "no data pools are imported on this node: start the migration on the node that has them")
	}
	if _, err := secrets.Seal("check"); err != nil {
		pf.Problems = append(pf.Problems, "the secrets key cannot seal values: "+err.Error())
	}
	if b, err := os.ReadFile("/etc/dplaneos/patroni.yaml"); err == nil {
		pf.Topology = hasplit.Topology(string(b))
	} else {
		pf.Topology = "shared"
	}
	pf.Address, pf.Interface = keepalivedVIP()
	return pf
}

// Status: GET /api/ha/split
func (h *HASplitHandler) Status(w http.ResponseWriter, r *http.Request) {
	plan, nodes, err := loadSplit(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	shared := NixWriter != nil && NixWriter.State().HAEnable
	out := map[string]any{"success": true, "shared_database": shared, "plan": plan, "nodes": nodes, "self": LocalNodeID()}
	if shared && (plan == nil || plan.State == "cancelled") {
		out["preflight"] = h.preflight()
	}
	h.mu.Lock()
	out["progress"] = h.status
	h.mu.Unlock()
	respondOK(w, out)
}

// Start: POST /api/ha/split {group_name, topology, pools, address, interface}
func (h *HASplitHandler) Start(w http.ResponseWriter, r *http.Request) {
	var b struct {
		GroupName string   `json:"group_name"`
		Topology  string   `json:"topology"`
		Pools     []string `json:"pools"`
		Address   string   `json:"address"`
		Interface string   `json:"interface"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if plan, _, _ := loadSplit(h.db); plan != nil && plan.State != "cancelled" {
		respondOK(w, map[string]any{"success": false, "error": "a migration is already " + plan.State})
		return
	}
	pf := h.preflight()
	if len(pf.Problems) > 0 {
		respondOK(w, map[string]any{"success": false, "error": strings.Join(pf.Problems, "; ")})
		return
	}
	if b.Topology != groups.Shared && b.Topology != groups.Replicated {
		respondOK(w, map[string]any{"success": false, "error": "topology must be shared or replicated"})
		return
	}
	if len(b.Pools) == 0 {
		respondOK(w, map[string]any{"success": false, "error": "choose at least one pool"})
		return
	}
	for _, p := range b.Pools {
		if !slices.Contains(pf.Pools, p) {
			respondOK(w, map[string]any{"success": false, "error": "pool " + p + " is not imported on this node"})
			return
		}
	}
	// The group definition is checked now, not after the switch.
	probe := groups.Group{Name: b.GroupName, Topology: b.Topology, Address: b.Address, Interface: b.Interface,
		Pools: []groups.PoolRef{{Name: b.Pools[0], GUID: "1"}}}
	if err := probe.Validate(); err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	sealed, err := secrets.Seal(randomSecret())
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": "sealing the pair secret: " + err.Error()})
		return
	}
	pools, _ := json.Marshal(b.Pools)
	host, _ := os.Hostname()
	tx, err := h.db.Begin()
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM ha_split_nodes`); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(`INSERT INTO ha_split (id, state, coordinator, secret, group_name, topology, pools, address, interface, error, started_at, finished_at)
		VALUES (1, 'planned', $1, $2, $3, $4, $5, $6, $7, '', NOW(), NULL)
		ON CONFLICT (id) DO UPDATE SET state = 'planned', coordinator = EXCLUDED.coordinator, secret = EXCLUDED.secret,
			group_name = EXCLUDED.group_name, topology = EXCLUDED.topology, pools = EXCLUDED.pools,
			address = EXCLUDED.address, interface = EXCLUDED.interface, error = '', started_at = NOW(), finished_at = NULL`,
		LocalNodeID(), sealed, b.GroupName, b.Topology, pools, b.Address, b.Interface); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(`INSERT INTO ha_split_nodes (machine_id, hostname, ip, config_node_id, url, patroni_role, status)
		VALUES ($1, $2, $3, gen_random_uuid()::text, $4, $5, 'ready')`,
		LocalNodeID(), host, pf.Self, selfURL(pf.Self), pf.SelfRole); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("HA SPLIT: migration off the shared database planned (group %s, %s, pools %v)", b.GroupName, b.Topology, b.Pools)
	go h.Tick()
	respondOK(w, map[string]any{"success": true})
}

// Cancel: POST /api/ha/split/cancel (before the switch has started)
func (h *HASplitHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	res, err := h.db.Exec(`UPDATE ha_split SET state = 'cancelled' WHERE id = 1 AND state = 'planned'`)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		respondOK(w, map[string]any{"success": false, "error": "only a migration that has not started switching can be cancelled"})
		return
	}
	_, _ = h.db.Exec(`DELETE FROM ha_split_nodes`)
	respondOK(w, map[string]any{"success": true})
}

// ── The migration on this node ────────────────────────────────────────────────

func (h *HASplitHandler) progress(msg string) {
	h.mu.Lock()
	changed := h.status != msg
	h.status = msg
	h.mu.Unlock()
	if changed && msg != "" {
		log.Printf("HA SPLIT: %s", msg)
	}
}

// StartWatcher runs Tick every interval.
func (h *HASplitHandler) StartWatcher(interval time.Duration) {
	go func() {
		for {
			h.Tick()
			time.Sleep(interval)
		}
	}()
}

// Tick advances the migration on this node.
func (h *HASplitHandler) Tick() {
	if NixWriter == nil {
		return
	}
	plan, nodes, err := loadSplit(h.db)
	if err != nil || plan == nil || plan.State == "cancelled" || plan.State == "finished" {
		return
	}
	self := LocalNodeID()
	var me, other *splitNode
	for i := range nodes {
		if nodes[i].MachineID == self {
			me = &nodes[i]
		} else {
			other = &nodes[i]
		}
	}
	if NixWriter.State().HAEnable {
		h.onShared(plan, me, other)
		return
	}
	h.afterSplit(plan, me, other)
}

// onShared: both nodes still use the shared database.
func (h *HASplitHandler) onShared(plan *splitPlan, me, other *splitNode) {
	members, pm, _, problems, err := patroniView()
	if err != nil {
		h.progress(err.Error())
		return
	}
	switch plan.State {
	case "planned":
		if me == nil {
			// The other node started it: add this node's row.
			status, msg := "ready", ""
			if len(problems) > 0 {
				status, msg = "failed", strings.Join(problems, "; ")
			}
			host, _ := os.Hostname()
			if _, err := h.db.Exec(`INSERT INTO ha_split_nodes (machine_id, hostname, ip, config_node_id, url, patroni_role, status, error)
				VALUES ($1, $2, $3, gen_random_uuid()::text, $4, $5, $6, $7) ON CONFLICT (machine_id) DO NOTHING`,
				LocalNodeID(), host, pm.Host, selfURL(pm.Host), pm.Role, status, msg); err != nil {
				h.progress("recording this node in the plan: " + err.Error())
				return
			}
			h.progress("ready to leave the shared database")
			return
		}
		if plan.Coordinator == LocalNodeID() {
			if other == nil {
				h.progress("waiting for the other node to confirm the plan")
				return
			}
			if other.Status != "ready" {
				h.progress("the other node cannot take part: " + other.Error)
				return
			}
			if _, err := h.db.Exec(`UPDATE ha_split SET state = 'go' WHERE id = 1 AND state = 'planned'`); err == nil {
				h.progress("both nodes are ready; the replica switches first")
			}
		}
	case "go":
		if me == nil {
			return
		}
		switch me.Status {
		case "ready":
			ok, why := hasplit.MaySwitch(pm.Leader(), members, statusOf(other))
			if !ok {
				h.progress(why)
				return
			}
			setNodeStatus(h.db, me.MachineID, "switching", "")
			h.progress("switching")
		case "switching":
			// The replica's copy must contain its own "switching" row.
			if !pm.Leader() && !hasplit.CaughtUp(members) {
				h.progress("waiting for this node's database copy to catch up")
				return
			}
			h.launchSwitch(me)
		}
	}
}

func statusOf(n *splitNode) string {
	if n == nil {
		return ""
	}
	return n.Status
}

// launchSwitch changes the NixOS configuration (no Patroni; the database
// stays as this node's own) and applies it in a unit that outlives dplaned.
func (h *HASplitHandler) launchSwitch(me *splitNode) {
	h.mu.Lock()
	if h.launched {
		h.mu.Unlock()
		if state := applyUnitState(); strings.Contains(state, "failed") {
			h.progress("applying the new configuration failed (journalctl -u dplaneos-apply-config); this node stays on the shared database")
			if err := NixWriter.SetHA(true); err == nil {
				setNodeStatus(h.db, me.MachineID, "failed", "nixos-rebuild failed: see journalctl -u dplaneos-apply-config")
			}
		}
		return
	}
	h.launched = true
	h.mu.Unlock()
	if err := NixWriter.LeavePatroni(); err != nil {
		h.progress("writing the NixOS state: " + err.Error())
		h.mu.Lock()
		h.launched = false
		h.mu.Unlock()
		return
	}
	h.progress("applying the configuration without the shared database (this restarts the daemon)")
	if out, err := cmdutil.RunFast("systemctl_apply_config", "start", "--no-block", "dplaneos-apply-config.service"); err != nil {
		h.progress(fmt.Sprintf("starting dplaneos-apply-config: %v: %s", err, strings.TrimSpace(string(out))))
		_ = NixWriter.SetHA(true)
		h.mu.Lock()
		h.launched = false
		h.mu.Unlock()
	}
}

func applyUnitState() string {
	out, _ := cmdutil.RunFast("systemctl_apply_config_state", "show", "-p", "ActiveState,Result", "--value", "dplaneos-apply-config.service")
	return string(out)
}

// afterSplit: this node runs on its own database copy.
func (h *HASplitHandler) afterSplit(plan *splitPlan, me, other *splitNode) {
	if me != nil && me.Status == "switching" {
		setNodeStatus(h.db, me.MachineID, "split", "")
	}
	if plan.State != "go" || plan.Coordinator != LocalNodeID() || me == nil || other == nil {
		if plan.State == "go" && plan.Coordinator != LocalNodeID() {
			h.progress("this node left the shared database; " + plan.Coordinator + " forms the cluster and the storage group")
		}
		return
	}
	if err := h.finish(plan, me, other); err != nil {
		h.progress(err.Error())
		setPlanError(h.db, err.Error())
		return
	}
	_, _ = h.db.Exec(`UPDATE ha_split SET state = 'finished', error = '', finished_at = NOW() WHERE id = 1`)
	h.progress("migration finished")
}

// finish (coordinator, after both nodes split): reach the other node over
// the paired channel, form the Corosync cluster, create the storage group.
func (h *HASplitHandler) finish(plan *splitPlan, me, other *splitNode) error {
	selfID, err := configstore.NodeID(h.db)
	if err != nil {
		return err
	}
	if selfID != me.ConfigNodeID {
		return fmt.Errorf("this node's configuration identity was not set from the plan (dplaneos-patroni-split did not run)")
	}
	var probe struct {
		Groups []groups.Group `json:"groups"`
	}
	if err := configstore.CallPeer(h.db, other.ConfigNodeID, "GET", "/api/config/sync/peer/groups", nil, &probe); err != nil {
		return fmt.Errorf("waiting for %s to come back on its own database: %v", other.Hostname, err)
	}
	if cfg, _, err := quorum.Load(h.db); err != nil {
		return err
	} else if cfg == nil {
		h.progress("forming the cluster with " + other.Hostname)
		_, err := quorum.Form(h.db,
			quorum.Node{Name: clusterNodeName(me.Hostname), Addr: me.IP, NodeKey: selfID},
			quorum.Node{Name: clusterNodeName(other.Hostname), Addr: other.IP, NodeKey: other.ConfigNodeID},
			h.q.push)
		h.q.mon.Refresh()
		if err != nil {
			return fmt.Errorf("forming the cluster: %w", err)
		}
	}
	if _, err := groups.Get(h.db, plan.GroupName); errors.Is(err, sql.ErrNoRows) {
		h.progress("creating storage group " + plan.GroupName)
		// A created group whose push to the other node failed is still
		// created (the other node pulls it).
		if g, err := h.groups.CreateWith(plan.GroupName, plan.Topology, plan.Pools, []string{other.ConfigNodeID}, 300, false); g == nil {
			return fmt.Errorf("creating storage group %s: %w", plan.GroupName, err)
		}
	} else if err != nil {
		return err
	}
	if plan.Address != "" {
		if g, err := groups.Get(h.db, plan.GroupName); err == nil && g.Address != plan.Address {
			if _, err := h.groups.SetAddress(plan.GroupName, plan.Address, plan.Interface); err != nil {
				return fmt.Errorf("setting the floating address: %w", err)
			}
		}
	}
	return nil
}
