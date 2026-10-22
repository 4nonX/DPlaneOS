package handlers

// docker_image_updates.go - per-stack settings, image update detection and
// auto-update (as Komodo's "poll for updates" and "auto update").
//
// Detection asks the registry, through the engine and its credentials, for
// the digest each image reference points to now (GET /distribution/.../json,
// no pull) and compares it with the digests of the image the service runs.
// An update that failed and was rolled back records the digest it failed
// with; auto-update leaves that image alone until the registry has a newer one.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"dplaned/internal/dockerclient"
	"dplaned/internal/jobs"
)

// ── Settings and last update, per stack ─────────────────────────────────────

// StackSettings are a stack's update settings.
type StackSettings struct {
	AutoUpdate   bool  `json:"auto_update"`             // update when the registry has new images
	Snapshot     *bool `json:"snapshot,omitempty"`      // snapshot its datasets before an update (default on)
	RollbackData bool  `json:"rollback_data,omitempty"` // on a failed update also roll the data back
}

// StackUpdateRecord is the outcome of a stack's last update.
type StackUpdateRecord struct {
	At     time.Time         `json:"at"`
	Result StackUpdateResult `json:"result"`
	// Failed: image ref -> digest that failed (kept until a newer one works).
	Failed map[string]string `json:"failed,omitempty"`
}

const (
	stackSettingsFile    = "stack-settings.json"
	stackUpdateStateFile = "stack-update-state.json"
)

var stackStateMu sync.Mutex

