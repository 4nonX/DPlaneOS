package ha

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/libzfs"
)

// PromotionStep is the outcome of one step of a promotion.
type PromotionStep struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Skipped  bool   `json:"skipped,omitempty"`
	Critical bool   `json:"critical,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// PromotionResult reports every step. Failed is set when a critical step
// failed (the pools could not be imported): the node does not serve the data
// and later steps were not attempted. Degraded is set when the pools are
// imported but another step failed (a service did not restart, a dataset
// stayed read-only): the node serves, but needs attention.
type PromotionResult struct {
	Candidate  string          `json:"candidate"`
	Leader     string          `json:"leader"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at"`
	Steps      []PromotionStep `json:"steps"`
	Failed     bool            `json:"failed"`
	Degraded   bool            `json:"degraded"`
}

// Problems lists the failed steps as "name: detail".
func (r *PromotionResult) Problems() []string {
	var out []string
	for _, s := range r.Steps {
		if !s.OK && !s.Skipped {
			out = append(out, s.Name+": "+s.Detail)
		}
	}
	return out
}

// Err is nil when every step succeeded or was skipped.
func (r *PromotionResult) Err() error {
	if p := r.Problems(); len(p) > 0 {
		return errors.New(strings.Join(p, "; "))
	}
	return nil
}

func (r *PromotionResult) add(s PromotionStep) {
	r.Steps = append(r.Steps, s)
	if s.OK || s.Skipped {
		log.Printf("HA Failover: %s: ok%s", s.Name, suffix(s.Detail))
		return
	}
	if s.Critical {
		r.Failed = true
	} else {
		r.Degraded = true
	}
	log.Printf("HA Failover: %s FAILED: %s", s.Name, s.Detail)
}

func suffix(d string) string {
	if d == "" {
		return ""
	}
	return " (" + d + ")"
}

func stepErr(name string, critical bool, err error) PromotionStep {
	if err == nil {
		return PromotionStep{Name: name, OK: true, Critical: critical}
	}
	return PromotionStep{Name: name, Critical: critical, Detail: err.Error()}
}

// promoteOps are the side effects of a promotion; replaced in tests.
type promoteOps struct {
	importAll      func() error
	listDatasets   func() ([]string, error)
	datasetGet     func(ds, prop string) (string, error)
	datasetSet     func(ds, prop, val string) error
	datasetPromote func(ds string) error
	run            func(name string, args ...string) error
	patroni        func(candidate, leader string) error
}

func runCmd(name string, args ...string) error {
	out, err := cmdutil.RunMedium(name, args...)
	if err != nil {
		return fmt.Errorf("%v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

var defaultPromoteOps = promoteOps{
	importAll: libzfs.PoolImportAll,
	listDatasets: func() ([]string, error) {
		out, err := cmdutil.RunFast("zfs_list_names", "list", "-H", "-o", "name")
		if err != nil {
			return nil, fmt.Errorf("%v: %s", err, bytes.TrimSpace(out))
		}
		var ds []string
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				ds = append(ds, l)
			}
		}
		return ds, nil
	},
	datasetGet:     libzfs.DatasetGet,
	datasetSet:     libzfs.DatasetSet,
	datasetPromote: libzfs.DatasetPromote,
	run:            runCmd,
	patroni:        patroniFailover,
}

// patroniFailover asks the local Patroni to make candidate the primary.
func patroniFailover(candidate, leader string) error {
	body, _ := json.Marshal(map[string]string{"candidate": candidate, "leader": leader})
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("http://localhost:8008/failover", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// Patroni answers 412 when the candidate already is the leader.
	if strings.Contains(strings.ToLower(string(msg)), "already") {
		return nil
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
}

// ExecutePromotion makes this node serve the storage: import the pools, make
// replicated datasets writable, take over CTDB, restart the NAS services and
// move the Patroni primary. Every step is reported in the result; nothing is
// only logged. If the pools cannot be imported the remaining steps are not
// attempted (Failed).
func ExecutePromotion(candidate, leader string) *PromotionResult {
	return executePromotion(defaultPromoteOps, candidate, leader)
}

func executePromotion(ops promoteOps, candidate, leader string) *PromotionResult {
	r := &PromotionResult{Candidate: candidate, Leader: leader, StartedAt: time.Now()}
	defer func() { r.FinishedAt = time.Now() }()

	// 1. Import the pools (zpool import -a -f -d /dev/disk/by-id).
	if err := ops.importAll(); err != nil {
		r.add(stepErr("import pools", true, err))
		return r
	}
	r.add(PromotionStep{Name: "import pools", OK: true, Critical: true})

	// 2. Replicated datasets: writable, clones promoted.
	datasets, err := ops.listDatasets()
	if err != nil {
		r.add(stepErr("list datasets", false, err))
	} else {
		var problems []string
		changed := 0
		for _, ds := range datasets {
			if ro, err := ops.datasetGet(ds, "readonly"); err != nil {
				problems = append(problems, fmt.Sprintf("%s: reading readonly: %v", ds, err))
			} else if strings.TrimSpace(ro) == "on" {
				if err := ops.datasetSet(ds, "readonly", "off"); err != nil {
					problems = append(problems, fmt.Sprintf("%s stays read-only: %v", ds, err))
				} else {
					changed++
				}
			}
			if origin, err := ops.datasetGet(ds, "origin"); err != nil {
				problems = append(problems, fmt.Sprintf("%s: reading origin: %v", ds, err))
			} else if o := strings.TrimSpace(origin); o != "-" && o != "" {
				if err := ops.datasetPromote(ds); err != nil {
					problems = append(problems, fmt.Sprintf("%s: clone not promoted: %v", ds, err))
				} else {
					changed++
				}
			}
		}
		if len(problems) > 0 {
			r.add(PromotionStep{Name: "make datasets writable", Detail: strings.Join(problems, "; ")})
		} else {
			r.add(PromotionStep{Name: "make datasets writable", OK: true, Detail: fmt.Sprintf("%d changed", changed)})
		}
	}

	// 3. CTDB (clustered Samba), only where it is set up.
	if ops.run("systemctl_ha_ctdb_enabled", "is-enabled", "ctdb") != nil {
		r.add(PromotionStep{Name: "take over CTDB", Skipped: true, Detail: "CTDB not enabled"})
	} else if err := ops.run("systemctl_ha_ctdb_start", "start", "ctdb"); err != nil {
		r.add(stepErr("take over CTDB", false, err))
	} else {
		time.Sleep(2 * time.Second) // CTDB detects the role change and takes the public IPs
		r.add(PromotionStep{Name: "take over CTDB", OK: true})
	}

	// 4. NAS services that serve the pools.
	r.add(stepErr("restart SMB (samba-smbd)", false, ops.run("systemctl_ha_smbd", "reload-or-restart", "samba-smbd")))
	r.add(stepErr("restart NetBIOS (samba-nmbd)", false, ops.run("systemctl_ha_nmbd", "reload-or-restart", "samba-nmbd")))
	r.add(stepErr("restart NFS", false, ops.run("systemctl_ha_nfs", "reload-or-restart", "nfs-server")))
	r.add(stepErr("restart Docker", false, ops.run("systemctl_restart_docker", "restart", "docker")))

	// 5. Patroni primary (shared database of today's HA pairs).
	r.add(stepErr("move the database primary (Patroni)", false, ops.patroni(candidate, leader)))
	return r
}
