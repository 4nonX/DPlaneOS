package handlers

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
)

// SupportBundleHandler generates a diagnostic archive for support cases.
// POST /api/system/support-bundle
//
// Streams a .tar.gz directly to the response — no temp files written to disk.
// Collection is best-effort: failures in individual sections are logged and
// included as error notes inside the archive, never cause a 500.
type SupportBundleHandler struct {
	db      *sql.DB
	version string
}

func NewSupportBundleHandler(db *sql.DB, version string) *SupportBundleHandler {
	return &SupportBundleHandler{db: db, version: version}
}

// bundleSection is one file's worth of content in the archive.
type bundleSection struct {
	name    string // path inside the tar (e.g. "zfs/pool-status.txt")
	content []byte
}

// collect runs cmd+args and returns the output as a section.
// On error the section still exists, containing the error message — so the
// bundle is always complete even when some commands aren't available.
func collectCmd(name string, timeout func(string, ...string) ([]byte, error), cmd string, args ...string) bundleSection {
	out, err := timeout(cmd, args...)
	if err != nil {
		return bundleSection{
			name:    name,
			content: []byte(fmt.Sprintf("ERROR running %s %v: %v\n\n%s", cmd, args, err, string(out))),
		}
	}
	return bundleSection{name: name, content: out}
}

// collectFile reads a file from disk.
func collectFile(name, path string, maxBytes int64) bundleSection {
	f, err := os.Open(path)
	if err != nil {
		return bundleSection{name: name, content: []byte(fmt.Sprintf("ERROR opening %s: %v\n", path, err))}
	}
	defer f.Close()

	buf := make([]byte, maxBytes)
	// Seek to end-maxBytes to get the tail (most recent entries).
	if fi, err := f.Stat(); err == nil && fi.Size() > maxBytes {
		f.Seek(-maxBytes, io.SeekEnd)
	}
	n, _ := f.Read(buf)
	return bundleSection{name: name, content: buf[:n]}
}

// collectAuditTail queries the last 500 rows from audit_logs.
func collectAuditTail(db *sql.DB) bundleSection {
	const name = "audit/audit_logs_tail.json"
	if db == nil {
		return bundleSection{name: name, content: []byte("[]")}
	}

	rows, err := db.Query(`
		SELECT id, timestamp, user, action, resource, details, ip_address, success
		FROM audit_logs
		ORDER BY id DESC
		LIMIT 500
	`)
	if err != nil {
		return bundleSection{name: name, content: []byte(fmt.Sprintf(`{"error": %q}`, err.Error()))}
	}
	defer rows.Close()

	type row struct {
		ID        int64  `json:"id"`
		Timestamp string `json:"timestamp"`
		User      string `json:"user"`
		Action    string `json:"action"`
		Resource  string `json:"resource"`
		Details   string `json:"details"`
		IPAddress string `json:"ip_address"`
		Success   int    `json:"success"`
	}

	var entries []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Timestamp, &r.User, &r.Action, &r.Resource, &r.Details, &r.IPAddress, &r.Success); err != nil {
			continue
		}
		entries = append(entries, r)
	}

	data, _ := json.MarshalIndent(entries, "", "  ")
	return bundleSection{name: name, content: data}
}

// collectSMART enumerates block devices and runs smartctl on each.
func collectSMART() []bundleSection {
	// Get list of physical disk names (type=disk, not partitions/loops)
	out, err := cmdutil.RunFast("lsblk", "-dno", "NAME,TYPE")
	if err != nil {
		return []bundleSection{{
			name:    "smart/error.txt",
			content: []byte(fmt.Sprintf("lsblk failed: %v\n", err)),
		}}
	}

	var sections []bundleSection
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "disk" {
			continue
		}
		dev := "/dev/" + fields[0]
		s := collectCmd(
			"smart/"+fields[0]+".txt",
			cmdutil.RunMedium,
			"smartctl", "-a", dev,
		)
		sections = append(sections, s)
	}
	if len(sections) == 0 {
		sections = append(sections, bundleSection{
			name:    "smart/no-disks.txt",
			content: []byte("No block devices of type=disk found via lsblk.\n"),
		})
	}
	return sections
}

