package fleet

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"dplaned/internal/secrets"
)

// ── Settings ──────────────────────────────────────────────────────────────────

// Settings of the manager role on this node.
type Settings struct {
	Enabled bool   `json:"enabled"`
	Name    string `json:"name"` // the fleet's name, shown on the remotes
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)

// GetSettings reads the role settings.
func GetSettings(db *sql.DB) (Settings, error) {
	var s Settings
	err := db.QueryRow(`SELECT enabled, name FROM fleet_settings WHERE id`).Scan(&s.Enabled, &s.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, nil
	}
	return s, err
}

// SaveSettings stores them.
func SaveSettings(db *sql.DB, s Settings) error {
	if s.Enabled && !nameRe.MatchString(s.Name) {
		return errors.New("give the fleet a name (letters, digits, space, . _ -)")
	}
	_, err := db.Exec(`INSERT INTO fleet_settings (id, enabled, name) VALUES (TRUE, $1, $2)
		ON CONFLICT (id) DO UPDATE SET enabled = EXCLUDED.enabled, name = EXCLUDED.name`, s.Enabled, s.Name)
	return err
}

// ── Remotes ───────────────────────────────────────────────────────────────────

// Remote is a registered node.
type Remote struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	Fingerprint string            `json:"fingerprint"`
	Labels      map[string]string `json:"labels"`
	NodeID      string            `json:"node_id"`
	AddedAt     time.Time         `json:"added_at"`
	LastSeen    *time.Time        `json:"last_seen,omitempty"`
	LastError   string            `json:"last_error,omitempty"`
	Status      *NodeStatus       `json:"status,omitempty"`
	token       string
}

// Client returns a client for the remote, calling on behalf of fleet and manager.
func (r Remote) Client(fleet, manager string) *Client {
	return &Client{URL: r.URL, Token: r.token, Fingerprint: r.Fingerprint, Fleet: fleet, Manager: manager}
}

var labelRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)

// ValidLabels checks label keys and values (site=berlin, tier=canary).
func ValidLabels(l map[string]string) error {
	for k, v := range l {
		if !labelRe.MatchString(k) || (v != "" && !labelRe.MatchString(v)) {
			return fmt.Errorf("invalid label %s=%s (lower-case letters, digits, . _ -)", k, v)
		}
	}
	return nil
}

const remoteCols = `id, name, url, token, fingerprint, labels, node_id, added_at, last_seen, last_error, status`

