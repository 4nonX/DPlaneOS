package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/jobs"
)

// System updates on an installed system. An update is a signed file per
// release; nixos/dplaneos-update.sh (the dplaneos-update tool) installs it
// as a new system generation that is started once on trial and kept only if
// the system is healthy afterwards. This handler finds and downloads the
// file and drives the tool; it never touches the system itself.

// DefaultReleaseURL answers with the latest release in GitHub's format:
// tag_name, body, published_at, assets[].name/.browser_download_url/.size.
const DefaultReleaseURL = "https://api.github.com/repos/4nonX/DPlaneOS/releases/latest"

const (
	updateTool        = "dplaneos-update"
	updateDownloadDir = "/var/lib/dplaneos/updates/download"
	// Update files are a few hundred megabytes; anything near this is not one.
	maxUpdateFileSize = 4 << 30
)

type SystemUpdateHandler struct {
	releaseURL string

	mu        sync.Mutex
	lastCheck map[string]any // result of the last check, shown with the status
	installing bool
}

func NewSystemUpdateHandler(releaseURL string) *SystemUpdateHandler {
	if releaseURL == "" {
		releaseURL = DefaultReleaseURL
	}
	return &SystemUpdateHandler{releaseURL: releaseURL}
}

// updateArch is the architecture name update files carry (uname -m).
func updateArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return runtime.GOARCH
}

func updateToolAvailable() bool {
	_, err := exec.LookPath(updateTool)
	return err == nil
}

// runUpdateTool runs dplaneos-update. Its messages go to stderr (and its own
// log); stdout carries the result of "status" and "verify".
func runUpdateTool(timeout time.Duration, args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, updateTool, args...)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

// toolError turns the tool's stderr into the message for the user: its last
// "ERROR:" line.
func toolError(stderr string, err error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if _, msg, ok := strings.Cut(lines[i], "ERROR: "); ok {
			return msg
		}
	}
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		return lines[len(lines)-1]
	}
	return err.Error()
}

func (h *SystemUpdateHandler) toolStatus() (map[string]any, error) {
	out, stderr, err := runUpdateTool(30*time.Second, "status")
	if err != nil {
		return nil, fmt.Errorf("%s", toolError(stderr, err))
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return nil, fmt.Errorf("unexpected answer from %s: %v", updateTool, err)
	}
	return st, nil
}

