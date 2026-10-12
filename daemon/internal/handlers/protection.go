package handlers

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/groups"
	"dplaned/internal/ha"
	"dplaned/internal/quorum"
)

// Protection summary and fencing policy (Design 0001 phase 3c, ADR-0009).
//
// The baseline for automatic failover is a third vote plus watchdog
// self-fencing; multihost, disk reservations and power fencing are optional
// layers. Missing layers are explained, not blocked; only automatic failover
// itself requires three votes and a fencing method.

// Fencing is the fencing available to this node for storage-group takeovers.
type Fencing struct {
	Watchdog bool
	Power    bool
	OK       bool
	Reason   string
	Delay    time.Duration // wait after the owner left before taking over
}

// fencingMargin is added to the watchdog timeout: detection jitter and the
// time the old owner needs to stop after its watchdog fired.
const fencingMargin = 15 * time.Second

// CurrentFencing reads the fencing configuration.
func CurrentFencing(db *sql.DB) Fencing {
	var f Fencing
	wd, _ := ha.GetWatchdogConfig(db)
	f.Watchdog = wd.Enable
	// Power fencing counts as this node's fencing method only when every
	// node it may take over from can be powered off.
	covered, missing := powerCoverage(db)
	f.Power = covered > 0 && len(missing) == 0
	switch {
	case f.Watchdog:
		f.OK, f.Delay = true, time.Duration(wd.TimeoutSecs)*time.Second+fencingMargin
	case f.Power:
		f.OK, f.Delay = true, fencingMargin
	default:
		f.Reason = "enable the watchdog (System › High Availability) or configure power fencing for every other node"
		if covered > 0 {
			f.Reason = "power fencing is not configured for " + strings.Join(missing, ", ") + "; enable the watchdog, or add those nodes"
		}
	}
	return f
}

