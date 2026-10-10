package handlers

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dplaned/internal/audit"
)

// Trash / recycle bin.
//
// Every filesystem has its own trash directory at its top (within the file
// roots): /mnt/tank/.dplaneos-trash for files on the tank dataset. Moving a
// file to the trash is then a rename on the same filesystem; a single trash
// directory on the root filesystem copied every trashed file onto the
// system disk.
//
// The trash directories live on shares, so their contents are not trusted:
// a trash directory must be a real directory owned by the daemon (root) and
// not writable by others; item names are single path components; the original path read
// from an item's .meta file is checked against the file roots again before
// a restore.

const trashDirName = ".dplaneos-trash"

type TrashHandler struct{}

func NewTrashHandler() *TrashHandler { return &TrashHandler{} }

// trashDirFor returns the trash directory of the filesystem holding the
// (resolved) entry: the topmost directory above it on the same device that
// is still within a file root.
func trashDirFor(entry string) (string, error) {
	dir := filepath.Dir(entry)
	fi, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	dev, known := deviceOf(fi)
	for !isFileRoot(dir) {
		parent := filepath.Dir(dir)
		if parent == dir || !(underRoot(parent) || isFileRoot(parent)) {
			break
		}
		pfi, err := os.Stat(parent)
		if err != nil {
			break
		}
		if pdev, _ := deviceOf(pfi); known && pdev != dev {
			break
		}
		dir = parent
	}
	return filepath.Join(dir, trashDirName), nil
}

// checkTrashDir verifies (and with create, makes) a trash directory.
func checkTrashDir(dir string, create bool) error {
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) && create {
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		fi, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || !privateToDaemon(fi) {
		return fmt.Errorf("%s is not a trash directory created by D-PlaneOS (wrong type, owner or permissions); move it away", dir)
	}
	return nil
}

// trashDirs lists the existing, valid trash directories: at the file roots
// and at every filesystem mounted below them.
func trashDirs() []string {
	seen := map[string]bool{}
	var out []string
	add := func(mp string) {
		d := filepath.Join(mp, trashDirName)
		if !seen[d] && checkTrashDir(d, false) == nil {
			seen[d] = true
			out = append(out, d)
		}
	}
	for _, r := range realRoots() {
		add(r)
	}
	if f, err := os.Open("/proc/self/mounts"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				continue
			}
			mp := strings.ReplaceAll(fields[1], `\040`, " ")
			if underRoot(mp) {
				add(mp)
			}
		}
	}
	return out
}

func validTrashName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00") && !strings.HasSuffix(name, ".meta")
}

// findTrashItem returns the trash directory holding name.
func findTrashItem(name string) (string, bool) {
	for _, d := range trashDirs() {
		if _, err := os.Lstat(filepath.Join(d, name)); err == nil {
			return d, true
		}
	}
	return "", false
}

func (h *TrashHandler) MoveToTrash(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	entry, err := resolveEntry(req.Path)
	if err != nil {
		respondErrorSimple(w, "Path not allowed (pool and media mounts only)", http.StatusForbidden)
		return
	}
	if filepath.Base(entry) == trashDirName {
		respondErrorSimple(w, "The trash itself cannot be trashed", http.StatusBadRequest)
		return
	}
	if _, err := os.Lstat(entry); err != nil {
		respondErrorSimple(w, "File not found", http.StatusNotFound)
		return
	}
	dir, err := trashDirFor(entry)
	if err == nil {
		err = checkTrashDir(dir, true)
	}
	if err != nil {
		respondErrorSimple(w, "Trash unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	name := fmt.Sprintf("%s-%s_%s", time.Now().Format("20060102-150405"), hex.EncodeToString(suffix), filepath.Base(entry))
	trashPath := filepath.Join(dir, name)
	if err := writeFileNoFollow(trashPath+".meta", []byte(entry), 0600); err != nil {
		respondErrorSimple(w, "Trash unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	start := time.Now()
	if err := os.Rename(entry, trashPath); err != nil {
		_ = os.Remove(trashPath + ".meta")
		audit.LogAction("trash", user, fmt.Sprintf("Failed to trash %s: %v", entry, err), false, time.Since(start))
		respondErrorSimple(w, "Failed to move to trash: "+err.Error(), http.StatusInternalServerError)
		return
	}
	audit.LogAction("trash", user, fmt.Sprintf("Moved to trash: %s", entry), true, time.Since(start))
	respondOK(w, map[string]any{"success": true, "trash_path": trashPath, "name": name})
}

func (h *TrashHandler) ListTrash(w http.ResponseWriter, r *http.Request) {
	items := []map[string]any{}
	for _, d := range trashDirs() {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".meta") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			item := map[string]any{
				"name":       e.Name(),
				"size":       info.Size(),
				"trashed_at": info.ModTime().Format(time.RFC3339),
				"is_dir":     e.IsDir(),
				"trash_dir":  d,
			}
			if meta, err := os.ReadFile(filepath.Join(d, e.Name()+".meta")); err == nil {
				item["original_path"] = string(meta)
			}
			items = append(items, item)
		}
	}
	respondOK(w, map[string]any{"success": true, "items": items})
}

func (h *TrashHandler) RestoreFromTrash(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if !validTrashName(req.Name) {
		respondErrorSimple(w, "Invalid item name", http.StatusBadRequest)
		return
	}
	dir, ok := findTrashItem(req.Name)
	if !ok {
		respondErrorSimple(w, "Item not found in trash", http.StatusNotFound)
		return
	}
	trashPath := filepath.Join(dir, req.Name)
	meta, err := os.ReadFile(trashPath + ".meta")
	if err != nil {
		respondErrorSimple(w, "Cannot determine the original path", http.StatusInternalServerError)
		return
	}
	// Checked again: the original location must still be within the roots.
	original, err := resolveEntry(strings.TrimSpace(string(meta)))
	if err != nil {
		respondErrorSimple(w, "Original location not allowed: "+err.Error(), http.StatusForbidden)
		return
	}
	if _, err := os.Lstat(original); err == nil {
		respondErrorSimple(w, "Target path already exists: "+original, http.StatusConflict)
		return
	}
	if err := os.MkdirAll(filepath.Dir(original), 0755); err != nil {
		respondErrorSimple(w, "Cannot recreate the folder: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(trashPath, original); err != nil {
		respondErrorSimple(w, "Failed to restore: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = os.Remove(trashPath + ".meta")
	audit.LogAction("trash_restore", user, fmt.Sprintf("Restored %s to %s", req.Name, original), true, 0)
	respondOK(w, map[string]any{"success": true, "restored_to": original})
}

func (h *TrashHandler) EmptyTrash(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	start := time.Now()
	var failed []string
	for _, d := range trashDirs() {
		entries, err := os.ReadDir(d)
		if err != nil {
			failed = append(failed, d)
			continue
		}
		for _, e := range entries {
			if err := os.RemoveAll(filepath.Join(d, e.Name())); err != nil {
				failed = append(failed, filepath.Join(d, e.Name()))
			}
		}
	}
	if len(failed) > 0 {
		audit.LogAction("trash_empty", user, fmt.Sprintf("Failed: %v", failed), false, time.Since(start))
		respondErrorSimple(w, "Some items could not be removed: "+strings.Join(failed, ", "), http.StatusInternalServerError)
		return
	}
	audit.LogAction("trash_empty", user, "Trash emptied", true, time.Since(start))
	respondOK(w, map[string]any{"success": true})
}
