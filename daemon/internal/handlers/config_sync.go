package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"dplaned/internal/configstore"
	"dplaned/internal/gitops"
)

// ConfigSyncHandler serves revision exchange between nodes and the merge
// review (Design 0001, Phase 2).
type ConfigSyncHandler struct {
	db *sql.DB
	s  *configstore.Syncer
}

func NewConfigSyncHandler(db *sql.DB, s *configstore.Syncer) *ConfigSyncHandler {
	return &ConfigSyncHandler{db: db, s: s}
}

func syncError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, configstore.ErrBusy):
		respondErrorSimple(w, "Another apply or rollback is in progress; try again shortly", http.StatusLocked)
	case errors.Is(err, gitops.ErrNotWriter):
		respondErrorSimple(w, err.Error(), http.StatusConflict)
	case errors.Is(err, configstore.ErrSameNode), errors.Is(err, configstore.ErrInvalidToken):
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, configstore.ErrUnknownPeer):
		respondErrorSimple(w, err.Error(), http.StatusNotFound)
	default:
		respondErrorSimple(w, err.Error(), http.StatusBadGateway)
	}
}

// ── GUI (session) ─────────────────────────────────────────────────────────────

// Status: GET /api/config/sync/status
func (h *ConfigSyncHandler) Status(w http.ResponseWriter, r *http.Request) {
	st, err := configstore.Status(h.db)
	if err != nil {
		respondErrorSimple(w, "Failed to read sync status: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "status": st})
}

// SyncNow: POST /api/config/sync/now
func (h *ConfigSyncHandler) SyncNow(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Round(true); err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// CreateToken: POST /api/config/peers/token
func (h *ConfigSyncHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	token, expires, err := configstore.CreateJoinToken(h.db)
	if err != nil {
		respondErrorSimple(w, "Failed to create a join token: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "token": token, "expires_at": expires})
}

type joinBody struct {
	URL     string `json:"url"`
	Token   string `json:"token"`
	SelfURL string `json:"self_url"`
}

// Preview: POST /api/config/peers/preview {url, token}
func (h *ConfigSyncHandler) Preview(w http.ResponseWriter, r *http.Request) {
	var b joinBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.URL == "" || b.Token == "" {
		respondErrorSimple(w, "url and token are required", http.StatusBadRequest)
		return
	}
	pv, err := h.s.Preview(b.URL, b.Token)
	if err != nil {
		syncError(w, err)
		return
	}
	respondOK(w, map[string]any{"success": true, "preview": pv})
}

// Join: POST /api/config/peers/join {url, token, self_url}
func (h *ConfigSyncHandler) Join(w http.ResponseWriter, r *http.Request) {
	var b joinBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.URL == "" || b.Token == "" || b.SelfURL == "" {
		respondErrorSimple(w, "url, token and self_url are required", http.StatusBadRequest)
		return
	}
	p, err := h.s.Join(b.URL, b.Token, b.SelfURL)
	if err != nil {
		syncError(w, err)
		return
	}
	respondOK(w, map[string]any{"success": true, "peer": p})
}

// RemovePeer: DELETE /api/config/peers/{id}
func (h *ConfigSyncHandler) RemovePeer(w http.ResponseWriter, r *http.Request) {
	if err := h.s.RemovePeer(mux.Vars(r)["id"]); err != nil {
		syncError(w, err)
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// Detach: POST /api/config/detach
func (h *ConfigSyncHandler) Detach(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Detach(); err != nil {
		respondErrorSimple(w, "Detach failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// Conflicts: GET /api/config/conflicts
func (h *ConfigSyncHandler) Conflicts(w http.ResponseWriter, r *http.Request) {
	conflicts, pending, err := configstore.Conflicts(h.db)
	if err != nil {
		respondErrorSimple(w, "Failed to read conflicts: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "conflicts": conflicts, "pending": pending})
}

// Resolve: POST /api/config/conflicts/{id}/resolve {choice, payload}
func (h *ConfigSyncHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		respondErrorSimple(w, "Invalid conflict id", http.StatusBadRequest)
		return
	}
	var b struct {
		Choice  string         `json:"choice"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := h.s.Resolve(id, b.Choice, b.Payload, r.Header.Get("X-User")); err != nil {
		if errors.Is(err, configstore.ErrBusy) || errors.Is(err, gitops.ErrNotWriter) {
			syncError(w, err)
			return
		}
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	h.s.Trigger(false)
	respondOK(w, map[string]any{"success": true})
}

// ── Peers (no session; peer secret or join token) ─────────────────────────────

func (h *ConfigSyncHandler) peer(w http.ResponseWriter, r *http.Request) *configstore.Peer {
	p, err := configstore.AuthenticatePeer(h.db, r.Header.Get("X-DPlane-Node"), r.Header.Get("X-DPlane-Peer-Secret"))
	if err != nil {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return nil
	}
	return p
}

// PeerRevisions: GET /api/config/sync/peer/revisions?since=&limit=
// A paired peer, or a joining node with a valid join token (merge preview).
func (h *ConfigSyncHandler) PeerRevisions(w http.ResponseWriter, r *http.Request) {
	var p *configstore.Peer
	if tok := r.Header.Get("X-DPlane-Join-Token"); tok != "" {
		if !configstore.ValidJoinToken(h.db, tok) {
			respondErrorSimple(w, configstore.ErrInvalidToken.Error(), http.StatusUnauthorized)
			return
		}
	} else if p = h.peer(w, r); p == nil {
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	resp, err := configstore.ServeRevisions(h.db, p, since, limit)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, resp)
}

// PeerJoin: POST /api/config/sync/peer/join (join token)
func (h *ConfigSyncHandler) PeerJoin(w http.ResponseWriter, r *http.Request) {
	var req configstore.JoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	resp, err := configstore.AcceptJoin(h.db, r.Header.Get("X-DPlane-Join-Token"), req)
	if err != nil {
		if errors.Is(err, configstore.ErrInvalidToken) {
			respondErrorSimple(w, err.Error(), http.StatusUnauthorized)
			return
		}
		syncError(w, err)
		return
	}
	h.s.Trigger(true)
	respondOK(w, resp)
}

// PeerNotify: POST /api/config/sync/peer/notify — the peer has new revisions.
func (h *ConfigSyncHandler) PeerNotify(w http.ResponseWriter, r *http.Request) {
	if h.peer(w, r) == nil {
		return
	}
	h.s.Trigger(false)
	respondOK(w, map[string]any{"success": true})
}

// PeerLeave: POST /api/config/sync/peer/leave — the peer detached or removed us.
func (h *ConfigSyncHandler) PeerLeave(w http.ResponseWriter, r *http.Request) {
	p := h.peer(w, r)
	if p == nil {
		return
	}
	if err := configstore.PeerLeft(h.db, p.ID); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true})
}
