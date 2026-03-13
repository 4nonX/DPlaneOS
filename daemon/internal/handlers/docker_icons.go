package handlers

// docker_icons.go — Custom icon assets + image-name → Material Symbol mapping
//
// Three responsibilities:
//   1. Serve /var/lib/dplaneos/custom_icons/ as a static directory via the
//      daemon so nginx doesn't need a separate location block.
//      GET /api/assets/custom-icons/<filename>  → file contents
//      GET /api/assets/custom-icons/list        → JSON list of available files
//
//   2. Return the built-in image-name → Material Symbol mapping so the
//      frontend can resolve icons for well-known images.
//      GET /api/docker/icon-map                 → JSON map
//
// Label resolution priority (enforced entirely in the frontend):
//   1. dplaneos.icon label on the container (set in docker-compose.yaml)
//   2. Built-in image-name mapping  (GET /api/docker/icon-map)
//   3. Generic fallback: "deployed_code"

import (
	"encoding/json"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const customIconsDir = "/var/lib/dplaneos/custom_icons"

// HandleCustomIconFile serves a single icon file from the custom icons directory.
// GET /api/assets/custom-icons/<filename>
func HandleCustomIconFile(w http.ResponseWriter, r *http.Request) {
	// Extract filename from URL — strip the route prefix
	name := strings.TrimPrefix(r.URL.Path, "/api/assets/custom-icons/")
	name = filepath.Base(name) // prevent path traversal

	if name == "" || name == "." {
		respondErrorSimple(w, "filename required", http.StatusBadRequest)
		return
	}

	path := filepath.Join(customIconsDir, name)

	// Validate the resolved path is still inside customIconsDir
	abs, err := filepath.Abs(path)
	if err != nil || !strings.HasPrefix(abs, customIconsDir) {
		respondErrorSimple(w, "invalid path", http.StatusBadRequest)
		return
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			respondErrorSimple(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, "read error", http.StatusInternalServerError)
		}
		return
	}

	// Detect MIME type from extension.
	// mime.TypeByExtension relies on the OS mime.types database which may be
	// absent on minimal Linux installs (e.g. Alpine, minimal Debian).
	// The explicit fallback switch covers every extension accepted by the frontend.
	ext := strings.ToLower(filepath.Ext(name))
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		switch ext {
		case ".svg":
			ct = "image/svg+xml"
		case ".png":
			ct = "image/png"
		case ".webp":
			ct = "image/webp"
		case ".jpg", ".jpeg":
			ct = "image/jpeg"
		case ".gif":
			ct = "image/gif"
		default:
			ct = "application/octet-stream"
		}
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

// HandleCustomIconList returns a JSON array of available custom icon filenames.
// GET /api/assets/custom-icons/list
func HandleCustomIconList(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(customIconsDir)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "icons": []string{}})
			return
		}
		http.Error(w, "cannot read icons directory", http.StatusInternalServerError)
		return
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".svg" || ext == ".png" || ext == ".webp" {
			names = append(names, e.Name())
		}
	}
	if names == nil {
		names = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "icons": names})
}

// HandleDockerIconMap returns the built-in image-name → Material Symbol mapping.
// GET /api/docker/icon-map
// The map keys are lowercased image name fragments (matched with strings.Contains).
func HandleDockerIconMap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"map":     builtinIconMap,
	})
}

