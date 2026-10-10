package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

var zvolNameRe = regexp.MustCompile(`^[a-zA-Z0-9_\-]+(/[a-zA-Z0-9_\-]+)+$`)
var zvolSizeRe = regexp.MustCompile(`^[0-9]+[KMGTP]$`)
var zvolBlockSizeRe = regexp.MustCompile(`^(512|1024|2048|4096|8192|16384|32768|65536|131072|[0-9]+[KMG])$`)

// zfsCompressionRe: the compression values ZFS accepts (plain zfs is not
// in the command whitelist, so the value is checked here).
var zfsCompressionRe = regexp.MustCompile(`^(on|off|lz4|lzjb|zle|gzip(-[1-9])?|zstd(-([1-9]|1[0-9]))?|zstd-fast(-[0-9]+)?)$`)

// Zvol represents a ZFS volume (block device)
type Zvol struct {
	Name         string `json:"name"`
	Used         string `json:"used"`
	Avail        string `json:"avail"`
	Volsize      string `json:"volsize"`
	VolBlockSize string `json:"volblocksize"`
	VolMode      string `json:"volmode"`
	Compression  string `json:"compression"`
}

// ListZvols handles GET /api/zfs/volumes
func ListZvols(w http.ResponseWriter, r *http.Request) {
	out, err := executeCommandWithTimeout(TimeoutFast, "zfs", []string{
		"list", "-H", "-t", "volume",
		"-o", "name,used,avail,volsize,volblocksize,volmode,compression",
	})
	if err != nil {
		respondErrorSimple(w, "Failed to list zvols: "+err.Error(), http.StatusInternalServerError)
		return
	}
	zvols := []Zvol{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		zvols = append(zvols, Zvol{
			Name:         fields[0],
			Used:         fields[1],
			Avail:        fields[2],
			Volsize:      fields[3],
			VolBlockSize: fields[4],
			VolMode:      fields[5],
			Compression:  fields[6],
		})
	}
	respondOK(w, map[string]any{"success": true, "volumes": zvols})
}

// CreateZvol handles POST /api/zfs/volumes
func CreateZvol(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		Size         string `json:"size"`
		VolBlockSize string `json:"blocksize,omitempty"`
		Sparse       bool   `json:"sparse"`
		Compression  string `json:"compression,omitempty"`
		VolMode      string `json:"volmode,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Size == "" {
		respondErrorSimple(w, "name and size are required", http.StatusBadRequest)
		return
	}
	if !zvolNameRe.MatchString(req.Name) {
		respondErrorSimple(w, "invalid zvol name: must be pool/name format", http.StatusBadRequest)
		return
	}
	if !zvolSizeRe.MatchString(strings.ToUpper(req.Size)) {
		respondErrorSimple(w, "invalid size format: use e.g. 10G, 500M, 1T", http.StatusBadRequest)
		return
	}
	args := []string{"create"}
	if req.Sparse {
		args = append(args, "-s")
	}
	args = append(args, "-V", req.Size)
	if req.VolBlockSize != "" {
		if !zvolBlockSizeRe.MatchString(req.VolBlockSize) {
			respondErrorSimple(w, "invalid volblocksize", http.StatusBadRequest)
			return
		}
		args = append(args, "-b", req.VolBlockSize)
	}
	if req.Compression != "" {
		if !zfsCompressionRe.MatchString(req.Compression) {
			respondErrorSimple(w, "invalid compression (e.g. lz4, zstd, gzip-6, off)", http.StatusBadRequest)
			return
		}
		args = append(args, "-o", "compression="+req.Compression)
	}
	if req.VolMode != "" && (req.VolMode == "dev" || req.VolMode == "geom" || req.VolMode == "none" || req.VolMode == "default") {
		args = append(args, "-o", "volmode="+req.VolMode)
	}
	args = append(args, req.Name)
	if _, err := executeCommandWithTimeout(TimeoutSlow, "zfs", args); err != nil {
		respondErrorSimple(w, "Failed to create zvol: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, CommandResponse{Success: true, Output: "Zvol " + req.Name + " created"})
}

// DestroyZvol handles DELETE /api/zfs/volumes
func DestroyZvol(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !zvolNameRe.MatchString(req.Name) {
		respondErrorSimple(w, "invalid zvol name", http.StatusBadRequest)
		return
	}
	args := []string{"destroy"}
	if req.Force {
		args = append(args, "-f")
	}
	args = append(args, req.Name)
	if _, err := executeCommandWithTimeout(TimeoutSlow, "zfs", args); err != nil {
		respondErrorSimple(w, "Failed to destroy zvol: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, CommandResponse{Success: true, Output: "Zvol " + req.Name + " destroyed"})
}

// ResizeZvol handles POST /api/zfs/volumes/resize
func ResizeZvol(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		NewSize string `json:"size"`
		// Shrinking truncates the block device and destroys whatever lies
		// past the new end (filesystem, partition table backup, LUN data).
		AllowShrink bool `json:"allow_shrink"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !zvolNameRe.MatchString(req.Name) {
		respondErrorSimple(w, "invalid zvol name", http.StatusBadRequest)
		return
	}
	if !zvolSizeRe.MatchString(strings.ToUpper(req.NewSize)) {
		respondErrorSimple(w, "invalid size format", http.StatusBadRequest)
		return
	}
	cur, err := executeCommandWithTimeout(TimeoutFast, "zfs", []string{"get", "-Hp", "-o", "value", "volsize", req.Name})
	if err != nil {
		respondErrorSimple(w, "Zvol not found: "+req.Name, http.StatusNotFound)
		return
	}
	if newBytes, curBytes := humanToBytes(req.NewSize), parseRawBytes(strings.TrimSpace(cur)); newBytes < curBytes && !req.AllowShrink {
		respondErrorSimple(w, "The new size is smaller than the current one: shrinking a zvol destroys the data past the new end. Send allow_shrink to do it anyway.", http.StatusConflict)
		return
	}
	if _, err := executeCommandWithTimeout(TimeoutFast, "zfs", []string{
		"set", "volsize=" + req.NewSize, req.Name,
	}); err != nil {
		respondErrorSimple(w, "Failed to resize zvol: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, CommandResponse{Success: true, Output: "Zvol " + req.Name + " resized to " + req.NewSize})
}
