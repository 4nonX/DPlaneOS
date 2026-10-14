package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/gorilla/mux"

	"dplaned/internal/configstore"
	"dplaned/internal/groups"
	"dplaned/internal/ha"
	"dplaned/internal/quorum"
)

// Cluster map, maintenance mode and safety tests (Design 0001 section 5.10,
// phase 4).

// ClusterHandler serves the cluster map, maintenance mode and the tests.
type ClusterHandler struct {
	db  *sql.DB
	mgr *groups.Manager
	mon *quorum.Monitor
}

func NewClusterHandler(db *sql.DB, mgr *groups.Manager, mon *quorum.Monitor) *ClusterHandler {
	return &ClusterHandler{db: db, mgr: mgr, mon: mon}
}

// MapNode is one machine on the cluster map.
type MapNode struct {
	Key         string     `json:"key,omitempty"` // configstore node id; empty for voters
	Name        string     `json:"name"`
	Addr        string     `json:"addr,omitempty"` // cluster (ring) address
	Self        bool       `json:"self"`
	Voter       bool       `json:"voter"`      // corosync-only member: votes, holds no storage
	InCluster   bool       `json:"in_cluster"` // configured Corosync member
	Paired      bool       `json:"paired"`     // exchanges configuration with this node
	Online      bool       `json:"online"`     // in this node's quorum partition (cluster members)
	Reachable   bool       `json:"reachable"`  // answers on the peer channel (paired nodes)
	LastContact *time.Time `json:"last_contact,omitempty"`
	Waiting     int        `json:"waiting"` // changes of this node the peer has not pulled
	Maintenance bool       `json:"maintenance"`
	Coordinator bool       `json:"coordinator"`
}

// MapReplication is the replication of one pool to one copy.
type MapReplication struct {
	Pool    string     `json:"pool"`
	Target  string     `json:"target"`
	LastOK  *time.Time `json:"last_ok_at"`
	LagSecs int64      `json:"lag_secs"` // -1: never replicated
	Error   string     `json:"error,omitempty"`
}

// MapGroup is one storage group on the map.
type MapGroup struct {
	Name         string           `json:"name"`
	Topology     string           `json:"topology"`
	Owner        string           `json:"owner"`
	Epoch        int64            `json:"epoch"`
	Candidates   []string         `json:"candidates"`
	Pools        []string         `json:"pools"`
	Serving      bool             `json:"serving"` // the owner may write (known only on the owner)
	Problems     []string         `json:"problems"`
	Failover     string           `json:"failover,omitempty"`
	IntervalSecs int              `json:"interval_secs,omitempty"`
	Replication  []MapReplication `json:"replication,omitempty"`
}

// ClusterMap is everything the map page shows, from this node's view.
type ClusterMap struct {
	Self        string     `json:"self"`
	Mode        string     `json:"mode"` // standalone, connected, isolated, independent
	ModeSince   *time.Time `json:"mode_since,omitempty"`
	Nodes       []MapNode  `json:"nodes"`
	Groups      []MapGroup `json:"groups"`
	Coordinator string     `json:"coordinator,omitempty"`
	Term        int64      `json:"term,omitempty"`
	Quorum      struct {
		Configured     bool   `json:"configured"`
		ClusterName    string `json:"cluster_name,omitempty"`
		Quorate        bool   `json:"quorate"`
		ExpectedVotes  int    `json:"expected_votes"`
		TotalVotes     int    `json:"total_votes"`
		AutoFailover   bool   `json:"auto_failover"`
		AutoFailoverNo string `json:"auto_failover_reason,omitempty"`
		QDevice        string `json:"qdevice,omitempty"`
		QDeviceVoting  bool   `json:"qdevice_voting"`
	} `json:"quorum"`
	Conflicts int `json:"conflicts"`
	Pending   int `json:"pending"`
	Proposals int `json:"proposals"`
}

