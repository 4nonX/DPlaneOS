package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"dplaned/internal/cmdutil"
	"dplaned/internal/security"
	"dplaned/internal/storageops"
)

// ImportablePool is one pool reported by `zpool import -d /dev/disk/by-id`.
type ImportablePool struct {
	Name   string `json:"name"`
	GUID   string `json:"guid"`
	State  string `json:"state"`
	Status string `json:"status,omitempty"`
	Action string `json:"action,omitempty"`
	// Config is the raw vdev tree as printed by zpool, for display.
	Config string `json:"config,omitempty"`
	// NeedsForce is true when the pool was last used by another system
	// (hostid mismatch); importing it requires -f.
	NeedsForce bool `json:"needs_force"`
}

var importKeyRe = regexp.MustCompile(`^\s*(pool|id|state|status|action|comment|see|config):\s?(.*)$`)

// parseImportScan parses the human-readable output of `zpool import`.
//
//	  pool: tank
//	    id: 15813948561712957512
//	 state: ONLINE
//	status: The pool was last accessed by another system.
//	action: The pool can be imported using its name or numeric identifier and
//	        the '-f' flag.
//	config:
//
//	        tank        ONLINE
//	          mirror-0  ONLINE
//	            ata-... ONLINE
func parseImportScan(out string) []ImportablePool {
	var pools []ImportablePool
	var cur *ImportablePool
	key := ""
	var config []string

	flush := func() {
		if cur == nil {
			return
		}
		cur.Config = strings.Trim(strings.Join(config, "\n"), "\n")
		cur.Status = strings.TrimSpace(cur.Status)
		cur.Action = strings.TrimSpace(cur.Action)
		cur.NeedsForce = strings.Contains(cur.Status, "another system") || strings.Contains(cur.Action, "'-f' flag")
		pools = append(pools, *cur)
		cur, config = nil, nil
	}

	for _, line := range strings.Split(out, "\n") {
		if m := importKeyRe.FindStringSubmatch(line); m != nil && (key != "config" || m[1] == "pool") {
			key = m[1]
			val := strings.TrimSpace(m[2])
			switch key {
			case "pool":
				flush()
				cur = &ImportablePool{Name: val}
			case "id":
				if cur != nil {
					cur.GUID = val
				}
			case "state":
				if cur != nil {
					cur.State = val
				}
			case "status":
				if cur != nil {
					cur.Status = val
				}
			case "action":
				if cur != nil {
					cur.Action = val
				}
			}
			continue
		}
		if cur == nil {
			continue
		}
		switch key {
		case "config":
			config = append(config, strings.TrimRight(line, " \t"))
		case "status":
			cur.Status += " " + strings.TrimSpace(line)
		case "action":
			cur.Action += " " + strings.TrimSpace(line)
		}
	}
	flush()

	// Drop entries without a GUID: import is always by GUID.
	valid := pools[:0]
	for _, p := range pools {
		if p.GUID != "" {
			valid = append(valid, p)
		}
	}
	return valid
}

// GroupImportGuard refuses manual imports of pools that belong to a shared
// storage group owned by another node (set from main; nil: no groups).
var GroupImportGuard func(guid, name string) (bool, string)

func importAllowed(guid, name string) (bool, string) {
	if GroupImportGuard == nil {
		return true, ""
	}
	return GroupImportGuard(guid, name)
}

// HandleListImportablePools serves GET /api/zfs/pool/importable.
func HandleListImportablePools(w http.ResponseWriter, r *http.Request) {
	out, err := cmdutil.RunZFS("zpool_import_scan", "import", "-d", "/dev/disk/by-id")
	text := string(out)
	if err != nil && !strings.Contains(text, "no pools available") {
		respondErrorSimple(w, "Scan for importable pools failed: "+strings.TrimSpace(text), http.StatusInternalServerError)
		return
	}
	pools := parseImportScan(text)
	if pools == nil {
		pools = []ImportablePool{}
	}
	respondOK(w, map[string]any{"success": true, "pools": pools})
}

// HandleImportPool serves POST /api/zfs/pool/import {guid, new_name?, force?}.
func HandleImportPool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GUID    string `json:"guid"`
		NewName string `json:"new_name"`
		Force   bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	req.NewName = strings.TrimSpace(req.NewName)
	if err := security.ValidatePoolGUID(req.GUID); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.NewName != "" {
		if err := security.ValidatePoolName(req.NewName); err != nil {
			respondErrorSimple(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if ok, why := importAllowed(req.GUID, ""); !ok {
		respondErrorSimple(w, "Import blocked: "+why, http.StatusConflict)
		return
	}

	args := []string{"import", "-d", "/dev/disk/by-id"}
	if req.Force {
		args = append(args, "-f")
	}
	args = append(args, req.GUID)
	if req.NewName != "" {
		args = append(args, req.NewName)
	}

	opID, err := storageops.Begin(registryDB, storageops.OpPoolImport, req.GUID)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusConflict)
		return
	}
	out, err := cmdutil.Run(cmdutil.TimeoutSlow, "zpool_import", args...)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		storageops.Fail(registryDB, opID, msg)
		respondErrorSimple(w, "Import failed: "+msg, http.StatusConflict)
		return
	}
	storageops.Commit(registryDB, opID)
	broadcastPoolHealthChanged()

	respondOK(w, map[string]any{"success": true, "guid": req.GUID, "name": req.NewName})
}
