// Package configbackup writes and restores a complete configuration backup.
//
// The former "database backup" was a pg_dump only and its restore ran
// pg_restore --clean into the live database under the running daemon. Half
// of the configuration is not in the database: snapshot, replication and
// rsync schedules, firewall rules, NVMe-oF targets, iSCSI, certificates,
// stack files, NixOS settings, replication SSH keys and the keys that open
// the sealed secrets and verify the audit log. A restore onto a new machine
// lost all of that, and the sealed secrets (SMB passwords, SMTP, Telegram)
// could not be opened. A failed pg_restore left a half-dropped database.
//
// A backup is now a tar.gz with a manifest, the database dump and those
// files (TrueNAS: the configuration database plus the secret seed). A
// restore is checked and staged; the daemon applies it at its next start,
// before migrations, in one database transaction (all or nothing), keeps a
// copy of what it replaced, and then runs migrations and its usual startup
// reconciliation (shares, accounts, smb.conf, exports, firewall, iSCSI,
// timers) from the restored state.
package configbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Format identifies the archive layout.
const Format = "dplaneos-config/1"

// Limits: configuration, not data.
const (
	maxFileSize  = 4 << 20   // a single file; larger files (e.g. app data under a stack) are skipped
	maxTotalSize = 256 << 20 // the whole archive, uncompressed
)

// Item is a file or directory tree that belongs to the configuration.
type Item struct {
	Path    string // absolute path on the system
	Kind    string // "secrets" | "network" | "" (always)
	Comment string
}

// Paths that may be changed in tests. Root prefixes every path of Items on
// disk; the archive holds the paths without it.
var (
	Root         = ""
	StageDir     = "/var/lib/dplaneos/restore-pending"
	PreviousPath = "/var/lib/dplaneos/before-restore.tar.gz"
	ResultPath   = "/var/lib/dplaneos/restore-result.json"
	SecretsKey   = "/var/lib/dplaneos/secrets.key"
)

// Items lists what a backup holds. configDir is the daemon's -config-dir.
// Not included, because the daemon derives them at startup or they are
// machine-specific: smb.conf, exports, the nftables ruleset, share-group
// bookkeeping (accounts are created from the users in the database), git
// stack checkouts (cloned again), cluster membership (corosync), the
// applied-NixOS markers (a restored NixOS setting shows as pending until it
// is applied) and update/restore bookkeeping.
func Items(configDir string) []Item {
	return []Item{
		{Path: configDir, Comment: "schedules, power management, certificates, ACME account, rclone remotes"},
		{Path: "/var/lib/dplaneos/dplane-state.json", Comment: "NixOS settings (applied with Settings → Apply)"},
		{Path: "/var/lib/dplaneos/firewall-rules.json", Comment: "firewall rules"},
		{Path: "/var/lib/dplaneos/nvmet-targets.json", Comment: "NVMe-oF exports"},
		{Path: "/var/lib/dplaneos/stacks", Comment: "Docker stack files"},
		{Path: "/var/lib/dplaneos/gitops/state.yaml", Comment: "GitOps state"},
		{Path: "/var/lib/dplaneos/ssh", Comment: "replication SSH keys"},
		{Path: "/var/lib/dplaneos/minio", Comment: "MinIO settings"},
		{Path: "/var/lib/dplaneos/ftp", Comment: "FTP settings"},
		{Path: "/etc/target", Comment: "iSCSI (LIO) configuration"},
		{Path: "/var/lib/dplaneos/audit.key", Comment: "audit log key (verifies the restored audit log)"},
		{Path: SecretsKey, Kind: "secrets", Comment: "opens the sealed secrets in the database"},
		{Path: "/var/lib/dplaneos/network-state.json", Kind: "network", Comment: "network configuration"},
		{Path: "/etc/systemd/network", Kind: "network", Comment: "network configuration (05-dplane-* files)"},
		{Path: "/etc/systemd/resolved.conf.d/50-dplane.conf", Kind: "network", Comment: "DNS servers"},
	}
}