// powerCoverage reports for how many of the other nodes power fencing is
// configured and enabled, and the names of those without.
func powerCoverage(db *sql.DB) (covered int, missing []string) {
	targets, _ := ha.ListPowerTargets(db)
	enabled := map[string]bool{}
	for _, t := range targets {
		if t.Enabled {
			enabled[t.Node] = true
		}
	}
	for key, name := range PowerTargetNodes() {
		if enabled[key] {
			covered++
		} else {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return covered, missing
}

// PowerFence powers off the previous owner of a storage group, the node
// with that key, through the power target configured for it. Without one
// it returns ha.ErrNoPowerTarget: the caller then relies on the watchdog,
// or does not take over. It never powers off another machine instead.
func PowerFence(db *sql.DB, owner string) error {
	return ha.PowerOff(db, owner)
}

// Layer is one line of the protection summary.
type Layer struct {
	Name   string `json:"name"`
	State  string `json:"state"` // ok, warn, off
	Detail string `json:"detail"`
}

// ProtectionHandler serves the protection summary.
type ProtectionHandler struct {
	db  *sql.DB
	mon *quorum.Monitor
	ha  *ha.Keeper
}

func NewProtectionHandler(db *sql.DB, mon *quorum.Monitor, keeper *ha.Keeper) *ProtectionHandler {
	return &ProtectionHandler{db: db, mon: mon, ha: keeper}
}

// watchdogKind reads the identity of the first watchdog device.
func watchdogKind() string {
	b, err := os.ReadFile("/sys/class/watchdog/watchdog0/identity")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Get: GET /api/ha/protection
func (h *ProtectionHandler) Get(w http.ResponseWriter, r *http.Request) {
	var layers []Layer
	in := h.mon.FreshInfo(time.Second)
	cfg, st, _, _ := h.mon.Snapshot()

	// Third vote.
	switch {
	case cfg == nil:
		layers = append(layers, Layer{"Third vote", "off", "No cluster formed. Without a cluster there is no automatic failover."})
	case cfg.QDevice == nil && len(cfg.Nodes) < 3:
		layers = append(layers, Layer{"Third vote", "off", "Two votes only: a network split leaves both nodes believing they are in charge, so failover is a manual takeover. Add a third vote (a Raspberry Pi, a small VM, another DPlaneOS system) to fail over automatically."})
	case cfg.QDevice != nil && !st.QDeviceAlive:
		layers = append(layers, Layer{"Third vote", "warn", fmt.Sprintf("Configured (%s) but not voting: check that it runs and is reachable on TCP 5403.", cfg.QDevice.Host)})
	default:
		layers = append(layers, Layer{"Third vote", "ok", fmt.Sprintf("%d of %d votes present.", st.TotalVotes, in.ExpectedVotes)})
	}

	// Watchdog.
	wd, _ := ha.GetWatchdogConfig(h.db)
	kind := watchdogKind()
	switch {
	case !wd.Enable:
		layers = append(layers, Layer{"Watchdog self-fence", "off", "Off. A node that loses quorum cannot reset itself; automatic failover then needs IPMI or PDU power fencing."})
	case strings.Contains(strings.ToLower(kind), "software"):
		layers = append(layers, Layer{"Watchdog self-fence", "warn", fmt.Sprintf("On, using the kernel's software watchdog (softdog, %ds). It resets the node as long as the kernel still runs; a hardware watchdog (most server boards, many mini PCs) also covers a hung kernel.", wd.TimeoutSecs)})
	default:
		layers = append(layers, Layer{"Watchdog self-fence", "ok", fmt.Sprintf("On (%s, %ds).", strings.TrimSpace(kind+" watchdog"), wd.TimeoutSecs)})
	}

	// Multihost on shared pools.
	gs, _ := groups.List(h.db)
	var on, off []string
	for _, g := range gs {
		if g.Topology != groups.Shared {
			continue
		}
		for _, p := range g.Pools {
			out, err := cmdutil.RunFast("zpool_get_multihost", "get", "-H", "-o", "value", "multihost", p.Name)
			switch {
			case err != nil:
				// Not imported on this node: the owner reports it.
			case strings.TrimSpace(string(bytes.TrimSpace(out))) == "on":
				on = append(on, p.Name)
			default:
				off = append(off, p.Name)
			}
		}
	}
	switch {
	case len(off) > 0:
		layers = append(layers, Layer{"ZFS multihost", "warn", fmt.Sprintf("Off on shared pool(s) %s: turn it on (Storage › Pools › Maintenance) so a pool can never be imported on two nodes at once.", strings.Join(off, ", "))})
	case len(on) > 0:
		layers = append(layers, Layer{"ZFS multihost", "ok", fmt.Sprintf("On for %s: a second import is refused while the pool is in use.", strings.Join(on, ", "))})
	default:
		layers = append(layers, Layer{"ZFS multihost", "off", "No shared-storage pools on this node."})
	}

	// Disk reservations.
	layers = append(layers, reservationLayer(h.db))

	// Power fencing.
	covered, missing := powerCoverage(h.db)
	switch {
	case covered > 0 && len(missing) == 0:
		layers = append(layers, Layer{"Power fencing", "ok", fmt.Sprintf("This node can power off every other node (%d) before it takes a group over from it.", covered)})
	case covered > 0:
		layers = append(layers, Layer{"Power fencing", "warn", "Configured for some nodes only. Not for: " + strings.Join(missing, ", ") + ". A takeover from one of those relies on the watchdog."})
	default:
		layers = append(layers, Layer{"Power fencing", "off", "Not configured (optional). Needs the BMC of each other node, or a switched PDU, reachable from this node."})
	}

	fence := CurrentFencing(h.db)
	auto := in.AutoFailover && fence.OK
	reason := in.AutoFailoverNo
	if in.AutoFailover && !fence.OK {
		reason = fence.Reason
	}
	respondOK(w, map[string]any{
		"success":              true,
		"layers":               layers,
		"auto_failover":        auto,
		"auto_failover_reason": reason,
		"failover_delay_secs":  int(fence.Delay.Seconds()),
		"self_fence_armed":     h.ha.FenceArmed(),
	})
}
