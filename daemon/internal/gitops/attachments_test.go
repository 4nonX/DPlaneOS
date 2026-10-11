package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dplaned/internal/testdb"
)

// Points the attachment checks at empty directories; returns their roots.
func attachmentDirs(t *testing.T) (lio, nvmet, stacks string) {
	t.Helper()
	root := t.TempDir()
	lio, nvmet, stacks = filepath.Join(root, "lio"), filepath.Join(root, "nvmet"), filepath.Join(root, "stacks")
	for _, d := range []string{lio, nvmet, stacks} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	oldL, oldN, oldS := lioCoreDir, nvmetSubsysDir, attachStacksDir
	lioCoreDir, nvmetSubsysDir, attachStacksDir = lio, nvmet, stacks
	t.Cleanup(func() { lioCoreDir, nvmetSubsysDir, attachStacksDir = oldL, oldN, oldS })
	return
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDatasetAttachments(t *testing.T) {
	db := testdb.Open(t)
	lio, nvmet, stacks := attachmentDirs(t)

	if r := datasetAttachmentReason(db, "tank/data", "/mnt/tank/data"); r != "" {
		t.Fatalf("nothing uses it: %s", r)
	}

	// An SMB share of a folder inside the dataset.
	if _, err := db.Exec(`INSERT INTO smb_shares (name, path) VALUES ('docs', '/mnt/tank/data/docs')`); err != nil {
		t.Fatal(err)
	}
	if r := datasetAttachmentReason(db, "tank/data", "/mnt/tank/data"); !strings.Contains(r, "SMB share") {
		t.Errorf("share below the mountpoint: %q", r)
	}
	if r := datasetAttachmentReason(db, "tank/database", "/mnt/tank/database"); r != "" {
		t.Errorf("a sibling with the same prefix is not affected: %q", r)
	}
	if _, err := db.Exec(`DELETE FROM smb_shares`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO nfs_exports (path, clients, options) VALUES ('/mnt/tank/data', '*', 'ro')`); err != nil {
		t.Fatal(err)
	}
	if r := datasetAttachmentReason(db, "tank/data", "/mnt/tank/data"); !strings.Contains(r, "NFS export") {
		t.Errorf("export of the mountpoint: %q", r)
	}
	if _, err := db.Exec(`DELETE FROM nfs_exports`); err != nil {
		t.Fatal(err)
	}

	// A stack with a bind mount below the dataset.
	writeFile(t, filepath.Join(stacks, "media", "docker-compose.yml"),
		"services:\n  app:\n    image: x\n    volumes:\n      - /mnt/tank/data/media:/media\n")
	writeFile(t, filepath.Join(stacks, "other", "docker-compose.yml"),
		"services:\n  app:\n    image: x\n    volumes:\n      - /mnt/tank/database:/db\n")
	r := datasetAttachmentReason(db, "tank/data", "/mnt/tank/data")
	if !strings.Contains(r, "media") || strings.Contains(r, "other") {
		t.Errorf("stack volumes: %q", r)
	}
	if err := os.RemoveAll(filepath.Join(stacks, "media")); err != nil {
		t.Fatal(err)
	}

	// iSCSI: a block backstore on a volume below the dataset.
	writeFile(t, filepath.Join(lio, "iblock_0", "lun0", "udev_path"), "/dev/zvol/tank/vm/disk0\n")
	if r := datasetAttachmentReason(db, "tank/vm", "-"); !strings.Contains(r, "/dev/zvol/tank/vm/disk0") {
		t.Errorf("iSCSI backstore below the dataset: %q", r)
	}
	if r := datasetAttachmentReason(db, "tank/vm/disk0", "-"); !strings.Contains(r, "iSCSI or NVMe-oF") {
		t.Errorf("iSCSI backstore on the volume itself: %q", r)
	}
	if r := datasetAttachmentReason(db, "tank/vm2", "-"); r != "" {
		t.Errorf("another volume: %q", r)
	}

	// NVMe-oF namespace.
	writeFile(t, filepath.Join(nvmet, "nqn.2024-01.io.dplane.x", "namespaces", "1", "device_path"), "/dev/zvol/tank/fast/vol\n")
	if r := datasetAttachmentReason(db, "tank/fast", "/mnt/tank/fast"); !strings.Contains(r, "/dev/zvol/tank/fast/vol") {
		t.Errorf("NVMe-oF namespace: %q", r)
	}

	// A check that cannot be made blocks.
	db.Close()
	if r := datasetAttachmentReason(db, "tank/data", "/mnt/tank/data"); !strings.Contains(r, "could not be checked") {
		t.Errorf("unreadable database must block: %q", r)
	}
}

func TestComposeUsesPath(t *testing.T) {
	for yaml, want := range map[string]bool{
		"      - /mnt/tank/data:/data\n":                 true,
		"      - /mnt/tank/data/app:/config:ro\n":        true,
		"      - \"/mnt/tank/data\":/data\n":             true,
		"      source: /mnt/tank/data\n":                 true,
		"      - /mnt/tank/database:/db\n":               false,
		"      - /srv/mnt/tank/data:/x\n":                false,
		"      - /mnt/tank/dat:/x\n":                     false,
		"    environment:\n      - DIR=/mnt/tank/data\n": true,
	} {
		if got := composeUsesPath(yaml, "/mnt/tank/data"); got != want {
			t.Errorf("%q: %v", yaml, got)
		}
	}
}