// Options select the optional parts of a backup or restore.
type Options struct {
	Secrets bool // the secrets key (without it, sealed secrets cannot be opened on another machine)
	Network bool // network configuration (interface names may differ on other hardware)
}

// Manifest describes an archive.
type Manifest struct {
	Format        string    `json:"format"`
	Version       string    `json:"version"` // DPlaneOS version that wrote it
	SchemaVersion int64     `json:"schema_version"`
	Hostname      string    `json:"hostname"`
	CreatedAt     time.Time `json:"created_at"`
	Secrets       bool      `json:"secrets"`
	Network       bool      `json:"network"`
	Cluster       bool      `json:"cluster"` // the node was a cluster member
	Files         []string  `json:"files"`   // absolute paths
	Skipped       []string  `json:"skipped,omitempty"`
	DBSHA256      string    `json:"db_sha256"`
}

// itemKindAllowed reports whether a file at p may be written by a restore
// with opts, and which item it belongs to.
func itemFor(items []Item, p string) (Item, bool) {
	for _, it := range items {
		if p == it.Path || strings.HasPrefix(p, strings.TrimSuffix(it.Path, "/")+"/") {
			return it, true
		}
	}
	return Item{}, false
}

func wanted(it Item, o Options) bool {
	switch it.Kind {
	case "secrets":
		return o.Secrets
	case "network":
		return o.Network
	}
	return true
}

// networkFile: only DPlaneOS's own networkd files, not the distribution's.
func networkFile(p string) bool {
	if strings.HasPrefix(p, "/etc/systemd/network/") {
		return strings.HasPrefix(filepath.Base(p), "05-dplane-")
	}
	return true
}

// collect walks the items and returns the regular files to back up.
func collect(items []Item, o Options) (files []string, skipped []string) {
	for _, it := range items {
		if !wanted(it, o) {
			continue
		}
		filepath.WalkDir(Root+it.Path, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck
			if err != nil {
				return nil // missing items are fine
			}
			p = filepath.ToSlash(strings.TrimPrefix(p, Root))
			if d.IsDir() || !d.Type().IsRegular() { // symlinks: NixOS-managed, not ours
				return nil
			}
			if it.Kind == "network" && !networkFile(p) {
				return nil
			}
			if info, err := d.Info(); err == nil && info.Size() > maxFileSize {
				skipped = append(skipped, p)
				return nil
			}
			files = append(files, p)
			return nil
		})
	}
	sort.Strings(files)
	return files, skipped
}

// Dumper runs pg_dump; replaced in tests.
var Dumper = func(ctx context.Context, dsn string, w io.Writer) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--no-owner", "--no-privileges", "--dbname="+dsn)
	cmd.Stdout, cmd.Stderr = w, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Create writes a complete backup to w. Nothing is written to w when the
