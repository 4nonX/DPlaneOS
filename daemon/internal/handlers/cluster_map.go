package handlers

import (
	"database/sql"
	"net/http"
	"sort"
	"time"

	"dplaned/internal/configstore"
	"dplaned/internal/groups"
	"dplaned/internal/ha"
	"dplaned/internal/quorum"
)

// The cluster map (Design 0001, 5.10): the nodes, their votes and state,
// the storage groups with owner and epoch, replication lag and what is
// wrong, in one answer, as this node sees it. It is read-only: each thing
// is changed where it is configured.

// MapNode is one machine on the map.
type MapNode struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Self  bool   `json:"self"`
	Addr  string `json:"addr,omitempty"`
	Voter bool   `json:"voter"` // votes only (a Raspberry Pi, a mini PC): holds no storage
	// State: online (in this node's partition), away (a cluster member that
	// is not), unreachable (paired, not answering), paired (answering, no
	// cluster formed).
	State       string     `json:"state"`
	Since       *time.Time `json:"since,omitempty"` // away or unreachable since
	Maintenance bool       `json:"maintenance"`
	// PowerFencing: this node can power that node off before taking over
	// from it ("ok"), has an entry that is switched off ("off"), or none.
	PowerFencing string   `json:"power_fencing,omitempty"`
	Owns         []string `json:"owns"` // storage groups it owns
}

// MapReplication is the state of one copy of a replicated group.
type MapReplication struct {
	Peer       string     `json:"peer"`
	PeerName   string     `json:"peer_name"`
	Pool       string     `json:"pool"`
	Direction  string     `json:"direction"`
	LastOKAt   *time.Time `json:"last_ok_at,omitempty"`
	LagSeconds int64      `json:"lag_seconds"` // since the last successful run; -1: never
	Error      string     `json:"error,omitempty"`
}

// MapGroup is one storage group on the map.
type MapGroup struct {
	Name        string           `json:"name"`
	Topology    string           `json:"topology"`
	Owner       string           `json:"owner"`
	OwnerName   string           `json:"owner_name"`
	Epoch       int64            `json:"epoch"`
	Candidates  []string         `json:"candidates"` // names, in takeover order
	Pools       []string         `json:"pools"`
	Role        string           `json:"role"`    // this node's: owner, standby, other
	Serving     bool             `json:"serving"` // served by this node now
	Problems    []string         `json:"problems"`
	Failover    groups.Decision  `json:"failover"`
	Replication []MapReplication `json:"replication"`
}

// ClusterMapHandler serves the map.
type ClusterMapHandler struct {
	db  *sql.DB
	mon *quorum.Monitor
	mgr *groups.Manager
}

func NewClusterMapHandler(db *sql.DB, mon *quorum.Monitor, mgr *groups.Manager) *ClusterMapHandler {
	return &ClusterMapHandler{db: db, mon: mon, mgr: mgr}
}

// Get: GET /api/ha/map
func (h *ClusterMapHandler) Get(w http.ResponseWriter, r *http.Request) {
	self, err := configstore.NodeID(h.db)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read this node's identity", err)
		return
	}
	cfg, st, quorumErr, checked := h.mon.Snapshot()
	info := h.mon.Info()
	peers, _ := configstore.Peers(h.db)
	statuses, err := h.mgr.Statuses()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to evaluate the storage groups", err)
		return
	}
	targets, _ := ha.ListPowerTargets(h.db)
	maint, _ := groups.GetMaintenance(h.db)
	sync, _ := configstore.Status(h.db)

	m := buildClusterMap(mapInput{
		Self: self, SelfName: configstore.NodeName(), Now: time.Now(),
		Cfg: cfg, Status: st, Online: info.Online, Peers: peers, Groups: statuses, Targets: targets,
		SelfMaintenance: maint.Active, PeersInMaintenance: h.mgr.PeersInMaintenance(),
	})
	out := map[string]any{
		"success":     true,
		"self":        self,
		"nodes":       m.Nodes,
		"groups":      m.Groups,
		"maintenance": maint,
		"quorum": map[string]any{
			"configured":           cfg != nil,
			"quorate":              info.Quorate,
			"expected_votes":       info.ExpectedVotes,
			"total_votes":          st.TotalVotes,
			"auto_failover":        info.AutoFailover,
			"auto_failover_reason": info.AutoFailoverNo,
			"error":                quorumErr,
			"checked_at":           checked,
		},
		"third_vote": m.ThirdVote,
	}
	if sync != nil {
		out["sync"] = map[string]any{"mode": sync.Mode, "since": sync.Since, "waiting": sync.Waiting, "conflicts": sync.Conflicts}
	}
	respondOK(w, out)
}

type mapInput struct {
	Self, SelfName     string
	Now                time.Time
	Cfg                *quorum.Config
	Status             quorum.Status
	Online             []string // node keys in this node's partition
	Peers              []configstore.Peer
	Groups             []groups.GroupStatus
	Targets            []ha.PowerTarget
	SelfMaintenance    bool
	PeersInMaintenance []string
}