func scanRemotes(rows *sql.Rows) ([]Remote, error) {
	defer rows.Close()
	out := []Remote{}
	for rows.Next() {
		var r Remote
		var sealed string
		var labels, status []byte
		var seen sql.NullTime
		if err := rows.Scan(&r.ID, &r.Name, &r.URL, &sealed, &r.Fingerprint, &labels, &r.NodeID, &r.AddedAt, &seen,
			&r.LastError, &status); err != nil {
			return nil, err
		}
		tok, err := secrets.Open(sealed)
		if err != nil {
			return nil, fmt.Errorf("remote %s: opening its token: %w", r.Name, err)
		}
		r.token = tok
		r.Labels = map[string]string{}
		_ = json.Unmarshal(labels, &r.Labels)
		if seen.Valid {
			r.LastSeen = &seen.Time
		}
		if len(status) > 0 {
			var st NodeStatus
			if json.Unmarshal(status, &st) == nil {
				r.Status = &st
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Remotes lists the registered remotes by name.
func Remotes(db *sql.DB) ([]Remote, error) {
	rows, err := db.Query(`SELECT ` + remoteCols + ` FROM fleet_remotes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return scanRemotes(rows)
}

// GetRemote reads one remote.
func GetRemote(db *sql.DB, id int64) (*Remote, error) {
	rows, err := db.Query(`SELECT `+remoteCols+` FROM fleet_remotes WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	rs, err := scanRemotes(rows)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, sql.ErrNoRows
	}
	return &rs[0], nil
}

// AddRemote registers a remote: it must answer with the token (its
// inventory report), and its certificate is pinned.
func AddRemote(db *sql.DB, name, rawURL, token string, labels map[string]string) (*Remote, error) {
	if !nameRe.MatchString(name) {
		return nil, errors.New("invalid name")
	}
	if err := ValidURL(rawURL); err != nil {
		return nil, err
	}
	if err := ValidLabels(labels); err != nil {
		return nil, err
	}
	if token == "" {
		return nil, errors.New("an API token of the remote (admin scope) is needed")
	}
	c := &Client{URL: rawURL, Token: token}
	st, err := c.Status()
	if err != nil {
		return nil, fmt.Errorf("the remote does not answer: %w", err)
	}
	sealed, err := secrets.Seal(token)
	if err != nil {
		return nil, err
	}
	labelsJSON, _ := json.Marshal(labels)
	statusJSON, _ := json.Marshal(st)
	var id int64
	err = db.QueryRow(`INSERT INTO fleet_remotes (name, url, token, fingerprint, labels, node_id, last_seen, status)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), $7) RETURNING id`, name, rawURL, sealed, c.Seen(), labelsJSON, st.NodeID, statusJSON).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("storing the remote (name and address must be unique): %w", err)
	}
	return GetRemote(db, id)
}

// RemoveRemote forgets a remote (nothing changes on the remote).
func RemoveRemote(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM fleet_remotes WHERE id = $1`, id)
	return err
}

// SetLabels replaces a remote's labels.
func SetLabels(db *sql.DB, id int64, labels map[string]string) error {
	if err := ValidLabels(labels); err != nil {
		return err
	}
	raw, _ := json.Marshal(labels)
	_, err := db.Exec(`UPDATE fleet_remotes SET labels = $2 WHERE id = $1`, id, raw)
	return err
}

func recordStatus(db *sql.DB, id int64, st *NodeStatus, err error) {
	if err != nil {
		_, _ = db.Exec(`UPDATE fleet_remotes SET last_error = $2 WHERE id = $1`, id, err.Error())
		return
	}
	raw, _ := json.Marshal(st)
	_, _ = db.Exec(`UPDATE fleet_remotes SET status = $2, last_seen = NOW(), last_error = '', node_id = $3 WHERE id = $1`, id, raw, st.NodeID)
}

// ── Fleet resources ───────────────────────────────────────────────────────────

// FleetKinds can be fleet-wide.
var FleetKinds = map[string]bool{"user": true, "group": true, "settings": true}

var keyRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,63}$`)

// StoredResource is a fleet resource with its version.
type StoredResource struct {
	Resource
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// Resources lists the fleet resources.
func Resources(db *sql.DB) ([]StoredResource, error) {
	rows, err := db.Query(`SELECT kind, key, payload, version, updated_at, updated_by FROM fleet_resources ORDER BY kind, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredResource{}
	for rows.Next() {
		var r StoredResource
		var payload []byte
		if err := rows.Scan(&r.Kind, &r.Key, &payload, &r.Version, &r.UpdatedAt, &r.UpdatedBy); err != nil {
			return nil, err
		}
		if payload != nil {
			if err := json.Unmarshal(payload, &r.Payload); err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutResource sets a fleet resource (payload nil: delete it on the remotes).
func PutResource(db *sql.DB, kind, key string, payload map[string]any, by string) error {
	if !FleetKinds[kind] {
		return fmt.Errorf("%s cannot be fleet-wide (users, groups, shared settings)", kind)
	}
	if !keyRe.MatchString(key) || (kind == "settings" && key != "settings") {
		return fmt.Errorf("invalid key %q", key)
	}
	var raw any
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		raw = b
	}
	_, err := db.Exec(`INSERT INTO fleet_resources (kind, key, payload, version, updated_at, updated_by) VALUES ($1, $2, $3, 1, NOW(), $4)
		ON CONFLICT (kind, key) DO UPDATE SET payload = EXCLUDED.payload, version = fleet_resources.version + 1,
			updated_at = NOW(), updated_by = EXCLUDED.updated_by`, kind, key, raw, by)
	return err
}

// ForgetResource stops managing a resource fleet-wide (nothing changes on the remotes).
func ForgetResource(db *sql.DB, kind, key string) error {
	_, err := db.Exec(`DELETE FROM fleet_resources WHERE kind = $1 AND key = $2`, kind, key)
	return err
}

// ── Rollouts ──────────────────────────────────────────────────────────────────

// Wave is one step of a rollout.
type Wave struct {
	Name    string  `json:"name"`
	Remotes []int64 `json:"remotes"`
}

// Rollout as stored and shown.
type Rollout struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	State       string          `json:"state"`
	Resources   []Resource      `json:"resources"`
	Waves       []Wave          `json:"waves"`
	CurrentWave int             `json:"current_wave"`
	SoakSecs    int             `json:"soak_secs"`
	AutoAdvance bool            `json:"auto_advance"`
	WaveDoneAt  *time.Time      `json:"wave_done_at,omitempty"`
	Message     string          `json:"message,omitempty"`
	CreatedBy   string          `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
	Targets     []RolloutTarget `json:"targets"`
}

// RolloutTarget is one remote of a rollout.
type RolloutTarget struct {
	RemoteID  int64      `json:"remote_id"`
	Name      string     `json:"name"`
	Wave      int        `json:"wave"`
	State     string     `json:"state"`
	Detail    string     `json:"detail,omitempty"`
	Attempts  int        `json:"attempts"`
	AppliedAt *time.Time `json:"applied_at,omitempty"`
}

// CreateRollout snapshots resources and plans the waves. Every remote may
// appear in one wave only.
func CreateRollout(db *sql.DB, name string, rs []Resource, waves []Wave, soakSecs int, autoAdvance bool, by string) (int64, error) {
	if !nameRe.MatchString(name) {
		return 0, errors.New("invalid rollout name")
	}
	if len(rs) == 0 {
		return 0, errors.New("choose at least one resource")
	}
	if len(waves) == 0 {
		return 0, errors.New("plan at least one wave")
	}
	if soakSecs < 0 || soakSecs > 7*86400 {
		return 0, errors.New("soak time: 0 to 7 days")
	}
	seen := map[int64]bool{}
	for i, w := range waves {
		if len(w.Remotes) == 0 {
			return 0, fmt.Errorf("wave %d has no remote", i+1)
		}
		for _, id := range w.Remotes {
			if seen[id] {
				return 0, fmt.Errorf("a remote is in more than one wave")
			}
			seen[id] = true
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rsJSON, _ := json.Marshal(rs)
	wavesJSON, _ := json.Marshal(waves)
	var id int64
	if err := tx.QueryRow(`INSERT INTO fleet_rollouts (name, resources, waves, soak_secs, auto_advance, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, name, rsJSON, wavesJSON, soakSecs, autoAdvance, by).Scan(&id); err != nil {
		return 0, err
	}
	for i, w := range waves {
		for _, rid := range w.Remotes {
			if _, err := tx.Exec(`INSERT INTO fleet_rollout_targets (rollout_id, remote_id, wave) VALUES ($1, $2, $3)`, id, rid, i); err != nil {
				return 0, fmt.Errorf("remote %d: %w", rid, err)
			}
		}
	}
	return id, tx.Commit()
}

// Rollouts lists rollouts, newest first, with their targets.
func Rollouts(db *sql.DB, limit int) ([]Rollout, error) {
	rows, err := db.Query(`SELECT id, name, state, resources, waves, current_wave, soak_secs, auto_advance, wave_done_at, message,
		created_by, created_at FROM fleet_rollouts ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var out []Rollout
	for rows.Next() {
		var r Rollout
		var rs, ws []byte
		var done sql.NullTime
		if err := rows.Scan(&r.ID, &r.Name, &r.State, &rs, &ws, &r.CurrentWave, &r.SoakSecs, &r.AutoAdvance, &done, &r.Message,
			&r.CreatedBy, &r.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal(rs, &r.Resources)
		_ = json.Unmarshal(ws, &r.Waves)
		if done.Valid {
			r.WaveDoneAt = &done.Time
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Targets, err = targets(db, out[i].ID); err != nil {
			return nil, err
		}
	}
	if out == nil {
		out = []Rollout{}
	}
	return out, nil
}

func targets(db *sql.DB, rollout int64) ([]RolloutTarget, error) {
	rows, err := db.Query(`SELECT t.remote_id, r.name, t.wave, t.state, t.detail, t.attempts, t.applied_at
		FROM fleet_rollout_targets t JOIN fleet_remotes r ON r.id = t.remote_id WHERE t.rollout_id = $1`, rollout)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RolloutTarget{}
	for rows.Next() {
		var t RolloutTarget
		var at sql.NullTime
		if err := rows.Scan(&t.RemoteID, &t.Name, &t.Wave, &t.State, &t.Detail, &t.Attempts, &at); err != nil {
			return nil, err
		}
		if at.Valid {
			t.AppliedAt = &at.Time
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Wave != out[j].Wave {
			return out[i].Wave < out[j].Wave
		}
		return out[i].Name < out[j].Name
	})
	return out, rows.Err()
}

// ErrRolloutState: the action does not fit the rollout's state.
var ErrRolloutState = errors.New("not possible in the rollout's current state")

// Continue resumes a paused rollout (failed remotes are tried again) or
// advances a rollout waiting for the operator after a wave.
func Continue(db *sql.DB, id int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var waves []byte
	var cur int
	var done sql.NullTime
	if err := tx.QueryRow(`SELECT state, waves, current_wave, wave_done_at FROM fleet_rollouts WHERE id = $1 FOR UPDATE`, id).
		Scan(&state, &waves, &cur, &done); err != nil {
		return err
	}
	switch {
	case state == RolloutPaused:
		if _, err := tx.Exec(`UPDATE fleet_rollout_targets SET state = 'pending', attempts = 0, detail = ''
			WHERE rollout_id = $1 AND state = 'failed'`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE fleet_rollouts SET state = 'running', message = '', updated_at = NOW() WHERE id = $1`, id); err != nil {
			return err
		}
	case state == RolloutRunning && done.Valid:
		var ws []Wave
		_ = json.Unmarshal(waves, &ws)
		if cur+1 >= len(ws) {
			return ErrRolloutState
		}
		if _, err := tx.Exec(`UPDATE fleet_rollouts SET current_wave = current_wave + 1, wave_done_at = NULL, updated_at = NOW() WHERE id = $1`, id); err != nil {
			return err
		}
	default:
		return ErrRolloutState
	}
	return tx.Commit()
}

// SetRolloutState pauses or cancels a rollout.
func SetRolloutState(db *sql.DB, id int64, state, message string) error {
	from := map[string][]string{
		RolloutPaused:    {RolloutRunning},
		RolloutCancelled: {RolloutRunning, RolloutPaused},
	}[state]
	res, err := db.Exec(`UPDATE fleet_rollouts SET state = $2, message = $3, updated_at = NOW() WHERE id = $1 AND state = ANY($4)`,
		id, state, message, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrRolloutState
	}
	return nil
}
