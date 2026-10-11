package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"dplaned/internal/configstore"
	"dplaned/internal/diskres"
	"dplaned/internal/groups"
	"dplaned/internal/quorum"
	"dplaned/internal/scsipr"
)

// DiskReservationStatus reports the reservations on the disks of shared
// groups as this node sees them. Set from main.go (the group manager).
var DiskReservationStatus func() ([]groups.PoolReservation, error)

// reservationKeyNames maps the reservation key of every cluster node (and
// of this node) to its name, to say who holds a disk.
func reservationKeyNames(db *sql.DB) map[string]string {
	names := map[string]string{}
	if self, err := configstore.NodeID(db); err == nil && self != "" {
		names[scsipr.KeyFor(self).String()] = "this node"
	}
	if cfg, _, err := quorum.Load(db); err == nil && cfg != nil {
		self, _ := configstore.NodeID(db)
		for _, n := range cfg.Nodes {
			if n.NodeKey == "" || n.NodeKey == self {
				continue
			}
			names[scsipr.KeyFor(n.NodeKey).String()] = n.Name
		}
	}
	return names
}

type reservedDisk struct {
	diskres.Disk
	HolderNode string `json:"holder_node,omitempty"` // name of the node that holds the reservation
}

type reservedPool struct {
	Group string         `json:"group"`
	Pool  string         `json:"pool"`
	Owner string         `json:"owner"`
	Mine  bool           `json:"mine"` // this node serves the group
	Disks []reservedDisk `json:"disks"`
	// State sums the disks up: reserved (all disks, for the owner),
	// partial, unsupported (the disks have no reservations), unreserved,
	// shut_out (this node serves the group but another holds the disks).
	State  string `json:"state"`
	Detail string `json:"detail"`
	Error  string `json:"error,omitempty"`
}

// summarizeReservation decides what the disks of one pool say together.
func summarizeReservation(mine bool, disks []reservedDisk) (state, detail string) {
	count := map[string]int{}
	holders := map[string]bool{}
	for _, d := range disks {
		count[d.State]++
		if d.HolderNode != "" {
			holders[d.HolderNode] = true
		} else if d.Holder != "" {
			holders["an unknown system (key "+d.Holder+")"] = true
		}
	}
	var who []string
	for h := range holders {
		who = append(who, h)
	}
	total := len(disks)
	switch {
	case total == 0:
		return "unreserved", "No disks of this pool are visible on this node."
	case count[diskres.Unsupported] == total:
		return "unsupported", "The disks of this pool have no persistent reservations (virtual disks, NVMe, SATA behind most adapters). The watchdog and ZFS multihost protect the pool."
	case mine && count[diskres.Foreign] > 0:
		return "shut_out", fmt.Sprintf("%s holds the disks: this node has been shut out and cannot write to the pool. Restart this node; it then follows the new owner.", strings.Join(who, ", "))
	case mine && count[diskres.Held] == total:
		return "reserved", fmt.Sprintf("All %d disks are reserved for this node: they refuse writes from any other node.", total)
	case !mine && count[diskres.Foreign] == total && len(who) > 0:
		return "reserved", fmt.Sprintf("All %d disks are reserved for %s.", total, strings.Join(who, ", "))
	case count[diskres.Held]+count[diskres.Foreign] > 0:
		var parts []string
		for _, s := range []string{diskres.Held, diskres.Foreign, diskres.Shared, diskres.Unclaimed, diskres.Unsupported, diskres.Failed} {
			if n := count[s]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, s))
			}
		}
		return "partial", "Not every disk is reserved for the owner alone: " + strings.Join(parts, ", ") + "."
	default:
		return "unreserved", "The disks have reservations, but none is held. The owner takes them when it imports the pool."
	}
}

// reservedPools builds the reservation view of this node.
func reservedPools(db *sql.DB) ([]reservedPool, error) {
	out := []reservedPool{}
	if DiskReservationStatus == nil {
		return out, nil
	}
	pools, err := DiskReservationStatus()
	if err != nil {
		return nil, err
	}
	names := reservationKeyNames(db)
	self, _ := configstore.NodeID(db)
	for _, p := range pools {
		rp := reservedPool{Group: p.Group, Pool: p.Pool, Owner: p.Owner, Mine: p.Owner == self, Disks: []reservedDisk{}, Error: p.Error}
		for _, d := range p.Disks {
			rp.Disks = append(rp.Disks, reservedDisk{Disk: d, HolderNode: names[d.Holder]})
		}
		if p.Error != "" {
			rp.State, rp.Detail = "partial", "The disks could not be read: "+p.Error
		} else {
			rp.State, rp.Detail = summarizeReservation(rp.Mine, rp.Disks)
		}
		out = append(out, rp)
	}
	return out, nil
}

// GetSCSIStatus reports the disk reservations of the shared storage groups.
// GET /api/ha/scsi/status
func (h *HAHandler) GetSCSIStatus(w http.ResponseWriter, r *http.Request) {
	pools, err := reservedPools(h.db)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read the disk reservations", err)
		return
	}
	key := ""
	if self, err := configstore.NodeID(h.db); err == nil && self != "" {
		key = scsipr.KeyFor(self).String()
	}
	active := false
	for _, p := range pools {
		if p.State == "reserved" || p.State == "partial" || p.State == "shut_out" {
			active = true
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"active":  active,
		"key":     key,
		"pools":   pools,
	})
}

// reservationLayer is the line of the protection summary.
func reservationLayer(db *sql.DB) Layer {
	const name = "Disk reservations (SCSI-3 PR)"
	pools, err := reservedPools(db)
	switch {
	case err != nil:
		return Layer{name, "warn", "The reservations could not be read: " + err.Error()}
	case len(pools) == 0:
		return Layer{name, "off", "No shared-storage pools. Disks shared between nodes are reserved for the node that serves them, where the disks support it."}
	}
	var good, weak, none, bad []string
	for _, p := range pools {
		switch p.State {
		case "reserved":
			good = append(good, p.Pool)
		case "unsupported":
			none = append(none, p.Pool)
		case "shut_out":
			bad = append(bad, p.Pool+": "+p.Detail)
		default:
			weak = append(weak, p.Pool+": "+p.Detail)
		}
	}
	switch {
	case len(bad) > 0:
		return Layer{name, "warn", strings.Join(bad, " ")}
	case len(weak) > 0:
		return Layer{name, "warn", strings.Join(weak, " ")}
	case len(good) > 0 && len(none) == 0:
		return Layer{name, "ok", fmt.Sprintf("Active for %s: the disks themselves refuse writes from every node but the owner, so a node that is hung or cut off cannot damage the pool.", strings.Join(good, ", "))}
	case len(good) > 0:
		return Layer{name, "warn", fmt.Sprintf("Active for %s; the disks of %s have no reservations.", strings.Join(good, ", "), strings.Join(none, ", "))}
	default:
		return Layer{name, "off", fmt.Sprintf("The disks of %s have no persistent reservations (SAS disks and some enterprise SATA disks have them). The protection comes from the watchdog and multihost.", strings.Join(none, ", "))}
	}
}
