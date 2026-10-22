package zfs

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ZED reports the end of a scrub or resilver as an info event without its
// result: a scrub that found damaged files or repaired data alerted no one.
// ScanFinding reads the pool's status after the scan.

var (
	scanErrorsRe = regexp.MustCompile(`with (\d+) errors`)
	repairedRe   = regexp.MustCompile(`repaired (\S+) in`)
	dataErrRe    = regexp.MustCompile(`^errors: (\d+) data errors`)
)

// ScanFinding evaluates `zpool status` text after a scrub or resilver: level
// "" when all is well, "warning" when data was repaired or a device reported
// errors, "critical" when files are damaged.
func ScanFinding(pool, status string) (level, msg string) {
	var scan string
	dataErrors := 0
	var bad []string
	inConfig := false
	for _, line := range strings.Split(status, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "scan:"):
			scan = strings.TrimSpace(strings.TrimPrefix(t, "scan:"))
		case strings.HasPrefix(t, "config:"):
			inConfig = true
		case strings.HasPrefix(t, "errors:"):
			inConfig = false
			if m := dataErrRe.FindStringSubmatch(t); m != nil {
				dataErrors, _ = strconv.Atoi(m[1])
			}
		case inConfig:
			// NAME STATE READ WRITE CKSUM
			f := strings.Fields(t)
			if len(f) >= 5 && f[0] != "NAME" {
				if f[2] != "0" || f[3] != "0" || f[4] != "0" {
					bad = append(bad, fmt.Sprintf("%s (read %s, write %s, checksum %s)", f[0], f[2], f[3], f[4]))
				}
			}
		}
	}
	scanErrors := 0
	if m := scanErrorsRe.FindStringSubmatch(scan); m != nil {
		scanErrors, _ = strconv.Atoi(m[1])
	}
	repaired := ""
	if m := repairedRe.FindStringSubmatch(scan); m != nil && m[1] != "0B" && m[1] != "0" {
		repaired = m[1]
	}
	switch {
	case dataErrors > 0 || scanErrors > 0:
		n := max(dataErrors, scanErrors)
		return "critical", fmt.Sprintf("Pool '%s': %d data errors - files are damaged and could not be repaired. "+
			"List them with zpool status -v %s and restore them from a backup or snapshot.", pool, n, pool)
	case repaired != "" || len(bad) > 0:
		msg := fmt.Sprintf("Pool '%s': ", pool)
		if repaired != "" {
			msg += "the scan repaired " + repaired + " of damaged data. "
		}
		if len(bad) > 0 {
			msg += "Devices with errors: " + strings.Join(bad, "; ") + ". A disk that keeps having errors should be replaced; zpool clear " + pool + " resets the counters."
		}
		return "warning", strings.TrimSpace(msg)
	}
	return "", ""
}

// checkScan alerts on the result of a finished scrub or resilver.
func checkScan(pool, kind string) {
	if !isValidPoolName(pool) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "zpool", "status", pool).Output()
	if err != nil {
		return
	}
	if level, msg := ScanFinding(pool, string(out)); level != "" {
		event := "pool.scrub_error"
		if kind == "resilver_finish" {
			event = "pool.resilver_error"
		}
		dispatch(level, event, pool, msg)
	}
}
