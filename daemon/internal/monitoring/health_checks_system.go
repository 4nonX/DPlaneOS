package monitoring

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
)

// The checks run every few seconds: each is one cheap command or read.

func result(name string, st HealthStatus, reason string, details map[string]interface{}) SubsystemHealth {
	return SubsystemHealth{Name: name, Status: st, Reason: reason, Details: details, CheckedAt: time.Now()}
}

// checkZFS: "zpool status -x" names only pools with problems.
func (c *Checker) checkZFS(ctx context.Context) SubsystemHealth {
	out, err := cmdutil.RunFast("zpool_status", "status", "-x")
	text := strings.TrimSpace(string(out))
	switch {
	case err != nil && strings.Contains(text, "no pools available"):
		return result("zfs", HealthOK, "no pools", nil)
	case err != nil:
		return result("zfs", HealthUnknown, "zpool status failed: "+firstLine(text, err), nil)
	case text == "all pools are healthy" || strings.Contains(text, "no pools available"):
		return result("zfs", HealthOK, text, nil)
	}
	st := HealthDegraded
	if strings.Contains(text, "UNAVAIL") || strings.Contains(text, "FAULTED") || strings.Contains(text, "SUSPENDED") {
		st = HealthUnavailable
	}
	return result("zfs", st, poolProblemSummary(text), map[string]interface{}{"zpool_status": text})
}

// poolProblemSummary names the pools in "zpool status -x" output and their state.
func poolProblemSummary(text string) string {
	var parts []string
	var pool string
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(l, "pool:"); ok {
			pool = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(l, "state:"); ok && pool != "" {
			parts = append(parts, pool+" "+strings.TrimSpace(v))
			pool = ""
		}
	}
	if len(parts) == 0 {
		return "pool problems reported"
	}
	return strings.Join(parts, ", ")
}

// checkDocker: the daemon answers (skipped when Docker is not installed).
func (c *Checker) checkDocker(ctx context.Context) SubsystemHealth {
	if _, err := exec.LookPath("docker"); err != nil {
		return result("docker", HealthOK, "not installed", nil)
	}
	out, err := cmdutil.RunFast("docker_cli", "ps", "--format", "{{.ID}}")
	if err != nil {
		return result("docker", HealthUnavailable, "docker daemon not responding: "+firstLine(string(out), err), nil)
	}
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			n++
		}
	}
	return result("docker", HealthOK, fmt.Sprintf("%d running containers", n), map[string]interface{}{"running": n})
}

// checkPostgres: the daemon's own database answers.
func (c *Checker) checkPostgres(ctx context.Context) SubsystemHealth {
	c.mu.RLock()
	db := c.db
	c.mu.RUnlock()
	if db == nil {
		return result("postgres", HealthUnknown, "no database connection configured", nil)
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := db.PingContext(pctx); err != nil {
		return result("postgres", HealthUnavailable, "database not reachable: "+err.Error(), nil)
	}
	stats := db.Stats()
	return result("postgres", HealthOK, "", map[string]interface{}{
		"ping_ms": time.Since(start).Milliseconds(), "open_connections": stats.OpenConnections, "in_use": stats.InUse,
	})
}

// checkNetwork: an interface is up with an address, and there is a default route.
func (c *Checker) checkNetwork(ctx context.Context) SubsystemHealth {
	ifaces, err := net.Interfaces()
	if err != nil {
		return result("network", HealthUnknown, err.Error(), nil)
	}
	var up []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if addrs, _ := ifc.Addrs(); len(addrs) > 0 {
			up = append(up, ifc.Name)
		}
	}
	if len(up) == 0 {
		return result("network", HealthUnavailable, "no interface is up with an address", nil)
	}
	details := map[string]interface{}{"interfaces": up}
	if !hasDefaultRoute() {
		return result("network", HealthDegraded, "no default route", details)
	}
	return result("network", HealthOK, "", details)
}

func hasDefaultRoute() bool {
	for _, f := range []string{"/proc/net/route", "/proc/net/ipv6_route"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n")[1:] {
			fields := strings.Fields(l)
			if f == "/proc/net/route" && len(fields) > 1 && fields[1] == "00000000" {
				return true
			}
			if f == "/proc/net/ipv6_route" && len(fields) > 1 && fields[0] == strings.Repeat("0", 32) && fields[1] == "00" {
				return true
			}
		}
	}
	return false
}

// checkBonding: every bond has at least one link, and reports links that are down.
func (c *Checker) checkBonding(ctx context.Context) SubsystemHealth {
	files, _ := filepath.Glob("/proc/net/bonding/*")
	if len(files) == 0 {
		return result("bonding", HealthOK, "no bonds", nil)
	}
	st := HealthOK
	var problems []string
	details := map[string]interface{}{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		bond := filepath.Base(f)
		up, down := bondLinks(string(b))
		details[bond] = map[string]interface{}{"links_up": up, "links_down": down}
		switch {
		case up == 0:
			st = HealthUnavailable
			problems = append(problems, bond+": no link up")
		case down > 0:
			if st == HealthOK {
				st = HealthDegraded
			}
			problems = append(problems, fmt.Sprintf("%s: %d of %d links down", bond, down, up+down))
		}
	}
	return result("bonding", st, strings.Join(problems, "; "), details)
}

// bondLinks counts the slave interfaces of a /proc/net/bonding file by MII status.
func bondLinks(text string) (up, down int) {
	inSlave := false
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "Slave Interface:") {
			inSlave = true
			continue
		}
		if inSlave && strings.HasPrefix(l, "MII Status:") {
			if strings.Contains(l, "up") {
				up++
			} else {
				down++
			}
			inSlave = false
		}
	}
	return up, down
}

func firstLine(s string, err error) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" && err != nil {
		return err.Error()
	}
	return s
}
