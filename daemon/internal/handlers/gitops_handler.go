package handlers

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"dplaned/internal/configstore"
	"dplaned/internal/gitsync"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  GIT BACKEND  (Design 0001 phase 5, section 5.8)
//
//    GET  /api/gitops/status                    last cycle: remotes, scopes, holds
//    GET  /api/gitops/plan                      what waits per scope (pull, push, conflicts)
//    POST /api/gitops/sync                      run a cycle now
//    GET  /api/gitops/settings                  repository settings
//    PUT  /api/gitops/settings                  save (and send to the paired nodes)
//    POST /api/gitops/approve   {scope, sha}    apply the repository's changes of a scope
//    POST /api/gitops/take      {scope}         replace local-only changes with the repository's
//    POST /api/gitops/holds/{scope}/resume      clear a hold and retry
//    POST /api/gitops/holds/{scope}/revert      write the last good version back, clear the hold
//    GET  /api/gitops/state?scope=              a scope's file in the newest commit
//    POST /api/gitops/webhook                   push notification (public, HMAC/token)
//    POST /api/config/sync/peer/gitops          settings from a paired node (peer secret)
// ═══════════════════════════════════════════════════════════════════════════════

// GitOpsHandler serves the Git backend.
type GitOpsHandler struct {
	db     *sql.DB
	engine *gitsync.Engine
}

func NewGitOpsHandler(db *sql.DB, engine *gitsync.Engine) *GitOpsHandler {
	return &GitOpsHandler{db: db, engine: engine}
}

// Status: GET /api/gitops/status
func (h *GitOpsHandler) Status(w http.ResponseWriter, r *http.Request) {
	holds, err := gitsync.Holds(h.db)
	if err != nil {
		respondErrorSimple(w, "Failed to read holds: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "status": h.engine.Status(), "holds": holds})
}

// Plan: GET /api/gitops/plan - what waits, per scope this node writes.
func (h *GitOpsHandler) Plan(w http.ResponseWriter, r *http.Request) {
	st := h.engine.Status()
	scopes := []gitsync.ScopeStatus{}
	for _, s := range st.Scopes {
		if s.Owner {
			scopes = append(scopes, s)
		}
	}
	respondOK(w, map[string]any{"success": true, "head": st.Head, "scopes": scopes})
}

// SyncNow: POST /api/gitops/sync
func (h *GitOpsHandler) SyncNow(w http.ResponseWriter, r *http.Request) {
	err := h.engine.Run()
	resp := map[string]any{"success": err == nil, "status": h.engine.Status()}
	if err != nil {
		resp["error"] = err.Error()
	}
	respondOK(w, resp)
}

type settingsView struct {
	gitsync.Settings
	WebhookSecretSet bool   `json:"webhook_secret_set"`
	WebhookPath      string `json:"webhook_path"`
}

// GetSettings: GET /api/gitops/settings
func (h *GitOpsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	s, err := gitsync.Load(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "settings": settingsView{Settings: *s,
		WebhookSecretSet: s.WebhookSecret != "", WebhookPath: "/api/gitops/webhook"}})
}

// UpdateSettings: PUT /api/gitops/settings
// webhook_secret: "" keeps the current one, "generate" creates a new one
// (returned once), "none" removes it.
func (h *GitOpsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		gitsync.Settings
		WebhookSecret string `json:"webhook_secret"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	cur, err := gitsync.Load(h.db)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s := req.Settings
	if s.Categories == nil {
		s.Categories = cur.Categories
	}
	generated := ""
	switch req.WebhookSecret {
	case "":
		s.WebhookSecret = cur.WebhookSecret
	case "none":
		s.WebhookSecret = ""
	case "generate":
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
			return
		}
		generated = hex.EncodeToString(b)
		s.WebhookSecret = generated
	default:
		respondErrorSimple(w, `webhook_secret: "", "generate" or "none"`, http.StatusBadRequest)
		return
	}
	for _, name := range append([]string{s.ProviderCred}, credNames(s.Remotes)...) {
		if name == "" {
			continue
		}
		if _, err := gitsync.LoadCredential(h.db, name); err != nil {
			respondOK(w, map[string]any{"success": false, "error": err.Error()})
			return
		}
	}
	if err := gitsync.Save(h.db, &s, configstore.NodeName()); err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	resp := map[string]any{"success": true}
	if generated != "" {
		resp["webhook_secret"] = generated
	}
	if errs := h.sendToPeers(); len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		resp["warning"] = "saved here, not yet on every paired node (sent again with the next change): " + strings.Join(msgs, "; ")
	}
	h.engine.Trigger()
	respondOK(w, resp)
}

func credNames(rs []gitsync.RemoteSetting) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.FetchCred, r.PushCred)
	}
	return out
}

const peerGitopsPath = "/api/config/sync/peer/gitops"

func (h *GitOpsHandler) sendToPeers() []error {
	b, err := gitsync.ExportBundle(h.db)
	if err != nil {
		return []error{err}
	}
	return configstore.SendSealed(h.db, peerGitopsPath, "gitops", b)
}

// PeerSettings: POST /api/config/sync/peer/gitops - a paired node sends its
// (newer) repository settings with their credentials, encrypted.
func (h *GitOpsHandler) PeerSettings(w http.ResponseWriter, r *http.Request) {
	p, err := configstore.AuthenticatePeer(h.db, r.Header.Get("X-DPlane-Node"), r.Header.Get("X-DPlane-Peer-Secret"))
	if err != nil {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var in configstore.SealedDoc
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	var b gitsync.Bundle
	if err := configstore.OpenSealed(p, "gitops", in, &b); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	adopted, err := gitsync.ImportBundle(h.db, &b)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if adopted {
		log.Printf("GIT: repository settings version %d from %s adopted", b.Settings.Version, p.Name)
		h.engine.Trigger()
	}
	respondOK(w, map[string]any{"success": true, "adopted": adopted})
}

// Approve: POST /api/gitops/approve {scope, sha}
func (h *GitOpsHandler) Approve(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Scope string `json:"scope"`
		SHA   string `json:"sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Scope == "" || b.SHA == "" {
		respondErrorSimple(w, "scope and sha are required", http.StatusBadRequest)
		return
	}
	h.result(w, h.engine.Approve(b.Scope, b.SHA))
}

