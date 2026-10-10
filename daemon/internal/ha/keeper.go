// Package ha holds the node-level protection used by storage groups (Design
// 0001, ADR-0009): the watchdog that resets a storage owner which lost quorum,
// and power fencing (IPMI/Redfish, PDU). Failover itself is decided by the
// storage groups (internal/groups) on top of Corosync quorum (internal/quorum).
package ha

import (
	"database/sql"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"dplaned/internal/cmdutil"
)

// ExternalQuorum is the cluster quorum as the keeper needs it.
type ExternalQuorum struct {
	Configured    bool // a Corosync cluster is configured
	Quorate       bool // this node is in the quorate partition
	Reconfiguring bool // corosync restarts for a configuration change
}

// Keeper resets the watchdog while this node may keep running: always
// without a cluster; with a cluster while it is quorate, while corosync
// restarts for a configuration change, or while it owns no storage. A storage
// owner that lost quorum stops resetting it, and the kernel resets the node
// before another node takes its storage over (the fencing delay).
type Keeper struct {
	db     *sql.DB
	quorum func() ExternalQuorum
	owns   func() bool

	mu      sync.Mutex
	armed   bool // the watchdog is no longer reset: the node resets soon
	opened  bool
	stop    chan struct{}
	stopped bool
}

// NewKeeper returns a keeper; quorum and owns are read on every tick.
func NewKeeper(db *sql.DB, quorum func() ExternalQuorum, owns func() bool) *Keeper {
	return &Keeper{db: db, quorum: quorum, owns: owns}
}

// Start opens the watchdog (when enabled) and resets it every pet interval.
func (k *Keeper) Start() {
	cfg, err := GetWatchdogConfig(k.db)
	if err != nil || !cfg.Enable {
		return
	}
	// No hardware watchdog driver: the kernel's softdog stands in (it resets
	// the node as well, but not if the kernel itself hangs).
	if _, err := os.Stat(cfg.Device); os.IsNotExist(err) && cfg.Device == "/dev/watchdog" {
		if out, err := cmdutil.RunFast("modprobe_softdog", "softdog"); err != nil {
			log.Printf("HA WATCHDOG: no %s and softdog could not be loaded: %v: %s", cfg.Device, err, strings.TrimSpace(string(out)))
		} else {
			log.Printf("HA WATCHDOG: no hardware watchdog found; loaded softdog")
			time.Sleep(500 * time.Millisecond)
		}
	}
	if err := openWatchdog(cfg.Device); err != nil {
		log.Printf("HA WATCHDOG: cannot open %s: %v (watchdog self-fencing is off; automatic failover needs power fencing)", cfg.Device, err)
		return
	}
	log.Printf("HA WATCHDOG: opened %s (timeout %ds): a storage owner that loses quorum resets itself", cfg.Device, cfg.TimeoutSecs)
	interval := time.Duration(cfg.PetIntervalSec) * time.Second
	if interval <= 0 || interval >= time.Duration(cfg.TimeoutSecs)*time.Second/2 {
		interval = time.Duration(cfg.TimeoutSecs) * time.Second / 3
	}
	k.mu.Lock()
	k.opened, k.stop = true, make(chan struct{})
	stop := k.stop
	k.mu.Unlock()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			k.tick()
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
}

func (k *Keeper) tick() {
	if k.mayRun() {
		k.setArmed(false)
		petWatchdog()
		return
	}
	k.setArmed(true)
}

// mayRun decides whether the watchdog is reset now.
func (k *Keeper) mayRun() bool {
	if k.quorum == nil {
		return true
	}
	q := k.quorum()
	if !q.Configured || q.Quorate || q.Reconfiguring {
		return true
	}
	return k.owns == nil || !k.owns()
}

func (k *Keeper) setArmed(armed bool) {
	k.mu.Lock()
	changed := k.armed != armed
	k.armed = armed
	k.mu.Unlock()
	if !changed {
		return
	}
	if armed {
		log.Printf("HA WATCHDOG: this node owns storage and lost quorum: no longer resetting the watchdog, the node resets shortly so another node can take over")
	} else {
		log.Printf("HA WATCHDOG: quorum back: resetting the watchdog again")
	}
}

// FenceArmed reports whether the node stopped resetting its watchdog.
func (k *Keeper) FenceArmed() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.armed
}

// Restart applies a changed watchdog configuration: closes the device
// gracefully (no reset) and opens it again if still enabled.
func (k *Keeper) Restart() {
	k.mu.Lock()
	if k.opened {
		close(k.stop)
		k.opened = false
		k.mu.Unlock()
		shutdownWatchdog()
		log.Printf("HA WATCHDOG: closed for a configuration change")
	} else {
		k.mu.Unlock()
	}
	k.Start()
}
