package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"dplaned/internal/cmdutil"
	"dplaned/internal/configstore"
	"dplaned/internal/fleet"
	"dplaned/internal/gitops"
	"dplaned/internal/gitsync"
	"dplaned/internal/groups"
	"dplaned/internal/quorum"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  FLEET OVERLAY  (Design 0001 phase 6, section 5.7)
//
//  On the fleet manager (any node with the role switched on):
//    GET    /api/fleet                              settings, remotes, fleet resources, rollouts
//    PUT    /api/fleet/settings          {enabled, name}
//    POST   /api/fleet/remotes           {name, url, token, labels}
//    PUT    /api/fleet/remotes/{id}/labels {labels}
//    DELETE /api/fleet/remotes/{id}
//    POST   /api/fleet/poll                         refresh the inventory now
//    PUT    /api/fleet/resources         {kind, key, payload}   (payload null: delete on the remotes)
//    DELETE /api/fleet/resources/{kind}/{key}       stop managing it fleet-wide
//    GET    /api/fleet/local/{kind}/{key}           this node's version, as a starting point
//    POST   /api/fleet/rollouts          {name, resources[], waves[], soak_secs, auto_advance}
//    POST   /api/fleet/rollouts/{id}/{continue|pause|cancel}
//
//  On every node (called by the manager with this node's API token):
//    GET    /api/fleet/node-status                  inventory report (records the manager's contact)
//    DELETE /api/fleet/membership                   leave the fleet (after being removed from it)
//    POST   /api/fleet/apply             {fleet, manager, rollout, resources[]}
// ═══════════════════════════════════════════════════════════════════════════════

// FleetHandler serves both sides.
type FleetHandler struct {
	db  *sql.DB
	mgr *fleet.Manager
	mon *quorum.Monitor
	ctx gitops.ApplyContext
}

func NewFleetHandler(db *sql.DB, mgr *fleet.Manager, mon *quorum.Monitor, ctx gitops.ApplyContext) *FleetHandler {
	return &FleetHandler{db: db, mgr: mgr, mon: mon, ctx: ctx}
}