// builtinIconMap maps lowercased image name substrings to Material Symbol names.
// Checked in order — first match wins. Keep more-specific entries above generic ones.
var builtinIconMap = []IconMapEntry{
	// Databases
	{Match: "postgres", Icon: "database"},
	{Match: "mysql", Icon: "database"},
	{Match: "mariadb", Icon: "database"},
	{Match: "mongo", Icon: "database"},
	{Match: "redis", Icon: "memory"},
	{Match: "valkey", Icon: "memory"},
	{Match: "memcached", Icon: "memory"},
	{Match: "influxdb", Icon: "monitoring"},
	{Match: "timescale", Icon: "monitoring"},
	{Match: "clickhouse", Icon: "database"},
	{Match: "sqlite", Icon: "database"},
	{Match: "elasticsearch", Icon: "search"},
	{Match: "opensearch", Icon: "search"},
	{Match: "meilisearch", Icon: "search"},
	{Match: "typesense", Icon: "search"},

	// Message queues / streaming
	{Match: "kafka", Icon: "queue"},
	{Match: "rabbitmq", Icon: "queue"},
	{Match: "nats", Icon: "queue"},
	{Match: "mosquitto", Icon: "wifi"},
	{Match: "emqx", Icon: "wifi"},

	// Web servers / proxies
	{Match: "nginx", Icon: "http"},
	{Match: "caddy", Icon: "http"},
	{Match: "traefik", Icon: "alt_route"},
	{Match: "haproxy", Icon: "alt_route"},
	{Match: "envoy", Icon: "alt_route"},

	// Monitoring / observability
	{Match: "grafana", Icon: "bar_chart"},
	{Match: "prometheus", Icon: "monitoring"},
	{Match: "loki", Icon: "description"},
	{Match: "tempo", Icon: "timeline"},
	{Match: "alertmanager", Icon: "notification_important"},
	{Match: "netdata", Icon: "monitoring"},
	{Match: "uptime-kuma", Icon: "heart_check"},
	{Match: "zabbix", Icon: "monitoring"},
	{Match: "checkmk", Icon: "monitoring"},

	// CI/CD / DevOps
	{Match: "jenkins", Icon: "build"},
	{Match: "gitea", Icon: "code_blocks"},
	{Match: "forgejo", Icon: "code_blocks"},
	{Match: "gitlab", Icon: "code_blocks"},
	{Match: "gogs", Icon: "code_blocks"},
	{Match: "woodpecker", Icon: "build"},
	{Match: "drone", Icon: "build"},
	{Match: "act_runner", Icon: "build"},
	{Match: "harbor", Icon: "inventory_2"},
	{Match: "registry", Icon: "inventory_2"},

	// Home automation / IoT
	{Match: "homeassistant", Icon: "home"},
	{Match: "home-assistant", Icon: "home"},
	{Match: "node-red", Icon: "device_hub"},
	{Match: "zigbee2mqtt", Icon: "wifi"},
	{Match: "zwavejs", Icon: "wifi"},
	{Match: "esphome", Icon: "developer_board"},
	{Match: "frigate", Icon: "videocam"},
	{Match: "scrypted", Icon: "videocam"},

	// Media
	{Match: "jellyfin", Icon: "play_circle"},
	{Match: "plex", Icon: "play_circle"},
	{Match: "emby", Icon: "play_circle"},
	{Match: "navidrome", Icon: "music_note"},
	{Match: "audiobookshelf", Icon: "headphones"},
	{Match: "kavita", Icon: "menu_book"},
	{Match: "komga", Icon: "menu_book"},
	{Match: "calibre", Icon: "menu_book"},
	{Match: "lidarr", Icon: "music_note"},
	{Match: "radarr", Icon: "movie"},
	{Match: "sonarr", Icon: "tv"},
	{Match: "prowlarr", Icon: "travel_explore"},
	{Match: "bazarr", Icon: "subtitles"},
	{Match: "readarr", Icon: "menu_book"},

	// Download / torrent
	{Match: "qbittorrent", Icon: "download"},
	{Match: "transmission", Icon: "download"},
	{Match: "deluge", Icon: "download"},
	{Match: "sabnzbd", Icon: "download"},
	{Match: "nzbget", Icon: "download"},
	{Match: "jdownloader", Icon: "download"},

	// File sync / backup
	{Match: "syncthing", Icon: "sync"},
	{Match: "nextcloud", Icon: "cloud"},
	{Match: "seafile", Icon: "cloud"},
	{Match: "owncloud", Icon: "cloud"},
	{Match: "minio", Icon: "storage"},
	{Match: "duplicati", Icon: "backup"},
	{Match: "borgmatic", Icon: "backup"},
	{Match: "restic", Icon: "backup"},
	{Match: "rclone", Icon: "sync"},
	{Match: "filebrowser", Icon: "folder_open"},
	{Match: "filerun", Icon: "folder_open"},

	// VPN / networking
	{Match: "wireguard", Icon: "vpn_key"},
	{Match: "wg-easy", Icon: "vpn_key"},
	{Match: "openvpn", Icon: "vpn_key"},
	{Match: "tailscale", Icon: "vpn_key"},
	{Match: "headscale", Icon: "vpn_key"},
	{Match: "pihole", Icon: "dns"},
	{Match: "adguard", Icon: "dns"},
	{Match: "unbound", Icon: "dns"},
	{Match: "bind", Icon: "dns"},
	{Match: "cloudflared", Icon: "cloud"},

	// Auth / identity
	{Match: "authelia", Icon: "lock"},
	{Match: "authentik", Icon: "lock"},
	{Match: "keycloak", Icon: "lock"},
	{Match: "vaultwarden", Icon: "key"},
	{Match: "bitwarden", Icon: "key"},
	{Match: "vault", Icon: "key"},
	{Match: "lldap", Icon: "people"},
	{Match: "openldap", Icon: "people"},

	// Dashboards / portals
	{Match: "homepage", Icon: "dashboard"},
	{Match: "homarr", Icon: "dashboard"},
	{Match: "dasherr", Icon: "dashboard"},
	{Match: "flame", Icon: "dashboard"},
	{Match: "organizr", Icon: "dashboard"},
	{Match: "portainer", Icon: "deployed_code"},

	// Communication
	{Match: "matrix", Icon: "forum"},
	{Match: "synapse", Icon: "forum"},
	{Match: "element", Icon: "forum"},
	{Match: "mattermost", Icon: "forum"},
	{Match: "rocketchat", Icon: "forum"},
	{Match: "signal", Icon: "forum"},
	{Match: "ntfy", Icon: "notifications"},
	{Match: "gotify", Icon: "notifications"},

	// Infrastructure / system
	{Match: "watchtower", Icon: "update"},
	{Match: "diun", Icon: "update"},
	{Match: "dozzle", Icon: "description"},
	{Match: "whats-up-docker", Icon: "update"},
	{Match: "healthchecks", Icon: "heart_check"},
	{Match: "gatus", Icon: "heart_check"},
	{Match: "statping", Icon: "heart_check"},
	{Match: "crowdsec", Icon: "security"},
	{Match: "fail2ban", Icon: "security"},
	{Match: "unifi", Icon: "router"},
	{Match: "netboot", Icon: "developer_board"},

	// Notes / productivity
	{Match: "obsidian", Icon: "edit_note"},
	{Match: "joplin", Icon: "edit_note"},
	{Match: "outline", Icon: "edit_note"},
	{Match: "bookstack", Icon: "menu_book"},
	{Match: "wikijs", Icon: "menu_book"},
	{Match: "hedgedoc", Icon: "edit_note"},
	{Match: "vikunja", Icon: "task"},
	{Match: "planka", Icon: "task"},
	{Match: "wekan", Icon: "task"},

	// Finance
	{Match: "firefly", Icon: "payments"},
	{Match: "actual", Icon: "payments"},
	{Match: "ghostfolio", Icon: "trending_up"},
}

// IconMapEntry is a single image-name fragment → Material Symbol mapping.
type IconMapEntry struct {
	Match string `json:"match"` // lowercased substring of image name
	Icon  string `json:"icon"`  // Material Symbol name
}
