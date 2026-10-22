package handlers

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/configbackup"
	"dplaned/internal/database"
)

// Configuration backup and restore (see package configbackup).
type SystemBackupHandler struct {
	db  *sql.DB
	dsn string
}

func NewSystemBackupHandler(db *sql.DB, dsn string) *SystemBackupHandler {
	return &SystemBackupHandler{db: db, dsn: dsn}
}

func boolParam(v string, def bool) bool {
	switch v {
	case "1", "true", "on":
		return true
	case "0", "false", "off":
		return false
	}
	return def
}

// sendFile streams a finished file as an attachment.
func sendFile(w http.ResponseWriter, path, name string) {
	f, err := os.Open(path)
	if err != nil {
		respondErrorSimple(w, "Backup not available: "+err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil {
		w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	io.Copy(w, f) //nolint:errcheck
}

// DownloadBackup: GET /api/system/config/backup?secrets=1&network=1
// The archive is written completely before anything is sent: a failing
// pg_dump answers with an error instead of a truncated download.
func (h *SystemBackupHandler) DownloadBackup(w http.ResponseWriter, r *http.Request) {
	o := configbackup.Options{
		Secrets: boolParam(r.URL.Query().Get("secrets"), true),
		Network: boolParam(r.URL.Query().Get("network"), true),
	}
	tmp, err := os.CreateTemp("", "dplaneos-config-*.tar.gz")
	if err != nil {
		respondErrorSimple(w, "Failed to create temp file", http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmp.Name())
	m, err := configbackup.Create(r.Context(), h.db, h.dsn, ConfigDir, DaemonVersion, o, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		log.Printf("CONFIG BACKUP: %v", err)
		respondErrorSimple(w, "Backup failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	host := strings.NewReplacer("/", "_", " ", "_").Replace(m.Hostname)
	audit.LogAction("config_backup", r.Header.Get("X-User"),
		fmt.Sprintf("configuration backup (%d files, secrets key: %v, network: %v)", len(m.Files), o.Secrets, o.Network), true, 0)
	sendFile(w, tmp.Name(), fmt.Sprintf("dplaneos-config-%s-%s.tar.gz", host, m.CreatedAt.Format("20060102-150405")))
}

// DownloadPrevious: GET /api/system/config/backup/previous
// The configuration as it was before the last restore.
func (h *SystemBackupHandler) DownloadPrevious(w http.ResponseWriter, r *http.Request) {
	sendFile(w, configbackup.PreviousPath, "dplaneos-config-before-restore.tar.gz")
}

// inCluster: a cluster member's database holds the membership; a restore
// would replace it.
func (h *SystemBackupHandler) inCluster() bool {
	var n int
	return h.db.QueryRow(`SELECT COUNT(*) FROM cluster_quorum`).Scan(&n) == nil && n > 0
}

// restartDaemon applies a staged restore (dplaned reads it at start). The
// restart runs as its own transient unit: a systemctl started from here sits
// in dplaned's cgroup and is killed by the restart it asked for.
var restartDaemon = func() {
	time.Sleep(time.Second)
	if out, err := exec.Command("systemd-run", "--no-block", "--collect", "--quiet",
		"--unit=dplaneos-config-restore-restart", "systemctl", "restart", "dplaned.service").CombinedOutput(); err != nil {
		log.Printf("CONFIG RESTORE: restart failed: %v: %s", err, out)
	}
}

// RestoreBackup: POST /api/system/config/restore (multipart: backup, secrets, network)
// Checks and stages the archive, then restarts the daemon, which applies it.
func (h *SystemBackupHandler) RestoreBackup(w http.ResponseWriter, r *http.Request) {
	if h.inCluster() {
		respondErrorSimple(w, "This node is a cluster member: leave the cluster before restoring a configuration (the restore would replace its membership)", http.StatusConflict)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		respondErrorSimple(w, "Failed to parse upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	f, _, err := r.FormFile("backup")
	if err != nil {
		respondErrorSimple(w, "Missing 'backup' file field in form", http.StatusBadRequest)
		return
	}
	defer f.Close()
	o := configbackup.Options{
		Secrets: boolParam(r.FormValue("secrets"), true),
		Network: boolParam(r.FormValue("network"), false),
	}
	m, err := configbackup.Stage(f, ConfigDir, database.LatestVersion(), o)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.staged(w, r, m)
}

// staged answers a staged restore and restarts the daemon to apply it.
func (h *SystemBackupHandler) staged(w http.ResponseWriter, r *http.Request, m *configbackup.Manifest) {
	audit.LogAction("config_restore", r.Header.Get("X-User"),
		fmt.Sprintf("configuration restore staged (backup of %s, %s, DPlaneOS %s); restarting", m.Hostname, m.CreatedAt.Format(time.RFC3339), m.Version), true, 0)
	respondOK(w, map[string]any{"success": true, "manifest": m, "restarting": true,
		"message": "The backup was checked. The daemon restarts and applies it; log in again in a minute."})
	go restartDaemon()
}

// RestoreStatus: GET /api/system/config/restore
func (h *SystemBackupHandler) RestoreStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"success": true, "pending": configbackup.Pending(), "in_cluster": h.inCluster()}
	if res := configbackup.ReadResult(); res != nil {
		resp["last_result"] = res
	}
	if _, err := os.Stat(configbackup.PreviousPath); err == nil {
		resp["previous_available"] = true
	}
	respondOK(w, resp)
}

// CancelRestore: DELETE /api/system/config/restore
func (h *SystemBackupHandler) CancelRestore(w http.ResponseWriter, r *http.Request) {
	if err := configbackup.Cancel(); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true})
}

// ApplyStagedRestore runs at daemon start, after the database answers and
// before migrations (an older backup is then migrated up).
func ApplyStagedRestore(db *sql.DB, dsn string) {
	res := configbackup.ApplyStaged(db, dsn, ConfigDir, DaemonVersion)
	if res == nil {
		return
	}
	if res.Status == "ok" {
		// Sessions of the backup would log in again whoever had logged out since.
		if _, err := db.Exec(`DELETE FROM sessions`); err != nil {
			log.Printf("CONFIG RESTORE: clearing sessions: %v", err)
		}
	}
	if res.Status == "ok" && res.Cluster {
		// Cluster membership is not portable (and the cluster components
		// read it later in the startup): join again if needed.
		if _, err := db.Exec(`DELETE FROM cluster_quorum`); err != nil {
			log.Printf("CONFIG RESTORE: clearing the cluster membership of the backup: %v", err)
		}
	}
	if res.Status == "ok" {
		log.Printf("CONFIG RESTORE: applied the backup of %s (%s, DPlaneOS %s): database and %d files", res.FromHost, res.CreatedAt.Format(time.RFC3339), res.FromVersion, res.Files)
	} else {
		log.Printf("CONFIG RESTORE: FAILED: %s", res.Error)
	}
}

// FinishRestore reports the result of a restore once (alert channels are
// set up by then).
func FinishRestore(db *sql.DB) {
	res := configbackup.ReadResult()
	if res == nil || res.Reported {
		return
	}
	switch res.Status {
	case "ok":
		msg := fmt.Sprintf("Configuration restored from the backup of %s (%s, DPlaneOS %s).", res.FromHost, res.CreatedAt.Format("2006-01-02 15:04"), res.FromVersion)
		if !res.Secrets {
			msg += " The secrets key was not restored: stored passwords (SMTP, SMB, replication) that were sealed on the other machine must be entered again."
		}
		if res.Cluster {
			msg += " The backup came from a cluster member: this node is not a member; join it again if needed."
		}
		DispatchAlert("info", "config.restored", "system", msg)
	case "failed":
		DispatchAlert("critical", "config.restore_failed", "system", "Configuration restore failed: "+res.Error)
	}
	res.Reported = true
	configbackup.WriteResult(res) //nolint:errcheck
}