// Overview: GET /api/fleet
func (h *FleetHandler) Overview(w http.ResponseWriter, r *http.Request) {
	s, err := fleet.GetSettings(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	member, err := configstore.FleetMembership(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := map[string]any{"success": true, "settings": s, "membership": member,
		"remotes": []fleet.Remote{}, "resources": []fleet.StoredResource{}, "rollouts": []fleet.Rollout{}}
	if s.Enabled {
		if resp["remotes"], err = fleet.Remotes(h.db); err == nil {
			if resp["resources"], err = fleet.Resources(h.db); err == nil {
				resp["rollouts"], err = fleet.Rollouts(h.db, 20)
			}
		}
		if err != nil {
			respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	respondOK(w, resp)
}

// SaveSettings: PUT /api/fleet/settings
func (h *FleetHandler) SaveSettings(w http.ResponseWriter, r *http.Request) {
	var s fleet.Settings
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	s.Name = strings.TrimSpace(s.Name)
	if err := fleet.SaveSettings(h.db, s); err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	h.mgr.Trigger()
	respondOK(w, map[string]any{"success": true})
}

func (h *FleetHandler) managerOnly(w http.ResponseWriter) bool {
	s, err := fleet.GetSettings(h.db)
	if err != nil || !s.Enabled {
		respondOK(w, map[string]any{"success": false, "error": "this node is not the fleet manager: switch the role on first"})
		return false
	}
	return true
}

// AddRemote: POST /api/fleet/remotes
func (h *FleetHandler) AddRemote(w http.ResponseWriter, r *http.Request) {
	if !h.managerOnly(w) {
		return
	}
	var b struct {
		Name   string            `json:"name"`
		URL    string            `json:"url"`
		Token  string            `json:"token"`
		Labels map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if b.Labels == nil {
		b.Labels = map[string]string{}
	}
	rm, err := fleet.AddRemote(h.db, strings.TrimSpace(b.Name), strings.TrimRight(strings.TrimSpace(b.URL), "/"), strings.TrimSpace(b.Token), b.Labels)
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true, "remote": rm,
		"fingerprint": configstore.FormatFingerprint(rm.Fingerprint)})
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	return id, err == nil
}

// SetLabels: PUT /api/fleet/remotes/{id}/labels
func (h *FleetHandler) SetLabels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	var b struct {
		Labels map[string]string `json:"labels"`
	}
	if !ok || json.NewDecoder(r.Body).Decode(&b) != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if b.Labels == nil {
		b.Labels = map[string]string{}
	}
	if err := fleet.SetLabels(h.db, id, b.Labels); err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// RemoveRemote: DELETE /api/fleet/remotes/{id}
func (h *FleetHandler) RemoveRemote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		respondErrorSimple(w, "Invalid id", http.StatusBadRequest)
		return
	}
	if err := fleet.RemoveRemote(h.db, id); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// Poll: POST /api/fleet/poll
func (h *FleetHandler) Poll(w http.ResponseWriter, r *http.Request) {
	if !h.managerOnly(w) {
		return
	}
	h.mgr.PollNow()
	remotes, err := fleet.Remotes(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "remotes": remotes})
}

// PutResource: PUT /api/fleet/resources
func (h *FleetHandler) PutResource(w http.ResponseWriter, r *http.Request) {
	if !h.managerOnly(w) {
		return
	}
	var b fleet.Resource
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := validFleetPayload(b); err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	if err := fleet.PutResource(h.db, b.Kind, b.Key, b.Payload, r.Header.Get("X-User")); err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// validFleetPayload checks a fleet resource the way the remotes will: it must
// describe exactly that resource and pass state.yaml validation (no
// plain-text passwords).
func validFleetPayload(b fleet.Resource) error {
	if b.Payload == nil {
		return nil
	}
	res := configstore.Resource{Kind: b.Kind, Key: b.Key, Scope: configstore.ScopeCluster, ScopeID: "local", Payload: b.Payload}
	ds, err := configstore.Assemble([]configstore.Resource{res})
	if err != nil {
		return err
	}
	ds.Version = "1"
	if errs := gitops.ValidState(ds); len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	back := configstore.Extract(ds, configstore.NodeName())
	for _, x := range back {
		if x.Kind == b.Kind && x.Key == b.Key {
			return nil
		}
	}
	return errors.New("the payload does not describe " + b.Kind + " " + b.Key + " (its name must match the key)")
}

// LocalResource: GET /api/fleet/local/{kind}/{key} - this node's current
// version of a resource (without secrets), to start a fleet resource from.
func (h *FleetHandler) LocalResource(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	if !fleet.FleetKinds[v["kind"]] {
		respondErrorSimple(w, "not a fleet kind", http.StatusBadRequest)
		return
	}
	latest, err := configstore.Latest(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rev, ok := latest[v["kind"]+"/"+v["key"]]
	if !ok || rev.Payload == nil {
		respondOK(w, map[string]any{"success": false, "error": "this node has no " + v["kind"] + " " + v["key"]})
		return
	}
	p := map[string]any{}
	for k, val := range rev.Payload {
		if k != "credentials" && k != "password_hash" {
			p[k] = val
		}
	}
	respondOK(w, map[string]any{"success": true, "payload": p})
}

// LeaveFleet: DELETE /api/fleet/membership - this node was removed from its
// fleet: stop expecting the manager. What the fleet applied stays.
func (h *FleetHandler) LeaveFleet(w http.ResponseWriter, r *http.Request) {
	if err := configstore.LeaveFleet(h.db); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// ForgetResource: DELETE /api/fleet/resources/{kind}/{key}
func (h *FleetHandler) ForgetResource(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	if err := fleet.ForgetResource(h.db, v["kind"], v["key"]); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// CreateRollout: POST /api/fleet/rollouts
func (h *FleetHandler) CreateRollout(w http.ResponseWriter, r *http.Request) {
	if !h.managerOnly(w) {
		return
	}
	var b struct {
		Name      string `json:"name"`
		Resources []struct {
			Kind string `json:"kind"`
			Key  string `json:"key"`
		} `json:"resources"`
		Waves       []fleet.Wave `json:"waves"`
		SoakSecs    int          `json:"soak_secs"`
		AutoAdvance bool         `json:"auto_advance"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	all, err := fleet.Resources(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var chosen []fleet.Resource
	for _, want := range b.Resources {
		found := false
		for _, s := range all {
			if s.Kind == want.Kind && s.Key == want.Key {
				chosen, found = append(chosen, s.Resource), true
			}
		}
		if !found {
			respondOK(w, map[string]any{"success": false, "error": "unknown fleet resource " + want.Kind + "/" + want.Key})
			return
		}
	}
	id, err := fleet.CreateRollout(h.db, strings.TrimSpace(b.Name), chosen, b.Waves, b.SoakSecs, b.AutoAdvance, r.Header.Get("X-User"))
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	h.mgr.Trigger()
	respondOK(w, map[string]any{"success": true, "id": id})
}

// RolloutAction: POST /api/fleet/rollouts/{id}/{action}
func (h *FleetHandler) RolloutAction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		respondErrorSimple(w, "Invalid id", http.StatusBadRequest)
		return
	}
	var err error
	switch mux.Vars(r)["action"] {
	case "continue":
		err = fleet.Continue(h.db, id)
	case "pause":
		err = fleet.SetRolloutState(h.db, id, fleet.RolloutPaused, "paused by "+r.Header.Get("X-User"))
	case "cancel":
		err = fleet.SetRolloutState(h.db, id, fleet.RolloutCancelled, "cancelled by "+r.Header.Get("X-User"))
	default:
		respondErrorSimple(w, "unknown action", http.StatusNotFound)
		return
	}
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	h.mgr.Trigger()
	respondOK(w, map[string]any{"success": true})
}

// ── Remote side ───────────────────────────────────────────────────────────────

// NodeStatus: GET /api/fleet/node-status - this node's inventory report.
func (h *FleetHandler) NodeStatus(w http.ResponseWriter, r *http.Request) {
	if f := r.Header.Get(fleet.HeaderFleet); f != "" {
		if err := configstore.FleetContact(h.db, f, r.Header.Get(fleet.HeaderManager)); err != nil {
			log.Printf("FLEET: recording the manager's contact: %v", err)
		}
	}
	st := fleet.NodeStatus{Name: configstore.NodeName(), Version: DaemonVersion, Pools: []fleet.PoolHealth{},
		Groups: []string{}, ReportedAt: time.Now()}
	var err error
	if st.NodeID, err = configstore.NodeID(h.db); err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	if sync, err := configstore.Status(h.db); err == nil {
		st.Mode, st.Conflicts = sync.Mode, sync.Conflicts
	}
	if ps, err := configstore.Proposals(h.db); err == nil {
		st.Proposals = len(ps)
	}
	if hs, err := gitsync.Holds(h.db); err == nil {
		st.Holds = len(hs)
	}
	if mt, err := groups.GetMaintenance(h.db); err == nil {
		st.Maintenance = mt.Enabled
	}
	if m, err := configstore.FleetMembership(h.db); err == nil {
		st.Fleet = m.Fleet
	}
	if h.mon != nil {
		if cfg, _, _, _ := h.mon.Snapshot(); cfg != nil {
			st.Cluster = cfg.ClusterName
		}
		in := h.mon.Info()
		st.Quorate, st.Coordinator = in.Quorate, in.Coordinator != "" && in.Coordinator == st.NodeID
	}
	if gs, err := groups.List(h.db); err == nil {
		for _, g := range gs {
			if g.Owner == st.NodeID {
				st.Groups = append(st.Groups, g.Name)
			}
		}
	}
	if out, err := cmdutil.RunFast("zpool_list", "list", "-H", "-o", "name,size,alloc,free,cap,health"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if f := strings.Fields(line); len(f) == 6 {
				st.Pools = append(st.Pools, fleet.PoolHealth{Name: f[0], Health: f[5]})
			}
		}
	}
	respondOK(w, map[string]any{"success": true, "status": st})
}

// Apply: POST /api/fleet/apply - fleet resources from the fleet manager.
func (h *FleetHandler) Apply(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Fleet     string           `json:"fleet"`
		Manager   string           `json:"manager"`
		Rollout   string           `json:"rollout"`
		Resources []fleet.Resource `json:"resources"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&b); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	var rs []configstore.Resource
	for _, x := range b.Resources {
		if err := validFleetPayload(x); err != nil {
			respondOK(w, map[string]any{"success": false, "error": x.Kind + "/" + x.Key + ": " + err.Error()})
			return
		}
		rs = append(rs, configstore.Resource{Kind: x.Kind, Key: x.Key, Payload: x.Payload})
	}
	results, err := configstore.ApplyOverlay(h.db, h.ctx, b.Fleet, b.Manager, b.Rollout, rs)
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error(), "results": results})
		return
	}
	respondOK(w, map[string]any{"success": true, "results": results})
}