func readJSONFile(name string, v any) error {
	b, err := os.ReadFile(configPath(name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSONFile(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	os.MkdirAll(ConfigDir, 0o755) //nolint
	return writeFileAtomic(configPath(name), b, 0o644)
}

func loadStackSettings() map[string]StackSettings {
	stackStateMu.Lock()
	defer stackStateMu.Unlock()
	m := map[string]StackSettings{}
	if err := readJSONFile(stackSettingsFile, &m); err != nil {
		log.Printf("WARN: %s: %v", stackSettingsFile, err)
	}
	return m
}

// stackUpdateOptionsFor returns the update options of a stack.
func stackUpdateOptionsFor(name string) StackUpdateOptions {
	s := loadStackSettings()[name]
	return StackUpdateOptions{Snapshot: s.Snapshot == nil || *s.Snapshot, RollbackData: s.RollbackData}
}

// recordStackUpdate stores an update's outcome; a working update clears the
// failures of the images it updated.
func recordStackUpdate(name string, res StackUpdateResult) {
	stackStateMu.Lock()
	defer stackStateMu.Unlock()
	state := map[string]StackUpdateRecord{}
	if err := readJSONFile(stackUpdateStateFile, &state); err != nil {
		log.Printf("WARN: %s: %v", stackUpdateStateFile, err)
	}
	rec := state[name]
	if rec.Failed == nil {
		rec.Failed = map[string]string{}
	}
	if res.Error == "" && !res.UpToDate {
		rec.Failed = map[string]string{}
	}
	for ref, d := range res.FailedImages {
		rec.Failed[ref] = d
	}
	rec.At, rec.Result = time.Now(), res
	state[name] = rec
	if err := writeJSONFile(stackUpdateStateFile, state); err != nil {
		log.Printf("WARN: %s: %v", stackUpdateStateFile, err)
	}
}

func loadStackUpdateState() map[string]StackUpdateRecord {
	stackStateMu.Lock()
	defer stackStateMu.Unlock()
	state := map[string]StackUpdateRecord{}
	readJSONFile(stackUpdateStateFile, &state) //nolint
	return state
}

// ── Update detection ────────────────────────────────────────────────────────

// ServiceUpdate is the update status of one service's image.
type ServiceUpdate struct {
	Service         string `json:"service"`
	Image           string `json:"image"`
	UpdateAvailable bool   `json:"update_available"`
	RemoteDigest    string `json:"remote_digest,omitempty"`
	FailedBefore    bool   `json:"failed_before,omitempty"` // this digest was tried and rolled back
	Note            string `json:"note,omitempty"`          // why it cannot be checked
}

// StackUpdates is the update status of a stack.
type StackUpdates struct {
	CheckedAt       time.Time          `json:"checked_at"`
	UpdateAvailable bool               `json:"update_available"`
	Services        []ServiceUpdate    `json:"services"`
	LastUpdate      *StackUpdateRecord `json:"last_update,omitempty"`
}

var (
	updatesMu    sync.Mutex
	updatesCache = map[string]StackUpdates{}
)

// imageChecker is what checkStackImages needs from the engine (a seam for tests).
type imageChecker interface {
	InspectImage(ctx context.Context, ref string) (*dockerclient.ImageInfo, error)
	RegistryDigest(ctx context.Context, ref string) (string, error)
}

// checkStackImages compares each service's running image with the
// registry. failed: ref -> digest that was rolled back.
func checkStackImages(ctx context.Context, ic imageChecker, svcs []stackService, failed map[string]string) []ServiceUpdate {
	remote := map[string]string{} // one registry request per reference
	remoteErr := map[string]error{}
	var out []ServiceUpdate
	for _, s := range svcs {
		u := ServiceUpdate{Service: s.Service, Image: s.ImageRef}
		if strings.Contains(s.ImageRef, "@") {
			u.Note = "pinned by digest"
			out = append(out, u)
			continue
		}
		img, err := ic.InspectImage(ctx, s.ImageID)
		if err != nil {
			u.Note = "image not found: " + err.Error()
			out = append(out, u)
			continue
		}
		digests := img.RepoDigests
		// With the containerd image store (Docker 29) an image loses its
		// RepoDigests as soon as its tag moves on: after a `compose pull`
		// the image a service still runs has none. The tag's image tells.
		if tagImg, err := ic.InspectImage(ctx, s.ImageRef); err == nil {
			if tagImg.ID != img.ID {
				u.UpdateAvailable = true
				u.Note = "a newer image is already pulled; update the stack to apply it"
				for _, d := range tagImg.RepoDigests {
					if _, dg, ok := strings.Cut(d, "@"); ok {
						u.RemoteDigest = dg
						break
					}
				}
				u.FailedBefore = u.RemoteDigest != "" && failed[s.ImageRef] == u.RemoteDigest
				out = append(out, u)
				continue
			}
			if len(digests) == 0 {
				digests = tagImg.RepoDigests
			}
		}
		if len(digests) == 0 {
			u.Note = "built locally (no registry digest)"
			out = append(out, u)
			continue
		}
		if _, done := remote[s.ImageRef]; !done && remoteErr[s.ImageRef] == nil {
			remote[s.ImageRef], remoteErr[s.ImageRef] = ic.RegistryDigest(ctx, s.ImageRef)
		}
		if err := remoteErr[s.ImageRef]; err != nil {
			u.Note = "registry: " + err.Error()
			out = append(out, u)
			continue
		}
		u.RemoteDigest = remote[s.ImageRef]
		u.UpdateAvailable = !dockerclient.HasDigest(digests, u.RemoteDigest)
		u.FailedBefore = u.UpdateAvailable && failed[s.ImageRef] == u.RemoteDigest
		out = append(out, u)
	}
	return out
}

// CheckImageUpdates checks every stack and returns the results.
func CheckImageUpdates(ctx context.Context) map[string]StackUpdates {
	names := listStackNames()
	state := loadStackUpdateState()
	dc := dockerclient.New()
	result := map[string]StackUpdates{}
	for _, name := range names {
		svcs, err := stackServices(ctx, dc, name)
		if err != nil || len(svcs) == 0 {
			continue
		}
		rec := state[name]
		su := StackUpdates{CheckedAt: time.Now(), Services: checkStackImages(ctx, dc, svcs, rec.Failed)}
		for _, s := range su.Services {
			if s.UpdateAvailable {
				su.UpdateAvailable = true
			}
		}
		result[name] = su
	}
	updatesMu.Lock()
	updatesCache = result
	updatesMu.Unlock()
	return result
}

// listStackNames returns the stacks in the stacks directory.
func listStackNames() []string {
	entries, err := os.ReadDir(defaultStacksDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() || !validStackNameRe.MatchString(e.Name()) {
			continue
		}
		if dir, err := stackDir(e.Name()); err == nil {
			if _, err := os.Stat(dir + "/docker-compose.yml"); err == nil {
				names = append(names, e.Name())
			}
		}
	}
	sort.Strings(names)
	return names
}

// ── Auto-update ─────────────────────────────────────────────────────────────

// ImageUpdateInterval is how often the registries are asked.
var ImageUpdateInterval = 6 * time.Hour

// StartImageUpdateMonitor checks for image updates every ImageUpdateInterval
// and updates the stacks that have auto-update on.
func StartImageUpdateMonitor() {
	go func() {
		time.Sleep(5 * time.Minute) // let the stacks start after boot
		for {
			runImageUpdateCycle()
			time.Sleep(ImageUpdateInterval)
		}
	}()
}

func runImageUpdateCycle() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	updates := CheckImageUpdates(ctx)
	settings := loadStackSettings()
	for name, su := range updates {
		if !settings[name].AutoUpdate || !autoUpdateWanted(su) {
			continue
		}
		unlock, ok := lockStack(name)
		if !ok {
			continue // busy: the next cycle
		}
		res := updateStack(ctx, nil, name, stackUpdateOptionsFor(name))
		unlock()
		recordStackUpdate(name, res)
		switch {
		case res.RolledBack:
			DispatchAlert("warning", "docker.auto_update_rolled_back", name,
				fmt.Sprintf("Automatic update of stack %s failed and was rolled back: %s", name, res.Error))
		case res.Error != "":
			DispatchAlert("critical", "docker.auto_update_failed", name,
				fmt.Sprintf("Automatic update of stack %s failed: %s", name, res.Error))
		case len(res.Updated) > 0:
			DispatchAlert("info", "docker.auto_updated", name,
				fmt.Sprintf("Stack %s updated automatically: %s", name, strings.Join(res.Updated, ", ")))
		}
	}
}

// autoUpdateWanted: an update is available that has not failed before.
func autoUpdateWanted(su StackUpdates) bool {
	for _, s := range su.Services {
		if s.UpdateAvailable && !s.FailedBefore {
			return true
		}
	}
	return false
}

// ── HTTP ────────────────────────────────────────────────────────────────────

// GetStackUpdates - GET /api/docker/stacks/updates
// The last check's results (with each stack's last update).
func (h *StackHandler) GetStackUpdates(w http.ResponseWriter, r *http.Request) {
	updatesMu.Lock()
	cache := map[string]StackUpdates{}
	for k, v := range updatesCache {
		cache[k] = v
	}
	updatesMu.Unlock()
	state := loadStackUpdateState()
	for name, rec := range state {
		su := cache[name]
		rec := rec
		su.LastUpdate = &rec
		cache[name] = su
	}
	respondOK(w, map[string]any{"success": true, "stacks": cache, "settings": loadStackSettings()})
}

// CheckStackUpdatesNow - POST /api/docker/stacks/updates/check
func (h *StackHandler) CheckStackUpdatesNow(w http.ResponseWriter, r *http.Request) {
	id := jobs.Start("docker_update_check", func(j *jobs.Job) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		res := CheckImageUpdates(ctx)
		n := 0
		for _, su := range res {
			if su.UpdateAvailable {
				n++
			}
		}
		j.Done(map[string]any{"stacks": len(res), "with_updates": n})
	})
	respondOK(w, map[string]any{"success": true, "job_id": id})
}

// SaveStackSettings - PUT /api/docker/stacks/settings {name, auto_update, snapshot, rollback_data}
func (h *StackHandler) SaveStackSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		StackSettings
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, err := stackDir(req.Name); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	stackStateMu.Lock()
	m := map[string]StackSettings{}
	err := readJSONFile(stackSettingsFile, &m)
	if err == nil {
		m[req.Name] = req.StackSettings
		err = writeJSONFile(stackSettingsFile, m)
	}
	stackStateMu.Unlock()
	if err != nil {
		respondErrorSimple(w, "Failed to save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// UpdateStackJob - POST /api/docker/stacks/update {name}
// Update with rollback as a background job (also used by the API and CLI).
func (h *StackHandler) UpdateStackJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	if _, err := stackDir(req.Name); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	unlock, ok := lockStack(req.Name)
	if !ok {
		respondErrorSimple(w, fmt.Sprintf("Stack '%s' is busy with another operation", req.Name), http.StatusConflict)
		return
	}
	id := jobs.Start("docker_stack_update", func(j *jobs.Job) {
		defer unlock()
		res := updateStack(context.Background(), jobLog(j), req.Name, stackUpdateOptionsFor(req.Name))
		recordStackUpdate(req.Name, res)
		if res.Error != "" {
			j.Fail(res.Error)
			return
		}
		j.Done(map[string]any{"result": res})
	})
	respondOK(w, map[string]any{"success": true, "job_id": id})
}
