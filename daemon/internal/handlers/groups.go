package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gorilla/mux"

	"dplaned/internal/configstore"
	"dplaned/internal/groups"
	"dplaned/internal/quorum"
)

// GroupTransport reaches the other cluster members over the paired-node
// channel (Design 0001 phase 3b).
type GroupTransport struct {
	DB *sql.DB
}

func (t GroupTransport) self() string {
	id, _ := configstore.NodeID(t.DB)
	return id
}

// Members are the cluster's nodes (this node alone without a cluster).
func (t GroupTransport) Members() []string {
	cfg, _, err := quorum.Load(t.DB)
	if err != nil || cfg == nil {
		return []string{t.self()}
	}
	out := make([]string, 0, len(cfg.Nodes))
	for _, n := range cfg.Nodes {
		out = append(out, n.NodeKey)
	}
	return out
}

func (t GroupTransport) Push(node string, u groups.Update) error {
	return configstore.CallPeer(t.DB, node, "POST", "/api/config/sync/peer/group", u, nil)
}

func (t GroupTransport) Fetch(node string) ([]groups.Group, error) {
	var resp struct {
		Groups []groups.Group `json:"groups"`
	}
	err := configstore.CallPeer(t.DB, node, "GET", "/api/config/sync/peer/groups", nil, &resp)
	return resp.Groups, err
}

// GroupsHandler serves storage groups.
type GroupsHandler struct {
	db  *sql.DB
	mgr *groups.Manager
}

func NewGroupsHandler(db *sql.DB, mgr *groups.Manager) *GroupsHandler {
	return &GroupsHandler{db: db, mgr: mgr}
}

// List: GET /api/groups
func (h *GroupsHandler) List(w http.ResponseWriter, r *http.Request) {
	st, err := h.mgr.Statuses()
	if err != nil {
		respondErrorSimple(w, "Failed to read storage groups: "+err.Error(), http.StatusInternalServerError)
		return
	}
	self, _ := configstore.NodeID(h.db)
	names := map[string]string{self: configstore.NodeName()}
	var members []map[string]string
	if cfg, _, err := quorum.Load(h.db); err == nil && cfg != nil {
		for _, n := range cfg.Nodes {
			names[n.NodeKey] = n.Name
			members = append(members, map[string]string{"key": n.NodeKey, "name": n.Name})
		}
	}
	if members == nil {
		members = []map[string]string{{"key": self, "name": configstore.NodeName()}}
	}
	imported, _ := groups.DefaultOps.ImportedPools()
	pools := []string{}
	for p := range imported {
		pools = append(pools, p)
	}
	respondOK(w, map[string]any{"success": true, "groups": st, "self": self, "names": names,
		"members": members, "imported_pools": pools})
}

// Create: POST /api/groups {name, topology, pools, candidates}
func (h *GroupsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name       string   `json:"name"`
		Topology   string   `json:"topology"`
		Pools      []string `json:"pools"`
		Candidates []string `json:"candidates"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	g, err := h.mgr.Create(b.Name, b.Topology, b.Pools, b.Candidates)
	if err != nil {
		respondOK(w, map[string]any{"success": g != nil, "error": err.Error(), "group": g})
		return
	}
	respondOK(w, map[string]any{"success": true, "group": g})
}

// Move: POST /api/groups/{name}/move {target}
func (h *GroupsHandler) Move(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Target == "" {
		respondErrorSimple(w, "target is required", http.StatusBadRequest)
		return
	}
	res, err := h.mgr.Move(mux.Vars(r)["name"], b.Target)
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true, "result": res})
}

// Remove: DELETE /api/groups/{name}
func (h *GroupsHandler) Remove(w http.ResponseWriter, r *http.Request) {
	if err := h.mgr.RemoveGroup(mux.Vars(r)["name"]); err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// ── Peer endpoints ────────────────────────────────────────────────────────────

func (h *GroupsHandler) authPeer(w http.ResponseWriter, r *http.Request) bool {
	if _, err := configstore.AuthenticatePeer(h.db, r.Header.Get("X-DPlane-Node"), r.Header.Get("X-DPlane-Peer-Secret")); err != nil {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// PeerGroup: POST /api/config/sync/peer/group — a member changed a group.
// A stale update (lower epoch) is refused with 409.
func (h *GroupsHandler) PeerGroup(w http.ResponseWriter, r *http.Request) {
	if !h.authPeer(w, r) {
		return
	}
	var u groups.Update
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&u); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := h.mgr.Receive(u); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, groups.ErrStale) {
			code = http.StatusConflict
		}
		respondErrorSimple(w, err.Error(), code)
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// PeerGroups: GET /api/config/sync/peer/groups — members pull the groups.
func (h *GroupsHandler) PeerGroups(w http.ResponseWriter, r *http.Request) {
	if !h.authPeer(w, r) {
		return
	}
	gs, err := groups.List(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "groups": gs})
}
