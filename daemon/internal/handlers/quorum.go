package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dplaned/internal/configstore"
	"dplaned/internal/quorum"
)

// QuorumHandler serves cluster quorum and the third vote (Design 0001 phase 3a).
type QuorumHandler struct {
	db  *sql.DB
	mon *quorum.Monitor
}

func NewQuorumHandler(db *sql.DB, mon *quorum.Monitor) *QuorumHandler {
	return &QuorumHandler{db: db, mon: mon}
}

// push delivers a cluster update to another member over the paired-node channel.
func (h *QuorumHandler) push(n quorum.Node, u quorum.Update) error {
	if n.NodeKey == "" {
		return errors.New("node " + n.Name + " is not a paired node")
	}
	return configstore.CallPeer(h.db, n.NodeKey, "POST", "/api/config/sync/peer/cluster", u, nil)
}

func (h *QuorumHandler) self() (string, error) { return configstore.NodeID(h.db) }

// Status: GET /api/quorum/status
func (h *QuorumHandler) Status(w http.ResponseWriter, r *http.Request) {
	h.mon.Refresh()
	cfg, st, errMsg, when := h.mon.Snapshot()
	clusters, _ := quorum.WitnessClusters(h.db)
	respondOK(w, map[string]any{
		"success":    true,
		"configured": cfg != nil,
		"cluster":    cfg,
		"status":     st,
		"info":       h.mon.Info(),
		"error":      errMsg,
		"checked_at": when,
		"witness": map[string]any{
			"active":   quorum.WitnessActive(),
			"clusters": clusters,
		},
	})
}

// Suggest proposes the cluster addresses for pairing with a paired node.
// GET /api/quorum/suggest?peer_id=
func (h *QuorumHandler) Suggest(w http.ResponseWriter, r *http.Request) {
	peers, err := configstore.Peers(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := r.URL.Query().Get("peer_id")
	for _, p := range peers {
		if p.ID != id {
			continue
		}
		u, _ := url.Parse(p.URL)
		out := map[string]any{"success": true, "peer_name": p.Name, "local_name": configstore.NodeName()}
		if u != nil {
			if ips, err := net.LookupIP(u.Hostname()); err == nil && len(ips) > 0 {
				out["peer_addr"] = ips[0].String()
			}
			if a, err := quorum.SourceAddrTowards(u.Hostname()); err == nil {
				out["local_addr"] = a
			}
		}
		respondOK(w, out)
		return
	}
	respondErrorSimple(w, "Unknown paired node", http.StatusNotFound)
}

// Form: POST /api/quorum/cluster {peer_id, local_addr, peer_addr}
func (h *QuorumHandler) Form(w http.ResponseWriter, r *http.Request) {
	var b struct {
		PeerID    string `json:"peer_id"`
		LocalAddr string `json:"local_addr"`
		PeerAddr  string `json:"peer_addr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.PeerID == "" {
		respondErrorSimple(w, "peer_id, local_addr and peer_addr are required", http.StatusBadRequest)
		return
	}
	self, err := h.self()
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var peerName string
	peers, _ := configstore.Peers(h.db)
	for _, p := range peers {
		if p.ID == b.PeerID {
			peerName = p.Name
		}
	}
	if peerName == "" {
		respondErrorSimple(w, "Pair the nodes first (System > Configuration Sync)", http.StatusBadRequest)
		return
	}
	cfg, err := quorum.Form(h.db,
		quorum.Node{Name: clusterNodeName(configstore.NodeName()), Addr: b.LocalAddr, NodeKey: self},
		quorum.Node{Name: clusterNodeName(peerName), Addr: b.PeerAddr, NodeKey: b.PeerID},
		h.push)
	h.mon.Refresh()
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error(), "cluster": cfg})
		return
	}
	respondOK(w, map[string]any{"success": true, "cluster": cfg})
}

// clusterNodeName makes a hostname usable as a corosync node name.
func clusterNodeName(s string) string {
	s = strings.Split(s, ".")[0]
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "node"
	}
	if b.Len() > 32 {
		return b.String()[:32]
	}
	return b.String()
}

// Dissolve: DELETE /api/quorum/cluster
func (h *QuorumHandler) Dissolve(w http.ResponseWriter, r *http.Request) {
	self, _ := h.self()
	err := quorum.Dissolve(h.db, self, h.push)
	h.mon.Refresh()
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// ThirdVoteCode: POST /api/quorum/third-vote/code
func (h *QuorumHandler) ThirdVoteCode(w http.ResponseWriter, r *http.Request) {
	code, exp, err := quorum.CreateEnrollCode(h.db)
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true, "code": code, "expires_at": exp})
}

// RemoveThirdVote: DELETE /api/quorum/third-vote
func (h *QuorumHandler) RemoveThirdVote(w http.ResponseWriter, r *http.Request) {
	self, _ := h.self()
	err := quorum.RemoveThirdVote(h.db, self, h.push)
	h.mon.Refresh()
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// BecomeWitness makes this node the third vote of another cluster.
// POST /api/quorum/witness/join {node_url, code}
func (h *QuorumHandler) BecomeWitness(w http.ResponseWriter, r *http.Request) {
	var b struct {
		NodeURL string `json:"node_url"`
		Code    string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.NodeURL == "" || b.Code == "" {
		respondErrorSimple(w, "node_url and code are required", http.StatusBadRequest)
		return
	}
	cluster, err := quorum.JoinCluster(b.NodeURL, b.Code, func(cluster string, csr []byte) ([]byte, error) {
		return quorum.WitnessSign(h.db, cluster, b.NodeURL, csr)
	})
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true, "cluster_name": cluster})
}

// ── Public: enrollment with a one-time code, and the setup script ─────────────

// WitnessScript: GET /api/quorum/witness-setup.sh
func (h *QuorumHandler) WitnessScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = io.WriteString(w, quorum.WitnessScript)
}

func textError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, msg+"\n")
}

func enrollStatus(err error) int {
	if errors.Is(err, quorum.ErrInvalidCode) {
		return http.StatusUnauthorized
	}
	return http.StatusBadRequest
}

// EnrollCA: POST /api/quorum/enroll/ca (code header, body: CA PEM)
func (h *QuorumHandler) EnrollCA(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	cluster, csr, err := quorum.EnrollCA(h.db, r.Header.Get(quorum.HdrCode), body)
	if err != nil {
		textError(w, err.Error(), enrollStatus(err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, quorum.FormatCAResponse(cluster, csr))
}

// EnrollCert: POST /api/quorum/enroll/cert?address= (code header, body: certificate PEM)
func (h *QuorumHandler) EnrollCert(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	self, _ := h.self()
	cfg, err := quorum.EnrollCert(h.db, r.Header.Get(quorum.HdrCode), body, r.URL.Query().Get("address"), self, h.push)
	h.mon.Refresh()
	if err != nil {
		textError(w, err.Error(), enrollStatus(err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok "+cfg.ClusterName+"\n")
}

// ── Peer endpoint (paired-node channel) ───────────────────────────────────────

// PeerCluster: POST /api/config/sync/peer/cluster — the member that changed
// the cluster sends the new configuration.
func (h *QuorumHandler) PeerCluster(w http.ResponseWriter, r *http.Request) {
	if _, err := configstore.AuthenticatePeer(h.db, r.Header.Get("X-DPlane-Node"), r.Header.Get("X-DPlane-Peer-Secret")); err != nil {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var u quorum.Update
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&u); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	err := quorum.Receive(h.db, u)
	go func() { time.Sleep(time.Second); h.mon.Refresh() }()
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true})
}
