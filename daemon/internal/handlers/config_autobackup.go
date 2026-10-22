package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"dplaned/internal/configbackup"
	"dplaned/internal/database"

	"github.com/gorilla/mux"
)

// Daily configuration backups on a data pool.
//
// A configuration backup helps after the loss of the boot disk only if it is
// somewhere else. TrueNAS keeps daily copies on the system dataset (on a data
// pool); DPlaneOS writes one a day to <first pool>/.dplaneos-config/ (with
// the secrets key, mode 0600, like the database it opens) and keeps the
// newest autoBackupKeep. After a reinstall: import the pool, then restore
// from Settings → Maintenance → Stored backups.

const (
	autoBackupDirName = ".dplaneos-config"
	autoBackupKeep    = 14
)

var autoBackupNameRe = regexp.MustCompile(`^dplaneos-config-[A-Za-z0-9._-]+-[0-9]{8}-[0-9]{6}\.tar\.gz$`)

type storedBackup struct {
	Pool    string    `json:"pool"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Created time.Time `json:"created"`
}

// poolMounts lists imported pools with a usable root mountpoint.
var poolMounts = func() map[string]string {
	out, err := exec.Command("zfs", "list", "-H", "-d", "0", "-o", "name,mountpoint,mounted").Output()
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 3 && strings.HasPrefix(f[1], "/") && f[2] == "yes" {
			m[f[0]] = f[1]
		}
	}
	return m
}

func storedBackups() []storedBackup {
	var list []storedBackup
	for pool, mnt := range poolMounts() {
		entries, _ := os.ReadDir(filepath.Join(mnt, autoBackupDirName))
		for _, e := range entries {
			if !autoBackupNameRe.MatchString(e.Name()) {
				continue
			}
			if info, err := e.Info(); err == nil {
				list = append(list, storedBackup{Pool: pool, Name: e.Name(), Size: info.Size(), Created: info.ModTime().UTC()})
			}
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	return list
}

func storedBackupPath(pool, name string) (string, bool) {
	if !autoBackupNameRe.MatchString(name) {
		return "", false
	}
	mnt, ok := poolMounts()[pool]
	if !ok {
		return "", false
	}
	return filepath.Join(mnt, autoBackupDirName, name), true
}

// writeAutoBackup writes today's backup to the first pool (by name) and
// prunes old ones there.
func writeAutoBackup(db *sql.DB, dsn string) error {
	mounts := poolMounts()
	if len(mounts) == 0 {
		return nil // no pool: nowhere off the boot disk to put it
	}
	pools := make([]string, 0, len(mounts))
	for p := range mounts {
		pools = append(pools, p)
	}
	sort.Strings(pools)
	dir := filepath.Join(mounts[pools[0]], autoBackupDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	host, _ := os.Hostname()
	host = regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(host, "_")
	name := fmt.Sprintf("dplaneos-config-%s-%s.tar.gz", host, time.Now().UTC().Format("20060102-150405"))
	tmp := filepath.Join(dir, "."+name+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = configbackup.Create(context.Background(), db, dsn, ConfigDir, DaemonVersion, configbackup.Options{Secrets: true, Network: true}, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, name))
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if autoBackupNameRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return names[i][len(names[i])-22:] > names[j][len(names[j])-22:] }) // by timestamp
	for _, old := range names[min(len(names), autoBackupKeep):] {
		os.Remove(filepath.Join(dir, old))
	}
	return nil
}

// StartAutoConfigBackup writes a backup shortly after start and then daily.
func StartAutoConfigBackup(ctx context.Context, db *sql.DB, dsn string) {
	go func() {
		t := time.NewTimer(10 * time.Minute)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if err := writeAutoBackup(db, dsn); err != nil {
				log.Printf("CONFIG BACKUP: daily backup failed: %v", err)
				DispatchAlert("warning", "config.backup_failed", "system", "The daily configuration backup failed: "+err.Error())
			}
			t.Reset(24 * time.Hour)
		}
	}()
}

// ListStoredBackups: GET /api/system/config/backups
func (h *SystemBackupHandler) ListStoredBackups(w http.ResponseWriter, r *http.Request) {
	respondOK(w, map[string]any{"success": true, "backups": storedBackups()})
}

// DownloadStoredBackup: GET /api/system/config/backups/{pool}/{name}
func (h *SystemBackupHandler) DownloadStoredBackup(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	p, ok := storedBackupPath(v["pool"], v["name"])
	if !ok {
		respondErrorSimple(w, "No such stored backup", http.StatusNotFound)
		return
	}
	sendFile(w, p, v["name"])
}

// RestoreStoredBackup: POST /api/system/config/backups/{pool}/{name}/restore
// { "secrets": true, "network": false }
func (h *SystemBackupHandler) RestoreStoredBackup(w http.ResponseWriter, r *http.Request) {
	if h.inCluster() {
		respondErrorSimple(w, "This node is a cluster member: leave the cluster before restoring a configuration (the restore would replace its membership)", http.StatusConflict)
		return
	}
	v := mux.Vars(r)
	p, ok := storedBackupPath(v["pool"], v["name"])
	if !ok {
		respondErrorSimple(w, "No such stored backup", http.StatusNotFound)
		return
	}
	var req struct {
		Secrets *bool `json:"secrets"`
		Network bool  `json:"network"`
	}
	json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
	o := configbackup.Options{Secrets: req.Secrets == nil || *req.Secrets, Network: req.Network}
	f, err := os.Open(p)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	m, err := configbackup.Stage(f, ConfigDir, database.LatestVersion(), o)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.staged(w, r, m)
}
