package zfs

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ScrubScanInfo represents parsed ZFS scan (scrub/resilver) information.
type ScrubScanInfo struct {
	InProgress  bool    `json:"in_progress"`
	PercentDone float64 `json:"percent_done"`
	ETA         string  `json:"eta"`
	BytesDone   string  `json:"bytes_done"`
	Errors      int     `json:"errors"`
	Completed   bool    `json:"completed"`
	CompletedAt string  `json:"completed_at,omitempty"`
	RawScanLine string  `json:"raw_scan_line"`
}

// GetPoolScanLine runs `zpool status` and extracts the scan section.
func GetPoolScanLine(pool string) (string, error) {
	cmd := exec.Command("zpool", "status", "-P", pool)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to get pool status: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	output := stdout.String()
	var scanLines []string
	lines := strings.Split(output, "\n")
	inScan := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "scan:") {
			inScan = true
			scanLines = append(scanLines, trimmed)
			continue
		}
		if inScan {
			// Continuation lines are indented and don't start a new top-level field (which contain ':')
			// unless it's a 'scan:' continuation.
			if strings.HasPrefix(line, "\t") || (len(line) > 0 && line[0] == ' ') {
				if !strings.Contains(trimmed, ":") || strings.HasPrefix(trimmed, "scan:") {
					scanLines = append(scanLines, trimmed)
					continue
				}
			}
			inScan = false
		}
	}

	return strings.Join(scanLines, " "), nil
}

// ParseScanLine parses a `zpool status` scan: line.
// It handles both in-progress and completed resilver/scrub lines.
func ParseScanLine(rawLine string) ScrubScanInfo {
	info := ScrubScanInfo{RawScanLine: rawLine}

	// In-progress pattern: "X.XXG done, XX.XX% done, ETA HH:MM:SS"
	pctRe := regexp.MustCompile(`([\d.]+)%\s+done`)
	etaRe := regexp.MustCompile(`ETA\s+(\S+)`)
	bytesRe := regexp.MustCompile(`([\d.]+[KMGT]?)\s+done`)

	if strings.Contains(rawLine, "in progress") {
		info.InProgress = true
		if m := pctRe.FindStringSubmatch(rawLine); len(m) > 1 {
			info.PercentDone, _ = strconv.ParseFloat(m[1], 64)
		}
		if m := etaRe.FindStringSubmatch(rawLine); len(m) > 1 {
			info.ETA = m[1]
		}
		if m := bytesRe.FindStringSubmatch(rawLine); len(m) > 1 {
			info.BytesDone = m[1]
		}
		return info
	}

	// Completed scrub: "scrub repaired X in HH:MM:SS with N errors on ..."
	// Completed resilver: "resilvered X in HH:MM:SS with N errors on ..."
	completedRe := regexp.MustCompile(`(?:resilvered|scrub repaired)\s+([\d.]+[KMGT]?)\s+in\s+\S+\s+with\s+(\d+)\s+errors?\s+on\s+(.+)`)
	if m := completedRe.FindStringSubmatch(rawLine); len(m) > 3 {
		info.Completed = true
		info.BytesDone = m[1]
		info.Errors, _ = strconv.Atoi(m[2])
		info.CompletedAt = strings.TrimSpace(m[3])
		return info
	}

	return info
}