type clusterMap struct {
	Nodes     []MapNode
	Groups    []MapGroup
	ThirdVote map[string]any
}

// buildClusterMap puts the pieces together. No I/O: tested as it is.
func buildClusterMap(in mapInput) clusterMap {
	names := map[string]string{in.Self: in.SelfName}
	peerOf := map[string]configstore.Peer{}
	for _, p := range in.Peers {
		names[p.ID] = p.Name
		peerOf[p.ID] = p
	}
	nameOf := func(key string) string {
		if n := names[key]; n != "" {
			return n
		}
		return key
	}
	online := map[string]bool{}
	for _, k := range in.Online {
		online[k] = true
	}
	maint := map[string]bool{in.Self: in.SelfMaintenance}
	for _, k := range in.PeersInMaintenance {
		maint[k] = true
	}
	power := map[string]string{}
	for _, t := range in.Targets {
		power[t.Node] = "off"
		if t.Enabled {
			power[t.Node] = "ok"
		}
	}
	owns := map[string][]string{}
	for _, g := range in.Groups {
		owns[g.Owner] = append(owns[g.Owner], g.Name)
	}

	var nodes []MapNode
	seen := map[string]bool{}
	add := func(n MapNode) {
		n.Maintenance = maint[n.Key]
		n.Owns = owns[n.Key]
		if n.Owns == nil {
			n.Owns = []string{}
		}
		if !n.Self && !n.Voter {
			n.PowerFencing = power[n.Key]
		}
		seen[n.Key] = true
		nodes = append(nodes, n)
	}
	if in.Cfg != nil {
		present := map[string]bool{}
		for _, m := range in.Status.Members {
			present[m.Name] = true
		}
		for _, cn := range in.Cfg.Nodes {
			key := cn.NodeKey
			if key == "" {
				key = "voter:" + cn.Name // a voter has no node key
			}
			n := MapNode{Key: key, Name: cn.Name, Addr: cn.Addr, Voter: cn.Voter, Self: cn.NodeKey == in.Self}
			switch {
			case n.Self, online[cn.NodeKey], cn.Voter && present[cn.Name]:
				n.State = "online"
			default:
				n.State = "away"
				if p, ok := peerOf[cn.NodeKey]; ok {
					n.Since = p.UnreachableSince
				}
			}
			add(n)
		}
	}
	if !seen[in.Self] {
		add(MapNode{Key: in.Self, Name: in.SelfName, Self: true, State: "online"})
	}
	// Paired nodes that are not cluster members (no cluster, or not added).
	for _, p := range in.Peers {
		if seen[p.ID] {
			continue
		}
		n := MapNode{Key: p.ID, Name: p.Name, State: "paired"}
		if p.UnreachableSince != nil || p.LastContact == nil {
			n.State, n.Since = "unreachable", p.UnreachableSince
		}
		add(n)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Self != nodes[j].Self {
			return nodes[i].Self
		}
		if nodes[i].Voter != nodes[j].Voter {
			return !nodes[i].Voter
		}
		return nodes[i].Name < nodes[j].Name
	})

	gs := []MapGroup{}
	for _, g := range in.Groups {
		mg := MapGroup{Name: g.Name, Topology: g.Topology, Owner: g.Owner, OwnerName: nameOf(g.Owner), Epoch: g.Epoch,
			Role: string(g.Role), Serving: g.Role == groups.RoleOwner && g.CanWrite, Problems: g.Problems,
			Failover: g.Failover, Replication: []MapReplication{}, Candidates: []string{}, Pools: []string{}}
		if mg.Problems == nil {
			mg.Problems = []string{}
		}
		for _, c := range g.Candidates {
			mg.Candidates = append(mg.Candidates, nameOf(c))
		}
		for _, p := range g.Pools {
			mg.Pools = append(mg.Pools, p.Name)
		}
		for _, rs := range g.Replication {
			mr := MapReplication{Peer: rs.Peer, PeerName: nameOf(rs.Peer), Pool: rs.Pool, Direction: rs.Direction,
				LastOKAt: rs.LastOKAt, LagSeconds: -1, Error: rs.LastError}
			if rs.LastOKAt != nil {
				mr.LagSeconds = int64(in.Now.Sub(*rs.LastOKAt).Seconds())
			}
			mg.Replication = append(mg.Replication, mr)
		}
		gs = append(gs, mg)
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].Name < gs[j].Name })

	third := map[string]any{"kind": "none"}
	if in.Cfg != nil {
		switch {
		case in.Cfg.QDevice != nil:
			third = map[string]any{"kind": "qdevice", "host": in.Cfg.QDevice.Host, "voting": in.Status.QDeviceAlive}
		case len(in.Cfg.Nodes) >= 3:
			third = map[string]any{"kind": "member"}
		}
	}
	return clusterMap{Nodes: nodes, Groups: gs, ThirdVote: third}
}