// Take: POST /api/gitops/take {scope}
func (h *GitOpsHandler) Take(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Scope == "" {
		respondErrorSimple(w, "scope is required", http.StatusBadRequest)
		return
	}
	h.result(w, h.engine.TakeRepository(b.Scope))
}

// Resume: POST /api/gitops/holds/{scope}/resume
func (h *GitOpsHandler) Resume(w http.ResponseWriter, r *http.Request) {
	h.result(w, h.engine.Resume(mux.Vars(r)["scope"], r.Header.Get("X-User")))
}

// Revert: POST /api/gitops/holds/{scope}/revert
func (h *GitOpsHandler) Revert(w http.ResponseWriter, r *http.Request) {
	h.result(w, h.engine.Revert(mux.Vars(r)["scope"], r.Header.Get("X-User")))
}

func (h *GitOpsHandler) result(w http.ResponseWriter, err error) {
	resp := map[string]any{"success": err == nil, "status": h.engine.Status()}
	if err != nil {
		resp["error"] = err.Error()
	}
	respondOK(w, resp)
}

// State: GET /api/gitops/state?scope= - the scope's file in the newest commit.
func (h *GitOpsHandler) State(w http.ResponseWriter, r *http.Request) {
	content, path, sha, err := h.engine.File(r.URL.Query().Get("scope"))
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true, "path": path, "sha": sha, "content": content})
}

// Webhook: POST /api/gitops/webhook - PUBLIC; authenticated by the shared
// secret (GitHub/Gitea/Forgejo HMAC signature, GitLab token).
func (h *GitOpsHandler) Webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	s, err := gitsync.Load(h.db)
	if err != nil || !s.Enabled {
		respondErrorSimple(w, "Git is not enabled", http.StatusNotFound)
		return
	}
	provider, ok := gitsync.VerifyWebhook(r.Header, body, s.WebhookSecret)
	if !ok {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	log.Printf("GIT: push notification from %s", provider)
	h.engine.Trigger()
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"success":true}`))
}