// database dump fails (callers write to a temporary file, then send it).
func Create(ctx context.Context, db *sql.DB, dsn, configDir, version string, o Options, w io.Writer) (*Manifest, error) {
	var dump bytes.Buffer
	if err := Dumper(ctx, dsn, &dump); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(dump.Bytes())
	m := &Manifest{Format: Format, Version: version, CreatedAt: time.Now().UTC(),
		Secrets: o.Secrets, Network: o.Network, DBSHA256: hex.EncodeToString(sum[:])}
	m.Hostname, _ = os.Hostname()
	db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&m.SchemaVersion) //nolint:errcheck
	var n int
	if db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cluster_quorum`).Scan(&n) == nil && n > 0 {
		m.Cluster = true
	}
	files, skipped := collect(Items(configDir), o)
	m.Files, m.Skipped = files, skipped

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: m.CreatedAt}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	var total int64
	var kept []string
	for _, f := range files {
		data, err := os.ReadFile(Root + f)
		if err != nil {
			m.Skipped = append(m.Skipped, f)
			continue
		}
		if total += int64(len(data)); total > maxTotalSize {
			return nil, fmt.Errorf("the configuration is larger than %d MB (large files under %s?)", maxTotalSize>>20, filepath.Dir(f))
		}
		info, _ := os.Stat(Root + f)
		mode := int64(0o600)
		if info != nil {
			mode = int64(info.Mode().Perm())
		}
		if err := add("files"+f, mode, data); err != nil {
			return nil, err
		}
		kept = append(kept, f)
	}
	m.Files = kept
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := add("manifest.json", 0o644, mb); err != nil {
		return nil, err
	}
	if err := add("db.dump", 0o600, dump.Bytes()); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return m, gz.Close()
}

// Lister checks a dump with pg_restore --list; replaced in tests.
var Lister = func(dumpPath string) error {
	out, err := exec.Command("pg_restore", "--list", dumpPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("not a readable database dump: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Stage checks an uploaded archive and stages it for the next daemon start.
// latestSchema is the newest migration this daemon knows: a backup from a
// newer DPlaneOS is refused (its schema cannot be migrated down).
func Stage(r io.Reader, configDir string, latestSchema int64, o Options) (*Manifest, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, errors.New("not a DPlaneOS configuration backup (expected a .tar.gz)")
	}
	tmp := StageDir + ".tmp"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "files"), 0o700); err != nil {
		return nil, err
	}
	fail := func(e error) (*Manifest, error) { os.RemoveAll(tmp); return nil, e }
	items := Items(configDir)
	tr := tar.NewReader(gz)
	var total int64
	var m *Manifest
	var staged []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(fmt.Errorf("damaged archive: %w", err))
		}
		if h.Typeflag != tar.TypeReg {
			return fail(fmt.Errorf("unexpected entry %q in the archive", h.Name))
		}
		if total += h.Size; total > maxTotalSize {
			return fail(errors.New("the archive is larger than a configuration backup can be"))
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxTotalSize))
		if err != nil {
			return fail(fmt.Errorf("damaged archive: %w", err))
		}
		switch {
		case h.Name == "manifest.json":
			m = &Manifest{}
			if err := json.Unmarshal(data, m); err != nil {
				return fail(fmt.Errorf("manifest: %w", err))
			}
		case h.Name == "db.dump":
			if err := os.WriteFile(filepath.Join(tmp, "db.dump"), data, 0o600); err != nil {
				return fail(err)
			}
		case strings.HasPrefix(h.Name, "files/"):
			p := path.Clean("/" + strings.TrimPrefix(h.Name, "files/"))
			if p != "/"+strings.TrimPrefix(h.Name, "files/") {
				return fail(fmt.Errorf("unsafe path %q in the archive", h.Name))
			}
			it, ok := itemFor(items, p)
			if !ok || (it.Kind == "network" && !networkFile(p)) {
				return fail(fmt.Errorf("%s is not part of a configuration backup", p))
			}
			if !wanted(it, o) {
				continue
			}
			dst := filepath.Join(tmp, "files", filepath.FromSlash(p))
			os.MkdirAll(filepath.Dir(dst), 0o700) //nolint:errcheck
			if err := os.WriteFile(dst, data, fs.FileMode(h.Mode)&0o777); err != nil {
				return fail(err)
			}
			staged = append(staged, p)
		default:
			return fail(fmt.Errorf("unexpected entry %q in the archive", h.Name))
		}
	}
	if m == nil || m.Format != Format {
		return fail(errors.New("not a DPlaneOS configuration backup (no manifest)"))
	}
	dump := filepath.Join(tmp, "db.dump")
	data, err := os.ReadFile(dump)
	if err != nil {
		return fail(errors.New("the backup has no database dump"))
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != m.DBSHA256 {
		return fail(errors.New("the database dump does not match its checksum (damaged backup)"))
	}
	if m.SchemaVersion > latestSchema {
		return fail(fmt.Errorf("the backup is from a newer DPlaneOS (%s, schema %d; this system knows %d): update first", m.Version, m.SchemaVersion, latestSchema))
	}
	if err := Lister(dump); err != nil {
		return fail(err)
	}
	if o.Secrets && !m.Secrets {
		o.Secrets = false
	}
	if o.Network && !m.Network {
		o.Network = false
	}
	sort.Strings(staged)
	staging := struct {
		Manifest *Manifest `json:"manifest"`
		Options  Options   `json:"options"`
		Files    []string  `json:"files"`
	}{m, o, staged}
	b, _ := json.MarshalIndent(staging, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, "staging.json"), b, 0o600); err != nil {
		return fail(err)
	}
	os.RemoveAll(StageDir)
	if err := os.Rename(tmp, StageDir); err != nil {
		return fail(err)
	}
	return m, nil
}

// Pending reports a staged restore.
func Pending() bool {
	_, err := os.Stat(filepath.Join(StageDir, "staging.json"))
	return err == nil
}

// Cancel removes a staged restore.
func Cancel() error { return os.RemoveAll(StageDir) }

// Result of the last applied restore (read by the daemon and the UI).
type Result struct {
	Status      string    `json:"status"` // ok | failed
	Error       string    `json:"error,omitempty"`
	FromHost    string    `json:"from_host"`
	FromVersion string    `json:"from_version"`
	CreatedAt   time.Time `json:"backup_created_at"`
	AppliedAt   time.Time `json:"applied_at"`
	Files       int       `json:"files"`
	Secrets     bool      `json:"secrets"`
	Network     bool      `json:"network"`
	Cluster     bool      `json:"cluster"`
	Previous    string    `json:"previous"` // backup of the replaced configuration (restorable)
	Reported    bool      `json:"reported,omitempty"`
}

// ReadResult returns the last restore's result, or nil.
func ReadResult() *Result {
	b, err := os.ReadFile(ResultPath)
	if err != nil {
		return nil
	}
	var r Result
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// WriteResult stores r.
func WriteResult(r *Result) error {
	b, _ := json.MarshalIndent(r, "", "  ")
	return os.WriteFile(ResultPath, b, 0o600)
}

// DBRestorer replaces the database contents with the dump in one
// transaction; replaced in tests.
var DBRestorer = func(dsn, dumpPath string) error {
	// pg_restore renders the dump as SQL; psql runs the drop of the current
	// objects and the restore as one transaction, so a failure leaves the
	// database as it was (pg_restore --clean into the live database did not).
	script, err := exec.Command("pg_restore", "--no-owner", "--no-privileges", "--file=-", dumpPath).Output()
	if err != nil {
		return fmt.Errorf("pg_restore: %w", err)
	}
	var in bytes.Buffer
	in.WriteString(dropAllSQL)
	in.Write(script)
	cmd := exec.Command("psql", "--no-psqlrc", "--quiet", "--single-transaction", "-v", "ON_ERROR_STOP=1", "--dbname="+dsn)
	cmd.Stdin = &in
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restore failed, the database is unchanged: %v: %s", err, lastLines(string(out), 8))
	}
	return nil
}

// dropAllSQL removes the database's own objects in the public schema (the
// schema itself may belong to another role). Objects of extensions
// (pgcrypto's functions) stay: the dump re-creates extensions with IF NOT
// EXISTS.
const dropAllSQL = `SET client_min_messages = warning;
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT c.relname, c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
           WHERE n.nspname = 'public' AND c.relkind IN ('r','p','v','m','f') AND NOT EXISTS
             (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e') LOOP
    EXECUTE format(CASE r.relkind WHEN 'v' THEN 'DROP VIEW IF EXISTS public.%I CASCADE'
                                  WHEN 'm' THEN 'DROP MATERIALIZED VIEW IF EXISTS public.%I CASCADE'
                                  WHEN 'f' THEN 'DROP FOREIGN TABLE IF EXISTS public.%I CASCADE'
                                  ELSE 'DROP TABLE IF EXISTS public.%I CASCADE' END, r.relname);
  END LOOP;
  FOR r IN SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
           WHERE n.nspname = 'public' AND c.relkind = 'S' AND NOT EXISTS
             (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e') LOOP
    EXECUTE format('DROP SEQUENCE IF EXISTS public.%I CASCADE', r.relname);
  END LOOP;
  FOR r IN SELECT p.oid::regprocedure AS sig FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
           WHERE n.nspname = 'public' AND NOT EXISTS
             (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e') LOOP
    EXECUTE format('DROP ROUTINE IF EXISTS %s CASCADE', r.sig);
  END LOOP;
  FOR r IN SELECT t.typname FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
           WHERE n.nspname = 'public' AND t.typtype IN ('e','c','d') AND NOT EXISTS
             (SELECT 1 FROM pg_class c WHERE c.reltype = t.oid) AND NOT EXISTS
             (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_type'::regclass AND d.objid = t.oid AND d.deptype = 'e') LOOP
    EXECUTE format('DROP TYPE IF EXISTS public.%I CASCADE', r.typname);
  END LOOP;
END $$;
`

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ApplyStaged applies a staged restore. The daemon calls it at startup once
// the database answers, before migrations and before it reads any of the
// restored files. It returns nil when nothing was staged.
func ApplyStaged(db *sql.DB, dsn, configDir, version string) *Result {
	b, err := os.ReadFile(filepath.Join(StageDir, "staging.json"))
	if err != nil {
		return nil
	}
	var st struct {
		Manifest *Manifest `json:"manifest"`
		Options  Options   `json:"options"`
		Files    []string  `json:"files"`
	}
	res := &Result{Status: "failed", AppliedAt: time.Now().UTC()}
	finish := func() *Result {
		WriteResult(res) //nolint:errcheck
		os.RemoveAll(StageDir)
		return res
	}
	if err := json.Unmarshal(b, &st); err != nil || st.Manifest == nil {
		res.Error = "the staged restore is damaged"
		return finish()
	}
	m := st.Manifest
	res.FromHost, res.FromVersion, res.CreatedAt = m.Hostname, m.Version, m.CreatedAt
	res.Secrets, res.Network, res.Cluster = st.Options.Secrets, st.Options.Network, m.Cluster

	// Keep what is replaced as a complete backup (restorable like any other).
	tmpPrev := PreviousPath + ".tmp"
	prev, err := os.OpenFile(tmpPrev, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = Create(context.Background(), db, dsn, configDir, version, Options{Secrets: true, Network: true}, prev)
		if cerr := prev.Close(); err == nil {
			err = cerr
		}
	}
	if err == nil {
		err = os.Rename(tmpPrev, PreviousPath)
	}
	if err != nil {
		os.Remove(tmpPrev)
		res.Error = "could not back up the current configuration first, nothing was restored: " + err.Error()
		return finish()
	}
	res.Previous = PreviousPath
	if err := DBRestorer(dsn, filepath.Join(StageDir, "db.dump")); err != nil {
		res.Error = err.Error()
		return finish()
	}
	// Files: write each one atomically.
	var errs []string
	for _, p := range st.Files {
		src := filepath.Join(StageDir, "files", filepath.FromSlash(p))
		data, err := os.ReadFile(src)
		if err != nil {
			errs = append(errs, p+": "+err.Error())
			continue
		}
		info, _ := os.Stat(src)
		dst := Root + p
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			errs = append(errs, p+": "+err.Error())
			continue
		}
		tmp := dst + ".restore-tmp"
		if err := os.WriteFile(tmp, data, info.Mode().Perm()); err != nil {
			errs = append(errs, p+": "+err.Error())
			continue
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			errs = append(errs, p+": "+err.Error())
			continue
		}
		res.Files++
	}
	if len(errs) > 0 {
		res.Error = "the database was restored, but these files were not: " + strings.Join(errs, "; ")
		return finish()
	}
	res.Status = "ok"
	return finish()
}