// buildMap assembles the map. Pure apart from its inputs, for tests.
func buildMap(self, selfName string, sync *configstore.SyncStatus, cfg *quorum.Config, st quorum.Status, in quorum.Info,
	gs []groups.GroupStatus, maint map[string]bool, proposals int, now time.Time) ClusterMap {
	cm := ClusterMap{Self: self, Mode: sync.Mode, ModeSince: sync.Since, Coordinator: in.Coordinator, Term: in.Term,
		Conflicts: sync.Conflicts, Pending: sync.Pending, Proposals: proposals, Nodes: []MapNode{}, Groups: []MapGroup{}}

	nodes := map[string]*MapNode{}
	var order []string
	add := func(id string, n MapNode) *MapNode {
		if cur, ok := nodes[id]; ok {
			return cur
		}
		nodes[id] = &n
		order = append(order, id)
		return nodes[id]
	}
	add(self, MapNode{Key: self, Name: selfName, Self: true, Reachable: true})
	for _, p := range sync.Peers {
		n := add(p.ID, MapNode{Key: p.ID, Name: p.Name})
		n.Paired, n.Reachable, n.LastContact, n.Waiting = true, p.Reachable, p.LastContact, p.Waiting
	}
	online := map[string]bool{}
	for _, k := range in.Online {
		online[k] = true
	}
	if cfg != nil {
		cm.Quorum.Configured, cm.Quorum.ClusterName = true, cfg.ClusterName
		members := map[int]bool{}
		for _, m := range st.Members {
			members[m.NodeID] = true
		}
		for _, cn := range cfg.Nodes {
			id := cn.NodeKey
			if cn.Voter || id == "" {
				id = "voter:" + cn.Name
			}
			n := add(id, MapNode{Key: cn.NodeKey, Name: cn.Name})
			n.Addr, n.Voter, n.InCluster = cn.Addr, cn.Voter, true
			if n.Name == "" {
				n.Name = cn.Name
			}
			n.Online = online[cn.NodeKey] || (cn.Voter && members[cn.ID])
		}
		if cfg.QDevice != nil {
			cm.Quorum.QDevice = cfg.QDevice.Host
		}
	}
	cm.Quorum.Quorate, cm.Quorum.ExpectedVotes, cm.Quorum.TotalVotes = in.Quorate, in.ExpectedVotes, st.TotalVotes
	cm.Quorum.AutoFailover, cm.Quorum.AutoFailoverNo, cm.Quorum.QDeviceVoting = in.AutoFailover, in.AutoFailoverNo, st.QDeviceAlive
	for _, id := range order {
		n := nodes[id]
		n.Maintenance = maint[n.Key]
		n.Coordinator = n.Key != "" && n.Key == in.Coordinator
		cm.Nodes = append(cm.Nodes, *n)
	}

	for _, g := range gs {
		mg := MapGroup{Name: g.Name, Topology: g.Topology, Owner: g.Owner, Epoch: g.Epoch, Candidates: g.Candidates,
			Serving: g.Role == groups.RoleOwner && g.CanWrite, Problems: g.Problems, Failover: g.Failover.Reason}
		if mg.Problems == nil {
			mg.Problems = []string{}
		}
		for _, p := range g.Pools {
			mg.Pools = append(mg.Pools, p.Name)
		}
		if g.Topology == groups.Replicated {
			mg.IntervalSecs = g.IntervalSecs
			for _, r := range g.Replication {
				if r.Direction != "out" {
					continue
				}
				mr := MapReplication{Pool: r.Pool, Target: r.Peer, LastOK: r.LastOKAt, LagSecs: -1, Error: r.LastError}
				if r.LastOKAt != nil {
					mr.LagSecs = int64(now.Sub(*r.LastOKAt).Seconds())
				}
				mg.Replication = append(mg.Replication, mr)
			}
		}
		cm.Groups = append(cm.Groups, mg)
	}
	sort.SliceStable(cm.Nodes, func(i, j int) bool {
		if cm.Nodes[i].Self != cm.Nodes[j].Self {
			return cm.Nodes[i].Self
		}
		if cm.Nodes[i].Voter != cm.Nodes[j].Voter {
			return !cm.Nodes[i].Voter
		}
		return cm.Nodes[i].Name < cm.Nodes[j].Name
	})
	return cm
}