// Status GET /api/system/update/status
func (h *SystemUpdateHandler) Status(w http.ResponseWriter, r *http.Request) {
	if !updateToolAvailable() {
		// Not an installed appliance (a development build, or a system
		// whose owner manages it with nixos-rebuild).
		respondOK(w, map[string]any{"success": true, "supported": false, "daemon_version": DaemonVersion})
		return
	}
	st, err := h.toolStatus()
	if err != nil {
		respondErrorSimple(w, "Update status: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.mu.Lock()
	st["last_check"], st["installing"] = h.lastCheck, h.installing
	h.mu.Unlock()
	st["success"], st["supported"], st["daemon_version"], st["arch"] = true, true, DaemonVersion, updateArch()
	respondOK(w, st)
}

type releaseInfo struct {
	TagName     string `json:"tag_name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

func (h *SystemUpdateHandler) fetchRelease(ctx context.Context) (*releaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.releaseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "DPlaneOS/"+DaemonVersion)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("the release server cannot be reached: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the release server answered %s", resp.Status)
	}
	var rel releaseInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("unexpected answer from the release server: %w", err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("the release server names no release")
	}
	return &rel, nil
}

var versionPartRe = regexp.MustCompile(`\d+`)

// versionNewer reports whether version a is newer than b ("v15.1.0" and
// "15.0.2" compare number by number).
func versionNewer(a, b string) bool {
	pa, pb := versionPartRe.FindAllString(a, -1), versionPartRe.FindAllString(b, -1)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// check asks the release server and compares with the installed version.
func (h *SystemUpdateHandler) check(ctx context.Context) (map[string]any, *releaseInfo, string, error) {
	st, err := h.toolStatus()
	if err != nil {
		return nil, nil, "", err
	}
	installed, _ := st["installed_version"].(string)
	rel, err := h.fetchRelease(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	res := map[string]any{
		"checked_at":        time.Now().UTC().Format(time.RFC3339),
		"installed_version": installed,
		"latest_version":    latest,
		"notes":             rel.Body,
		"release_url":       rel.HTMLURL,
		"published_at":      rel.PublishedAt,
		"available":         false,
	}
	assetURL := ""
	suffix := "-" + updateArch() + ".update"
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.Name, suffix) {
			assetURL = a.URL
			res["file"], res["size"] = a.Name, a.Size
		}
	}
	switch {
	case !versionNewer(latest, installed):
		res["message"] = "This system is up to date."
	case assetURL == "":
		res["message"] = fmt.Sprintf("Version %s is published, but without an update file for this kind of machine (%s). It cannot be installed from here.", latest, updateArch())
	default:
		res["available"] = true
	}
	h.mu.Lock()
	h.lastCheck = res
	h.mu.Unlock()
	return res, rel, assetURL, nil
}

// Check POST /api/system/update/check
func (h *SystemUpdateHandler) Check(w http.ResponseWriter, r *http.Request) {
	if !updateToolAvailable() {
		respondErrorSimple(w, "System updates are not available on this installation", http.StatusNotImplemented)
		return
	}
	res, _, _, err := h.check(r.Context())
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	out := map[string]any{"success": true}
	for k, v := range res {
		out[k] = v
	}
	respondOK(w, out)
}

// beginInstall makes sure only one installation runs.
func (h *SystemUpdateHandler) beginInstall() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.installing {
		return false
	}
	h.installing = true
	return true
}

func (h *SystemUpdateHandler) endInstall() {
	h.mu.Lock()
	h.installing = false
	h.mu.Unlock()
}

// installFile runs the tool on a downloaded or uploaded file and reports
// through the job. The file is removed afterwards.
func (h *SystemUpdateHandler) installFile(j *jobs.Job, user, path string) {
	defer os.Remove(path)
	j.Log("Checking the signature and installing (this downloads the rest of the system and takes a few minutes)")
	_, stderr, err := runUpdateTool(2*time.Hour, "install", path)
	if err != nil {
		msg := toolError(stderr, err)
		audit.LogActivity(user, "system_update_install", map[string]any{"success": false, "error": msg})
		j.Fail(msg)
		return
	}
	st, _ := h.toolStatus()
	version := ""
	if p, ok := st["pending"].(map[string]any); ok {
		version, _ = p["version"].(string)
	}
	audit.LogActivity(user, "system_update_install", map[string]any{"success": true, "version": version})
	j.Done(map[string]any{
		"version":          version,
		"restart_required": true,
		"message":          fmt.Sprintf("Version %s is installed and starts at the next restart. If it does not work, the system returns to the current version by itself.", version),
	})
}

// Install POST /api/system/update/install
// Downloads the update file of the latest release and installs it. The new
// version starts at the next restart.
func (h *SystemUpdateHandler) Install(w http.ResponseWriter, r *http.Request) {
	if !updateToolAvailable() {
		respondErrorSimple(w, "System updates are not available on this installation", http.StatusNotImplemented)
		return
	}
	user := r.Header.Get("X-User")
	res, _, assetURL, err := h.check(r.Context())
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadGateway)
		return
	}
	if res["available"] != true {
		msg, _ := res["message"].(string)
		respondErrorSimple(w, msg, http.StatusConflict)
		return
	}
	if !h.beginInstall() {
		respondErrorSimple(w, "An update is being installed already", http.StatusConflict)
		return
	}
	name, _ := res["file"].(string)
	jobID := jobs.Start("system_update", func(j *jobs.Job) {
		defer h.endInstall()
		path, err := h.download(j, assetURL, name)
		if err != nil {
			audit.LogActivity(user, "system_update_install", map[string]any{"success": false, "error": err.Error()})
			j.Fail("Download failed: " + err.Error())
			return
		}
		h.installFile(j, user, path)
	})
	respondOK(w, map[string]any{"success": true, "job_id": jobID})
}

func (h *SystemUpdateHandler) download(j *jobs.Job, rawURL, name string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("the release names an unusable download address")
	}
	if err := os.MkdirAll(updateDownloadDir, 0750); err != nil {
		return "", err
	}
	path := filepath.Join(updateDownloadDir, filepath.Base(name))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "DPlaneOS/"+DaemonVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the server answered %s", resp.Status)
	}
	if resp.ContentLength > maxUpdateFileSize {
		return "", fmt.Errorf("the file is too large to be an update")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if err != nil {
		return "", err
	}
	j.Log(fmt.Sprintf("Downloading %s", filepath.Base(name)))
	var done int64
	lastReport := time.Now()
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(path)
				return "", werr
			}
			done += int64(n)
			if done > maxUpdateFileSize {
				f.Close()
				os.Remove(path)
				return "", fmt.Errorf("the file is too large to be an update")
			}
			if time.Since(lastReport) > time.Second {
				lastReport = time.Now()
				j.Progress(map[string]any{"phase": "download", "bytes": done, "total": resp.ContentLength})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(path)
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	j.Progress(map[string]any{"phase": "install", "bytes": done, "total": done})
	return path, nil
}

// Upload POST /api/system/update/upload (multipart, field "file")
// Installs an update file brought to the system by hand. The system still
// needs to reach the public NixOS cache for the parts that are not in it.
func (h *SystemUpdateHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if !updateToolAvailable() {
		respondErrorSimple(w, "System updates are not available on this installation", http.StatusNotImplemented)
		return
	}
	user := r.Header.Get("X-User")
	if !h.beginInstall() {
		respondErrorSimple(w, "An update is being installed already", http.StatusConflict)
		return
	}
	started := false
	defer func() {
		if !started {
			h.endInstall()
		}
	}()
	r.Body = http.MaxBytesReader(w, r.Body, maxUpdateFileSize)
	mr, err := r.MultipartReader()
	if err != nil {
		respondErrorSimple(w, "Expected a file upload", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(updateDownloadDir, 0750); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	path := filepath.Join(updateDownloadDir, "uploaded.update")
	found := false
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			respondErrorSimple(w, "Upload interrupted: "+err.Error(), http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" {
			continue
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
		if err != nil {
			respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, cerr := io.Copy(f, part)
		if err := f.Close(); cerr == nil {
			cerr = err
		}
		if cerr != nil {
			os.Remove(path)
			respondErrorSimple(w, "Upload failed: "+cerr.Error(), http.StatusBadRequest)
			return
		}
		found = true
	}
	if !found {
		respondErrorSimple(w, "No file in the upload", http.StatusBadRequest)
		return
	}
	started = true
	jobID := jobs.Start("system_update", func(j *jobs.Job) {
		defer h.endInstall()
		h.installFile(j, user, path)
	})
	respondOK(w, map[string]any{"success": true, "job_id": jobID})
}

// Revert POST /api/system/update/revert
// Takes an update that is on trial back, or returns to the previous version;
// the system restarts if it has to.
func (h *SystemUpdateHandler) Revert(w http.ResponseWriter, r *http.Request) {
	if !updateToolAvailable() {
		respondErrorSimple(w, "System updates are not available on this installation", http.StatusNotImplemented)
		return
	}
	user := r.Header.Get("X-User")
	st, err := h.toolStatus()
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	gens, _ := st["generations"].([]any)
	if st["pending"] == nil && len(gens) < 2 {
		respondErrorSimple(w, "There is no previous version to return to", http.StatusConflict)
		return
	}
	audit.LogActivity(user, "system_update_revert", map[string]any{"from": st["installed_version"]})
	// The tool restarts the system: answer first.
	respondOK(w, map[string]any{"success": true, "message": "Returning to the previous version; the system restarts if it has to."})
	go func() {
		time.Sleep(time.Second)
		if _, stderr, err := runUpdateTool(30*time.Minute, "revert"); err != nil {
			log.Printf("SYSTEM UPDATE: revert: %s", toolError(stderr, err))
		}
	}()
}