// GenerateBundle streams a gzip tar of all diagnostic sections.
// POST /api/system/support-bundle
func (h *SupportBundleHandler) GenerateBundle(w http.ResponseWriter, r *http.Request) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	filename := fmt.Sprintf("dplaneos-support-%s-%s.tar.gz", hostname, ts)

	// Collect all sections before streaming so we can set Content-Disposition.
	// Most commands are fast (TimeoutFast = 10s); total wall time is bounded.
	var sections []bundleSection

	// ── ZFS ────────────────────────────────────────────────────────────
	sections = append(sections,
		collectCmd("zfs/pool-status.txt", cmdutil.RunZFS, "zpool", "status", "-v"),
		collectCmd("zfs/pool-list.txt", cmdutil.RunZFS, "zpool", "list", "-v"),
		collectCmd("zfs/dataset-list.txt", cmdutil.RunZFS, "zfs", "list", "-r", "-o", "name,used,avail,refer,mountpoint,compression,quota"),
		collectCmd("zfs/pool-events.txt", cmdutil.RunFast, "zpool", "events", "-H"),
	)

	// ── System ──────────────────────────────────────────────────────────
	sections = append(sections,
		collectCmd("system/uname.txt", cmdutil.RunFast, "uname", "-a"),
		collectCmd("system/free.txt", cmdutil.RunFast, "free", "-m"),
		collectCmd("system/df.txt", cmdutil.RunFast, "df", "-h"),
		collectCmd("system/uptime.txt", cmdutil.RunFast, "uptime"),
		collectCmd("system/dmesg-errors.txt", cmdutil.RunFast, "dmesg", "-T", "--level=err,warn"),
	)

	// ── Network ─────────────────────────────────────────────────────────
	sections = append(sections,
		collectCmd("network/ip-addr.txt", cmdutil.RunFast, "ip", "addr"),
		collectCmd("network/ip-route.txt", cmdutil.RunFast, "ip", "route"),
	)

	// ── Logs ────────────────────────────────────────────────────────────
	sections = append(sections,
		collectCmd("logs/dplaned-journal.txt", cmdutil.RunMedium,
			"journalctl", "-n", "1000", "--no-pager", "-u", "dplaned"),
		collectCmd("logs/nixos-rebuild-journal.txt", cmdutil.RunMedium,
			"journalctl", "-n", "500", "--no-pager", "-u", "nixos-rebuild"),
		collectFile("logs/dplaned.log.tail", "/var/log/dplaneos/dplaned.log", 512*1024),
	)

	// ── NixOS (best-effort — only on NixOS) ────────────────────────────
	if isNixOS() {
		sections = append(sections,
			collectCmd("nixos/version.txt", cmdutil.RunFast, "nixos-version"),
			collectCmd("nixos/generations.txt", cmdutil.RunMedium,
				"nix-env", "--list-generations", "--profile", "/nix/var/nix/profiles/system"),
		)
		// Include current NixOS config if it exists
		for _, p := range []string{"/etc/nixos/configuration.nix", "/etc/nixos/flake.nix"} {
			if _, statErr := os.Stat(p); statErr == nil {
				sections = append(sections, collectFile("nixos/"+p[len("/etc/nixos/"):], p, 256*1024))
			}
		}
	}

	// ── SMART ───────────────────────────────────────────────────────────
	sections = append(sections, collectSMART()...)

	// ── Audit log tail (from DB) ─────────────────────────────────────────
	sections = append(sections, collectAuditTail(h.db))

	// ── Bundle metadata ──────────────────────────────────────────────────
	meta := map[string]interface{}{
		"generated_at":    time.Now().UTC().Format(time.RFC3339),
		"hostname":        hostname,
		"dplaneos_version": h.version,
		"sections":        len(sections),
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	sections = append([]bundleSection{{name: "bundle-meta.json", content: metaJSON}}, sections...)

	// ── Stream tar.gz ────────────────────────────────────────────────────
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.WriteHeader(http.StatusOK)

	gw := gzip.NewWriter(w)
	tw := tar.NewWriter(gw)

	for _, s := range sections {
		hdr := &tar.Header{
			Name:    filename[:len(filename)-len(".tar.gz")] + "/" + s.name,
			Mode:    0644,
			Size:    int64(len(s.content)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			log.Printf("support-bundle: tar header error for %s: %v", s.name, err)
			continue
		}
		if _, err := tw.Write(s.content); err != nil {
			log.Printf("support-bundle: tar write error for %s: %v", s.name, err)
		}
	}

	if err := tw.Close(); err != nil {
		log.Printf("support-bundle: tar close error: %v", err)
	}
	if err := gw.Close(); err != nil {
		log.Printf("support-bundle: gzip close error: %v", err)
	}
}