// Map: GET /api/cluster/map
func (h *ClusterHandler) Map(w http.ResponseWriter, r *http.Request) {
	self, err := configstore.NodeID(h.db)
	if err != nil {
		respondErrorSimple(w, "Failed to read the node id: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sync, err := configstore.Status(h.db)
	if err != nil {
		respondErrorSimple(w, "Failed to read the sync status: "+err.Error(), http.StatusInternalServerError)
		return
	}
	gs, err := h.mgr.Statuses()
	if err != nil {
		respondErrorSimple(w, "Failed to read storage groups: "+err.Error(), http.StatusInternalServerError)
		return
	}
	proposals, err := configstore.Proposals(h.db)
	if err != nil {
		respondErrorSimple(w, "Failed to read proposals: "+err.Error(), http.StatusInternalServerError)
		return
	}
	cfg, st, _, _ := h.mon.Snapshot()
	cm := buildMap(self, configstore.NodeName(), sync, cfg, st, h.mon.Info(), gs, h.mgr.Maintenances(), len(proposals), time.Now())
	respondOK(w, map[string]any{"success": true, "map": cm})
}

// GetMaintenance: GET /api/cluster/maintenance
func (h *ClusterHandler) GetMaintenance(w http.ResponseWriter, r *http.Request) {
	mt, err := groups.GetMaintenance(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "maintenance": mt})
}

// EnterMaintenance: POST /api/cluster/maintenance {reason}
func (h *ClusterHandler) EnterMaintenance(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if len(b.Reason) > 200 {
		respondErrorSimple(w, "reason: at most 200 characters", http.StatusBadRequest)
		return
	}
	moves, err := h.mgr.EnterMaintenance(b.Reason, r.Header.Get("X-User"))
	if moves == nil {
		moves = []groups.MaintenanceMove{}
	}
	if err != nil {
		// Maintenance is on when only a move failed; off when nothing changed.
		mt, _ := groups.GetMaintenance(h.db)
		respondOK(w, map[string]any{"success": false, "error": err.Error(), "moves": moves, "maintenance": mt})
		return
	}
	mt, _ := groups.GetMaintenance(h.db)
	respondOK(w, map[string]any{"success": true, "moves": moves, "maintenance": mt})
}

// ExitMaintenance: DELETE /api/cluster/maintenance
func (h *ClusterHandler) ExitMaintenance(w http.ResponseWriter, r *http.Request) {
	if err := h.mgr.ExitMaintenance(r.Header.Get("X-User")); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// TestResult is the outcome of a safety test.
type TestResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	// Checks are per-target results (replication).
	Checks []groups.ReplicationCheck `json:"checks,omitempty"`
}

// qdeviceDial is replaced in tests.
var qdeviceDial = func(addr string) error {
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err == nil {
		c.Close()
	}
	return err
}

func testQDevice(cfg *quorum.Config, st quorum.Status) TestResult {
	if cfg == nil {
		return TestResult{Detail: "this node is not in a cluster"}
	}
	if cfg.QDevice == nil {
		return TestResult{Detail: "the cluster has no QDevice (a voter or third node is tested by the member list)"}
	}
	addr := net.JoinHostPort(cfg.QDevice.Host, "5403")
	if err := qdeviceDial(addr); err != nil {
		return TestResult{Detail: fmt.Sprintf("this node cannot reach the QDevice at %s: %v. Open TCP 5403 on it and check the route", addr, err)}
	}
	if !st.QDeviceAlive {
		return TestResult{Detail: fmt.Sprintf("%s answers on TCP 5403, but it does not vote for this node now (certificate, cluster name or corosync-qdevice service: see the logs of corosync-qdevice)", addr)}
	}
	return TestResult{OK: true, Detail: fmt.Sprintf("%s answers on TCP 5403 and votes for this node", addr)}
}

// Test: POST /api/cluster/test/{kind} {group}
// kind: qdevice, fencing (IPMI), pdu, replication. None changes anything;
// SCSI reservations are tested by /api/ha/scsi/probe.
func (h *ClusterHandler) Test(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Group string `json:"group"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
			return
		}
	}
	var res TestResult
	switch mux.Vars(r)["kind"] {
	case "qdevice":
		cfg, st, _, _ := h.mon.Snapshot()
		res = testQDevice(cfg, st)
	case "fencing":
		cfg, err := ha.GetFencingConfig(h.db)
		if err != nil {
			respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
			return
		}
		answer, err := ha.TestFencing(cfg)
		if err != nil {
			res.Detail = err.Error()
		} else {
			res.OK, res.Detail = true, "the BMC answers and accepts the credentials: "+answer
			if !cfg.Enable {
				res.Detail += " (power fencing is configured but switched off)"
			}
		}
	case "pdu":
		cfg, err := ha.GetPDUConfig(h.db)
		if err != nil {
			respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
			return
		}
		detail, err := ha.TestPDU(cfg)
		if err != nil {
			res.Detail = err.Error()
		} else {
			res.OK, res.Detail = true, detail
		}
	case "replication":
		if b.Group == "" {
			respondErrorSimple(w, "group is required", http.StatusBadRequest)
			return
		}
		checks, err := h.mgr.TestReplication(b.Group)
		if errors.Is(err, sql.ErrNoRows) {
			respondErrorSimple(w, "unknown group", http.StatusNotFound)
			return
		}
		if err != nil {
			res.Detail = err.Error()
			break
		}
		res.Checks, res.OK = checks, len(checks) > 0
		failed := 0
		for _, c := range checks {
			if !c.OK {
				res.OK = false
				failed++
			}
		}
		switch {
		case len(checks) == 0:
			res.Detail = "the group has no other candidate"
		case failed == 0:
			res.Detail = "every copy is reachable"
		default:
			res.Detail = fmt.Sprintf("%d of %d copies have a problem", failed, len(checks))
		}
	default:
		respondErrorSimple(w, "unknown test", http.StatusNotFound)
		return
	}
	respondOK(w, map[string]any{"success": true, "result": res})
}
