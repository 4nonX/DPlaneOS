package handlers

// ═══════════════════════════════════════════════════════════════════════════
//  Multi-Stack Templates  (v5.1)
//
//  A "template" is a directory containing one or more sub-directories,
//  each with a docker-compose.yml. Templates may also include:
//    dplane-requirements.json - ZFS dataset + firewall port requirements
//    template.json            - metadata (name, description, icon, variables)
//
//  Endpoints:
//    GET  /api/docker/templates             - list built-in templates
//    POST /api/docker/templates/deploy      - deploy a template (git URL or built-in)
//    GET  /api/docker/templates/installed   - list installed template groups
//
//  After deployment each stack directory contains a .dplane-template file
//  recording the template ID. ListStacks reads this to return template_id.
// ═══════════════════════════════════════════════════════════════════════════

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/jobs"
)

// ── Types ────────────────────────────────────────────────────────────────────

// TemplateVariable is a user-configurable value prompted by the UI at deploy time.
type TemplateVariable struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"` // render as password field in UI
}

// TemplateMetadata is read from template.json at the root of a template.
type TemplateMetadata struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Icon        string             `json:"icon,omitempty"` // Material Symbol or image filename
	Version     string             `json:"version,omitempty"`
	Author      string             `json:"author,omitempty"`
	Tags        []string           `json:"tags,omitempty"`
	Stacks      []string           `json:"stacks,omitempty"` // ordered stack names
	Variables   []TemplateVariable `json:"variables,omitempty"`
	Network     string             `json:"network,omitempty"` // shared Docker network name
}

// TemplateRequirements is read from dplane-requirements.json.
type TemplateRequirements struct {
	ZFSDatasets []ZFSDatasetReq `json:"zfs_datasets,omitempty"`
	FirewallTCP []int           `json:"firewall_tcp,omitempty"`
	FirewallUDP []int           `json:"firewall_udp,omitempty"`
}

// ZFSDatasetReq describes a ZFS dataset the template needs.
type ZFSDatasetReq struct {
	Path       string `json:"path"` // e.g. "tank/data/plex"
	QuotaGB    int    `json:"quota_gb,omitempty"`
	MountPoint string `json:"mountpoint,omitempty"`
}

// BuiltinTemplate is a template that ships with D-PlaneOS.
type BuiltinTemplate struct {
	TemplateMetadata
	GitURL string `json:"git_url,omitempty"` // if empty, bundled in binary
}

// ── Built-in template catalogue ───────────────────────────────────────────────

var builtinTemplates = []BuiltinTemplate{
	{
		TemplateMetadata: TemplateMetadata{
			ID:          "arr-suite",
			Name:        "*arr Media Suite",
			Description: "Radarr, Sonarr, Prowlarr, qBittorrent, and Jellyfin - fully wired together on a shared media network.",
			Icon:        "movie",
			Tags:        []string{"media", "automation"},
			Stacks:      []string{"jellyfin", "radarr", "sonarr", "prowlarr", "qbittorrent"},
			Network:     "media",
			Variables: []TemplateVariable{
				{Key: "MEDIA_PATH", Label: "Media directory", Default: "/tank/media", Required: true},
				{Key: "DOWNLOADS_PATH", Label: "Downloads directory", Default: "/tank/downloads", Required: true},
				{Key: "PUID", Label: "User ID", Default: "1000"},
				{Key: "PGID", Label: "Group ID", Default: "1000"},
				{Key: "TZ", Label: "Timezone", Default: "Europe/Berlin"},
			},
		},
		GitURL: "https://github.com/4nonX/dplaneos-templates",
	},
	{
		TemplateMetadata: TemplateMetadata{
			ID:          "monitoring-suite",
			Name:        "Monitoring Suite",
			Description: "Prometheus, Grafana, Loki, and Promtail - full observability stack with D-PlaneOS dashboards pre-configured.",
			Icon:        "monitoring",
			Tags:        []string{"monitoring", "metrics", "logs"},
			Stacks:      []string{"prometheus", "grafana", "loki"},
			Network:     "monitoring",
			Variables: []TemplateVariable{
				{Key: "GRAFANA_PASSWORD", Label: "Grafana admin password", Required: true, Secret: true},
				{Key: "DATA_PATH", Label: "Data directory", Default: "/tank/monitoring"},
			},
		},
		GitURL: "https://github.com/4nonX/dplaneos-templates",
	},
	{
		TemplateMetadata: TemplateMetadata{
			ID:          "home-automation",
			Name:        "Home Automation",
			Description: "Home Assistant, Mosquitto MQTT broker, and Node-RED.",
			Icon:        "home",
			Tags:        []string{"automation", "iot", "smarthome"},
			Stacks:      []string{"homeassistant", "mosquitto", "nodered"},
			Network:     "home",
			Variables: []TemplateVariable{
				{Key: "CONFIG_PATH", Label: "Config directory", Default: "/tank/homeassistant", Required: true},
				{Key: "TZ", Label: "Timezone", Default: "Europe/Berlin"},
			},
		},
		GitURL: "https://github.com/4nonX/dplaneos-templates",
	},
}

