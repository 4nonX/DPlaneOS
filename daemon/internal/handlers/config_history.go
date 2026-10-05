package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"

	"dplaned/internal/configstore"
	"dplaned/internal/gitops"
)

// ConfigHistoryHandler serves the configuration history (Design 0001, Phase 1).
type ConfigHistoryHandler struct {
	db          *sql.DB
	smbConfPath string
}

func NewConfigHistoryHandler(db *sql.DB, smbConfPath string) *ConfigHistoryHandler {
	return &ConfigHistoryHandler{db: db, smbConfPath: smbConfPath}
}

// revisionView is a revision plus its changes against the previous revision
// of the same resource.
type revisionView struct {
	configstore.Revision
	Changes     []configstore.FieldChange `json:"changes"`
	Created     bool                      `json:"created"` // no earlier revision
	Deleted     bool                      `json:"deleted"`
	CanRollback bool                      `json:"can_rollback"`
}

func (h *ConfigHistoryHandler) views(revs []configstore.Revision) ([]revisionView, error) {
	var baseIDs []int64
	for _, r := range revs {
		if r.BaseRevision != nil {
			baseIDs = append(baseIDs, *r.BaseRevision)
		}
	}
	bases := map[int64]map[string]any{}
	if len(baseIDs) > 0 {
		rows, err := h.db.Query(`SELECT id, payload FROM config_revisions WHERE id = ANY($1)`, baseIDs)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return nil, err
			}
			var p map[string]any
			if raw != nil {
				if err := json.Unmarshal(raw, &p); err != nil {
					rows.Close()
					return nil, err
				}
			}
			bases[id] = p
		}
		rows.Close()
	}
	out := make([]revisionView, 0, len(revs))
	for _, r := range revs {
		var base map[string]any
		if r.BaseRevision != nil {
			base = bases[*r.BaseRevision]
		}
		out = append(out, revisionView{
			Revision:    r,
			Changes:     configstore.DiffPayloads(base, r.Payload),
			Created:     r.BaseRevision == nil || (base == nil && r.Payload != nil),
			Deleted:     r.Payload == nil,
			CanRollback: configstore.RollbackKinds[r.Kind],
		})
	}
	return out, nil
}

// History lists revisions, newest first.
// GET /api/config/history?kind=&key=&limit=&before=
func (h *ConfigHistoryHandler) History(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	revs, err := configstore.History(h.db, configstore.HistoryFilter{
		Kind: q.Get("kind"), Key: q.Get("key"), Limit: limit, Before: before,
	})
	if err != nil {
		respondErrorSimple(w, "Failed to read history: "+err.Error(), http.StatusInternalServerError)
		return
	}
	views, err := h.views(revs)
	if err != nil {
		respondErrorSimple(w, "Failed to read history: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "revisions": views})
}

// Revision returns one revision with its changes.
// GET /api/config/revisions/{id}
func (h *ConfigHistoryHandler) Revision(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		respondErrorSimple(w, "Invalid revision id", http.StatusBadRequest)
		return
	}
	rev, err := configstore.Get(h.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		respondErrorSimple(w, "Revision not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, "Failed to read revision: "+err.Error(), http.StatusInternalServerError)
		return
	}
	views, err := h.views([]configstore.Revision{*rev})
	if err != nil {
		respondErrorSimple(w, "Failed to read revision: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "revision": views[0]})
}

// Rollback returns a resource to a recorded revision.
// POST /api/config/rollback {"revision_id": 123}
func (h *ConfigHistoryHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RevisionID int64 `json:"revision_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RevisionID <= 0 {
		respondErrorSimple(w, "revision_id is required", http.StatusBadRequest)
		return
	}
	ctx := gitops.ApplyContext{
		DB:             h.db,
		SmbConfPath:    h.smbConfPath,
		NFSExportsPath: "/etc/exports",
		OwnershipGuard: func() bool { ok, _ := gitops.IsWriter(); return ok },
	}
	res, err := configstore.Rollback(h.db, ctx, req.RevisionID, r.Header.Get("X-User"))
	switch {
	case errors.Is(err, configstore.ErrBusy):
		respondErrorSimple(w, "Another apply or rollback is in progress; try again shortly", http.StatusLocked)
		return
	case errors.Is(err, gitops.ErrNotWriter):
		respondErrorSimple(w, "Rollback runs on the active node only: "+err.Error(), http.StatusConflict)
		return
	case err != nil:
		respondOK(w, map[string]any{"success": false, "error": err.Error(), "result": res})
		return
	}
	if res.Changeset != "" || res.NoChange {
		// Write the rolled-back state to Git like any other change.
		gitops.CommitAllAsync(h.db)
	}
	respondOK(w, map[string]any{"success": true, "result": res})
}

// Export downloads the current configuration as state.yaml.
// GET /api/config/export
func (h *ConfigHistoryHandler) Export(w http.ResponseWriter, r *http.Request) {
	content, err := configstore.Export(h.db)
	if err != nil {
		respondErrorSimple(w, "Export failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition",
		`attachment; filename="state-`+time.Now().Format("20060102-150405")+`.yaml"`)
	_, _ = w.Write([]byte(content))
}

// Capture records the current configuration now.
// POST /api/config/capture
func (h *ConfigHistoryHandler) Capture(w http.ResponseWriter, r *http.Request) {
	cs, err := configstore.Capture(h.db, configstore.OriginDetected, r.Header.Get("X-User"), "recorded on request")
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]any{"success": true, "changeset": cs, "changed": cs != ""})
}
