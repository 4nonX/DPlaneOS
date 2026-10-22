package configbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dplaned/internal/database"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// testDB creates a database owned by an ordinary role (as on NixOS:
// ensureDBOwnership, not a superuser) and returns a handle and its DSN.
func testDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set: database-backed test")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	b := make([]byte, 5)
	rand.Read(b) //nolint:errcheck
	name := "cb_" + hex.EncodeToString(b)
	if _, err := admin.Exec(`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cb_owner') THEN CREATE ROLE cb_owner LOGIN NOSUPERUSER PASSWORD 'cb_owner'; END IF; END $$`); err != nil {
		t.Skipf("cannot create a role: %v", err)
	}
	if _, err := admin.Exec(`CREATE DATABASE ` + name + ` OWNER cb_owner`); err != nil {
		t.Skipf("cannot create a database: %v", err)
	}
	t.Cleanup(func() { admin.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`) }) //nolint:errcheck
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("cb_owner", "cb_owner")
	u.Path = "/" + name
	own := u.String()
	db, err := sql.Open("pgx", own)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.RunMigrations(db); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return db, own
}

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755) //nolint:errcheck
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func withRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldRoot, oldStage, oldPrev, oldRes := Root, StageDir, PreviousPath, ResultPath
	Root = root
	StageDir = filepath.Join(root, "stage")
	PreviousPath = filepath.Join(root, "before-restore.tar.gz")
	ResultPath = filepath.Join(root, "restore-result.json")
	t.Cleanup(func() { Root, StageDir, PreviousPath, ResultPath = oldRoot, oldStage, oldPrev, oldRes })
	return root
}

func TestBackupAndRestore(t *testing.T) {
	db, dsn := testDB(t)
	root := withRoot(t)
	const cfg = "/etc/dplaneos"

	// The configuration at backup time.
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('alice', 'x', 'user')`); err != nil {
		t.Fatal(err)
	}
	write(t, root+cfg+"/snapshot-schedules.json", `[{"name":"daily"}]`)
	write(t, root+cfg+"/ssl/a.crt", "CERT")
	write(t, root+"/var/lib/dplaneos/secrets.key", "KEY-1")
	write(t, root+"/var/lib/dplaneos/audit.key", "AUDIT-1")
	write(t, root+"/var/lib/dplaneos/stacks/web/docker-compose.yml", "services: {}")
	write(t, root+"/var/lib/dplaneos/stacks/web/data/big.bin", strings.Repeat("x", maxFileSize+1))
	write(t, root+"/etc/systemd/network/05-dplane-eth0.network", "[Match]\nName=eth0\n")
	write(t, root+"/etc/systemd/network/10-distro.network", "not ours")
	write(t, root+"/var/lib/dplaneos/share-groups.json", "derived, not backed up")
	os.Symlink("/nix/store/x", root+cfg+"/nix-managed.conf") //nolint:errcheck

	var buf bytes.Buffer
	m, err := Create(context.Background(), db, dsn, cfg, "15.2.0", Options{Secrets: true, Network: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	files := strings.Join(m.Files, " ")
	for _, want := range []string{cfg + "/snapshot-schedules.json", cfg + "/ssl/a.crt", "/var/lib/dplaneos/secrets.key",
		"/var/lib/dplaneos/audit.key", "/var/lib/dplaneos/stacks/web/docker-compose.yml", "/etc/systemd/network/05-dplane-eth0.network"} {
		if !strings.Contains(files, want) {
			t.Errorf("backup lacks %s: %v", want, m.Files)
		}
	}
	for _, not := range []string{"10-distro", "share-groups", "nix-managed", "big.bin"} {
		if strings.Contains(files, not) {
			t.Errorf("backup holds %s", not)
		}
	}
	if !strings.Contains(strings.Join(m.Skipped, " "), "big.bin") || m.SchemaVersion != database.LatestVersion() {
		t.Errorf("manifest: skipped %v, schema %d", m.Skipped, m.SchemaVersion)
	}

	// Changes after the backup.
	db.Exec(`UPDATE users SET role = 'admin' WHERE username = 'alice'`)                          //nolint:errcheck
	db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('mallory', 'x', 'user')`) //nolint:errcheck
	db.Exec(`CREATE TABLE stray (id int)`)                                                       //nolint:errcheck
	write(t, root+cfg+"/snapshot-schedules.json", `[]`)
	os.Remove(root + cfg + "/ssl/a.crt")
	write(t, root+"/var/lib/dplaneos/secrets.key", "KEY-2")
	write(t, root+"/etc/systemd/network/05-dplane-eth0.network", "[Match]\nName=enp1s0\n")

	// Restore without the network settings (other hardware).
	if _, err := Stage(bytes.NewReader(buf.Bytes()), cfg, database.LatestVersion(), Options{Secrets: true}); err != nil {
		t.Fatal(err)
	}
	if !Pending() {
		t.Fatal("not staged")
	}
	res := ApplyStaged(db, dsn, cfg, "15.2.0")
	if res == nil || res.Status != "ok" {
		t.Fatalf("restore: %+v", res)
	}
	if Pending() {
		t.Error("still staged after applying")
	}
	var role string
	var n int
	db.QueryRow(`SELECT role FROM users WHERE username = 'alice'`).Scan(&role)    //nolint:errcheck
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE username = 'mallory'`).Scan(&n) //nolint:errcheck
	if role != "user" || n != 0 {
		t.Errorf("database not restored: alice %q, mallory %d", role, n)
	}
	if err := db.QueryRow(`SELECT 1 FROM stray`).Scan(&n); err == nil {
		t.Error("a table created after the backup survived the restore")
	}
	if got := read(t, root+cfg+"/snapshot-schedules.json"); got != `[{"name":"daily"}]` {
		t.Errorf("schedules: %s", got)
	}
	if read(t, root+cfg+"/ssl/a.crt") != "CERT" || read(t, root+"/var/lib/dplaneos/secrets.key") != "KEY-1" {
		t.Error("certificate or secrets key not restored")
	}
	if got := read(t, root+"/etc/systemd/network/05-dplane-eth0.network"); !strings.Contains(got, "enp1s0") {
		t.Errorf("network restored although not selected: %s", got)
	}
	if r := ReadResult(); r == nil || r.Status != "ok" || r.Previous != PreviousPath || r.Secrets != true || r.Network {
		t.Errorf("result: %+v", r)
	}

	// The configuration before the restore was kept as a restorable backup.
	prev, err := os.ReadFile(PreviousPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(bytes.NewReader(prev), cfg, database.LatestVersion(), Options{Secrets: true, Network: true}); err != nil {
		t.Fatalf("the before-restore backup does not stage: %v", err)
	}
	if res := ApplyStaged(db, dsn, cfg, "15.2.0"); res == nil || res.Status != "ok" {
		t.Fatalf("undo: %+v", res)
	}
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE username = 'mallory'`).Scan(&n) //nolint:errcheck
	if n != 1 || read(t, root+"/var/lib/dplaneos/secrets.key") != "KEY-2" || !strings.Contains(read(t, root+"/etc/systemd/network/05-dplane-eth0.network"), "enp1s0") {
		t.Errorf("undo did not bring back the state before the restore (mallory %d)", n)
	}
}

func TestRestoreIsAllOrNothing(t *testing.T) {
	src, srcDSN := testDB(t)
	dst, dstDSN := testDB(t)
	withRoot(t)
	// The backup holds a schema that already exists on the target: the
	// restore fails after the drop of the current tables, which must roll back.
	src.Exec(`CREATE SCHEMA extra; CREATE TABLE extra.t (id int)`)                             //nolint:errcheck
	dst.Exec(`CREATE SCHEMA extra`)                                                            //nolint:errcheck
	dst.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('keep', 'x', 'user')`) //nolint:errcheck
	var buf bytes.Buffer
	if _, err := Create(context.Background(), src, srcDSN, "/etc/dplaneos", "15.2.0", Options{}, &buf); err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(bytes.NewReader(buf.Bytes()), "/etc/dplaneos", database.LatestVersion(), Options{}); err != nil {
		t.Fatal(err)
	}
	res := ApplyStaged(dst, dstDSN, "/etc/dplaneos", "15.2.0")
	if res == nil || res.Status != "failed" || !strings.Contains(res.Error, "unchanged") {
		t.Fatalf("expected a failed restore: %+v", res)
	}
	var n int
	if err := dst.QueryRow(`SELECT COUNT(*) FROM users WHERE username = 'keep'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the database changed after a failed restore: %v %d", err, n)
	}
	if Pending() {
		t.Error("a failed restore stays staged (would fail at every start)")
	}
}

func archive(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}) //nolint:errcheck
		tw.Write([]byte(body))                                                                              //nolint:errcheck
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestStageRefuses(t *testing.T) {
	withRoot(t)
	oldLister := Lister
	Lister = func(string) error { return nil }
	t.Cleanup(func() { Lister = oldLister })
	dump := "DUMP"
	sum := sha256.Sum256([]byte(dump))
	manifest := func(schema int64) string {
		return `{"format":"` + Format + `","schema_version":` + strconv.FormatInt(schema, 10) + `,"db_sha256":"` + hex.EncodeToString(sum[:]) + `"}`
	}
	cases := map[string]map[string]string{
		"traversal":    {"manifest.json": manifest(1), "db.dump": dump, "files/../etc/shadow": "x"},
		"not ours":     {"manifest.json": manifest(1), "db.dump": dump, "files/etc/shadow": "x"},
		"distro net":   {"manifest.json": manifest(1), "db.dump": dump, "files/etc/systemd/network/10-x.network": "x"},
		"newer schema": {"manifest.json": manifest(999), "db.dump": dump},
		"no manifest":  {"db.dump": dump},
		"bad checksum": {"manifest.json": manifest(1), "db.dump": "OTHER"},
	}
	for name, entries := range cases {
		if _, err := Stage(bytes.NewReader(archive(t, entries)), "/etc/dplaneos", 50, Options{Secrets: true, Network: true}); err == nil {
			t.Errorf("%s: staged", name)
		}
		if Pending() {
			t.Fatalf("%s: left a staged restore", name)
		}
	}
	if _, err := Stage(strings.NewReader("not gzip"), "/etc/dplaneos", 50, Options{}); err == nil {
		t.Error("staged a non-archive")
	}
	// A valid one, with an optional part the archive lacks requested.
	if _, err := Stage(bytes.NewReader(archive(t, map[string]string{"manifest.json": manifest(1), "db.dump": dump,
		"files/etc/dplaneos/x.json": "{}"})), "/etc/dplaneos", 50, Options{Secrets: true}); err != nil {
		t.Fatalf("valid archive: %v", err)
	}
}