// ── Handler ───────────────────────────────────────────────────────────────────

type TemplateHandler struct{}

func NewTemplateHandler() *TemplateHandler { return &TemplateHandler{} }

// GET /api/docker/templates
// Returns the built-in template catalogue.
func (h *TemplateHandler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	respondOK(w, map[string]interface{}{
		"success":   true,
		"templates": builtinTemplates,
		"count":     len(builtinTemplates),
	})
}

// POST /api/docker/templates/deploy
// Deploys a template - either a built-in (by id) or a custom git URL.
// Returns immediately with a job_id. Poll GET /api/jobs/{id} for progress.
//
// Request body:
//
//	{
//	  "template_id": "arr-suite",   // built-in id, OR
//	  "git_url":     "https://...", // custom git URL (takes precedence)
//	  "variables":   { "MEDIA_PATH": "/tank/media", ... }
//	}
func (h *TemplateHandler) DeployTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TemplateID string            `json:"template_id"`
		GitURL     string            `json:"git_url"`
		Variables  map[string]string `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.TemplateID == "" && req.GitURL == "" {
		respondErrorSimple(w, "template_id or git_url is required", http.StatusBadRequest)
		return
	}

	// Resolve the git URL to clone
	gitURL := req.GitURL
	var meta TemplateMetadata
	if gitURL == "" {
		found := false
		for _, t := range builtinTemplates {
			if t.ID == req.TemplateID {
				gitURL = t.GitURL
				meta = t.TemplateMetadata
				found = true
				break
			}
		}
		if !found {
			respondErrorSimple(w, fmt.Sprintf("unknown template_id: %q", req.TemplateID), http.StatusBadRequest)
			return
		}
	} else {
		// Custom URL - derive a safe ID from the URL
		meta.ID = sanitizeServiceName(filepath.Base(strings.TrimSuffix(gitURL, ".git")))
		meta.Name = meta.ID
	}

	if gitURL == "" {
		respondErrorSimple(w, "template has no git_url configured", http.StatusBadRequest)
		return
	}

	user := getUserFromRequest(r)
	variables := req.Variables
	if variables == nil {
		variables = map[string]string{}
	}

	// Clone the template repo into a temp directory, then walk sub-stacks and deploy each.
	jobID := jobs.Start("template_deploy_"+meta.ID, func(j *jobs.Job) {
		j.Log(fmt.Sprintf("Deploying template: %s", meta.Name))

		// ── Step 1: Clone template repo ──────────────────────────────────
		cloneDir := filepath.Join(os.TempDir(), fmt.Sprintf("dplane-tpl-%s-%d", meta.ID, time.Now().UnixNano()))
		defer os.RemoveAll(cloneDir) // clean up temp clone regardless

		j.Log(fmt.Sprintf("Cloning %s …", gitURL))
		out, err := cmdutil.RunSlow("/usr/bin/git", "clone", "--depth=1", gitURL, cloneDir)
		if err != nil {
			j.Fail(fmt.Sprintf("git clone failed: %v\n%s", err, out))
			return
		}

		// ── Step 2: Read template.json if present ─────────────────────────
		metaPath := filepath.Join(cloneDir, "template.json")
		if data, err := os.ReadFile(metaPath); err == nil {
			_ = json.Unmarshal(data, &meta) // best effort - override with repo metadata
		}

		// ── Step 3: Process dplane-requirements.json ─────────────────────
		reqsPath := filepath.Join(cloneDir, "dplane-requirements.json")
		if data, err := os.ReadFile(reqsPath); err == nil {
			var reqs TemplateRequirements
			if json.Unmarshal(data, &reqs) == nil {
				// Create ZFS datasets
				for _, ds := range reqs.ZFSDatasets {
					j.Log(fmt.Sprintf("Ensuring ZFS dataset: %s", ds.Path))
					args := []string{"create", "-p"}
					if ds.QuotaGB > 0 {
						args = append(args, "-o", fmt.Sprintf("quota=%dG", ds.QuotaGB))
					}
					if ds.MountPoint != "" {
						args = append(args, "-o", fmt.Sprintf("mountpoint=%s", ds.MountPoint))
					}
					args = append(args, ds.Path)
					if dsOut, dsErr := cmdutil.RunMedium("/usr/sbin/zfs", args...); dsErr != nil {
						// Dataset may already exist - log and continue
						j.Log(fmt.Sprintf("  zfs create: %v %s", dsErr, dsOut))
					}
				}
				// Note: firewall port requirements are logged but not auto-applied -
				// user controls firewall changes explicitly via the UI.
				if len(reqs.FirewallTCP)+len(reqs.FirewallUDP) > 0 {
					j.Log(fmt.Sprintf("Template requires firewall ports - open manually if needed: TCP%v UDP%v",
						reqs.FirewallTCP, reqs.FirewallUDP))
				}
			}
		}

		// ── Step 4: Create shared Docker network if requested ─────────────
		if meta.Network != "" {
			j.Log(fmt.Sprintf("Ensuring Docker network: %s", meta.Network))
			netOut, netErr := cmdutil.RunFast("/usr/bin/docker",
				"network", "create", "--driver", "bridge", meta.Network)
			if netErr != nil && !strings.Contains(string(netOut), "already exists") {
				j.Log(fmt.Sprintf("  network create: %v", netErr))
			}
		}

		// ── Step 5: Find sub-stack directories ────────────────────────────
		// Any sub-directory containing docker-compose.yml is a deployable stack.
		// If template.json defines a Stacks list, deploy in that order.
		// Otherwise walk alphabetically.
		stackDirs, err := findTemplateSubs(cloneDir, meta.Stacks)
		if err != nil || len(stackDirs) == 0 {
			// No sub-stacks found - treat the root as a single stack
			rootCompose := filepath.Join(cloneDir, "docker-compose.yml")
			if _, err := os.Stat(rootCompose); err == nil {
				stackDirs = []string{cloneDir}
			} else {
				j.Fail("template has no sub-stacks and no root docker-compose.yml")
				return
			}
		}

		// ── Step 6: Deploy each sub-stack ────────────────────────────────
		deployed := 0
		for _, subDir := range stackDirs {
			subName := sanitizeServiceName(filepath.Base(subDir))
			destDir, dirErr := stackDir(subName)
			if dirErr != nil {
				j.Log(fmt.Sprintf("  skip %s: %v", subName, dirErr))
				continue
			}

			j.Log(fmt.Sprintf("  Deploying stack: %s", subName))

			// Write stack files
			if err := os.MkdirAll(destDir, 0750); err != nil {
				j.Log(fmt.Sprintf("  mkdir %s: %v", subName, err))
				continue
			}

			// Copy all files from sub-directory
			if err := copyDir(subDir, destDir); err != nil {
				j.Log(fmt.Sprintf("  copy %s: %v", subName, err))
				continue
			}

			// Apply variable substitution to docker-compose.yml and .env
			composeDest := filepath.Join(destDir, "docker-compose.yml")
			if err := substituteVars(composeDest, variables); err != nil {
				j.Log(fmt.Sprintf("  var substitution %s: %v", subName, err))
			}
			envDest := filepath.Join(destDir, ".env")
			if err := substituteVars(envDest, variables); err != nil {
				// .env may not exist - not an error
				_ = err
			}

			// Write .dplane-template marker so ListStacks can group by template
			markerPath := filepath.Join(destDir, ".dplane-template")
			markerData := map[string]string{
				"template_id":   meta.ID,
				"template_name": meta.Name,
				"deployed_at":   time.Now().UTC().Format(time.RFC3339),
			}
			if markerBytes, err := json.Marshal(markerData); err == nil {
				_ = os.WriteFile(markerPath, markerBytes, 0640)
			}

			// Run compose up
			composePath := filepath.Join(destDir, "docker-compose.yml")
			upOut, upErr := cmdutil.RunSlow("/usr/bin/docker",
				"compose", "--project-directory", destDir, "-f", composePath, "up", "-d")
			if upErr != nil {
				j.Log(fmt.Sprintf("  compose up %s failed: %v\n%s", subName, upErr, upOut))
			} else {
				j.Log(fmt.Sprintf("  ✓ %s running", subName))
				deployed++
			}
		}

		if deployed == 0 {
			j.Fail("no stacks were successfully deployed")
			return
		}

		audit.LogCommand(audit.LevelInfo, user, "template_deploy",
			[]string{meta.ID}, true, 0, nil)

		j.Done(map[string]interface{}{
			"success":       true,
			"template_id":   meta.ID,
			"template_name": meta.Name,
			"stacks":        deployed,
		})
	})

	respondOK(w, map[string]interface{}{
		"success":     true,
		"job_id":      jobID,
		"template_id": meta.ID,
	})
}

// GET /api/docker/templates/installed
// Returns all stack groups (stacks sharing a template_id).
// Stacks with no .dplane-template marker are returned as standalone (template_id="").
func (h *TemplateHandler) ListInstalledTemplates(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(defaultStacksDir)
	if err != nil {
		respondOK(w, map[string]interface{}{"success": true, "groups": []interface{}{}})
		return
	}

	type StackGroup struct {
		TemplateID   string      `json:"template_id"`
		TemplateName string      `json:"template_name"`
		Stacks       []StackInfo `json:"stacks"`
		Icon         string      `json:"icon,omitempty"`
	}

	groups := map[string]*StackGroup{}

	for _, entry := range entries {
		if !entry.IsDir() || !validStackNameRe.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(defaultStacksDir, entry.Name())
		composePath := filepath.Join(dir, "docker-compose.yml")
		if _, err := os.Stat(composePath); err != nil {
			continue
		}

		// Read template marker
		tid, tname := "", ""
		markerPath := filepath.Join(dir, ".dplane-template")
		if data, err := os.ReadFile(markerPath); err == nil {
			var m map[string]string
			if json.Unmarshal(data, &m) == nil {
				tid = m["template_id"]
				tname = m["template_name"]
			}
		}
		if tid == "" {
			tid = "__standalone__"
			tname = ""
		}

		// Build StackInfo
		fileInfo, _ := os.Stat(composePath)
		si := StackInfo{
			Name: entry.Name(),
			Path: dir,
		}
		if fileInfo != nil {
			si.FileSize = fileInfo.Size()
			si.UpdatedAt = fileInfo.ModTime().UTC().Format(time.RFC3339)
		}
		if di, err := entry.Info(); err == nil {
			si.CreatedAt = di.ModTime().UTC().Format(time.RFC3339)
		}

		// Get status
		out, err := cmdutil.RunFast("/usr/bin/docker",
			"compose", "--project-directory", dir, "-f", composePath, "ps", "--format", "json")
		if err != nil {
			si.Status = "stopped"
			si.Services = []map[string]interface{}{}
		} else {
			si.Services = parseComposePS(out)
			si.Status = computeStackStatus(si.Services)
		}

		if groups[tid] == nil {
			groups[tid] = &StackGroup{
				TemplateID:   tid,
				TemplateName: tname,
				Stacks:       []StackInfo{},
			}
			// Attach icon from builtin catalogue if known
			for _, bt := range builtinTemplates {
				if bt.ID == tid {
					groups[tid].Icon = bt.Icon
					break
				}
			}
		}
		groups[tid].Stacks = append(groups[tid].Stacks, si)
	}

	// Convert map to slice - standalone last
	result := []*StackGroup{}
	standalone := (*StackGroup)(nil)
	for _, g := range groups {
		if g.TemplateID == "__standalone__" {
			standalone = g
		} else {
			result = append(result, g)
		}
	}
	if standalone != nil {
		result = append(result, standalone)
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"groups":  result,
		"count":   len(result),
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// findTemplateSubs finds sub-directories containing docker-compose.yml.
// If orderedNames is non-empty, returns directories in that order (skipping missing).
// Otherwise returns all matching dirs sorted alphabetically.
func findTemplateSubs(root string, orderedNames []string) ([]string, error) {
	if len(orderedNames) > 0 {
		var result []string
		for _, name := range orderedNames {
			dir := filepath.Join(root, name)
			if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
				result = append(result, dir)
			}
		}
		return result, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
			result = append(result, dir)
		}
	}
	return result, nil
}

// copyDir copies all files from src to dst (non-recursive - one level).
// Subdirectories in template stacks are not followed to avoid accidental deep copies.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue // don't recurse into sub-directories of a stack
		}
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", e.Name(), err)
		}
		if err := os.WriteFile(dstPath, data, 0640); err != nil {
			return fmt.Errorf("write %s: %w", e.Name(), err)
		}
	}
	return nil
}

// substituteVars replaces ${VAR} placeholders in a file with values from vars.
// If the file does not exist, this is a no-op.
func substituteVars(path string, vars map[string]string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	content := string(data)
	for k, v := range vars {
		content = strings.ReplaceAll(content, "${"+k+"}", v)
	}
	return os.WriteFile(path, []byte(content), 0640)
}

