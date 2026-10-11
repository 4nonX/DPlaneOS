package gitops

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// What still uses a dataset: checked before a plan may destroy it.

// Kernel target configuration (configfs): where iSCSI (LIO) and NVMe-oF
// exports name their backing devices. Variables so tests can point elsewhere.
var (
	lioCoreDir      = "/sys/kernel/config/target/core"
	nvmetSubsysDir  = "/sys/kernel/config/nvmet/subsystems"
	attachStacksDir = defaultStacksDir
)

// DatasetAttachmentReason is datasetAttachmentReason for the handlers that
// destroy datasets outside a plan.
func DatasetAttachmentReason(db *sql.DB, datasetName, mountpoint string) string {
	return datasetAttachmentReason(db, datasetName, mountpoint)
}

// datasetAttachmentReason returns why the dataset (or anything below it)
// must not be destroyed: SMB shares, NFS exports and stacks that use its
// mountpoint, iSCSI and NVMe-oF exports that use it or a volume below it.
// Empty: nothing uses it. A check that cannot be made counts as "in use":
// the plan is blocked rather than destroying a dataset on a guess.
func datasetAttachmentReason(db *sql.DB, datasetName, mountpoint string) string {
	var attachments []string
	unverified := func(what string, err error) string {
		return fmt.Sprintf("Dataset %q cannot be deleted: whether %s still use it could not be checked (%v).", datasetName, what, err)
	}

	hasMount := mountpoint != "" && mountpoint != "none" && mountpoint != "legacy" && mountpoint != "-"
	if hasMount {
		below := strings.TrimSuffix(mountpoint, "/") + "/%"
		var shareCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM smb_shares WHERE path = $1 OR path LIKE $2`, mountpoint, below).Scan(&shareCount); err != nil {
			return unverified("SMB shares", err)
		}
		if shareCount > 0 {
			attachments = append(attachments, fmt.Sprintf("%d SMB share(s) under %s", shareCount, mountpoint))
		}

		var nfsCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM nfs_exports WHERE path = $1 OR path LIKE $2`, mountpoint, below).Scan(&nfsCount); err != nil {
			return unverified("NFS exports", err)
		}
		if nfsCount > 0 {
			attachments = append(attachments, fmt.Sprintf("%d NFS export(s) under %s", nfsCount, mountpoint))
		}

		stacks, err := stacksUsingPath(mountpoint)
		if err != nil {
			return unverified("container stacks", err)
		}
		if len(stacks) > 0 {
			attachments = append(attachments, fmt.Sprintf("stack(s) %s with volumes under %s", strings.Join(stacks, ", "), mountpoint))
		}
	}

	devs, err := exportedDevices()
	if err != nil {
		return unverified("iSCSI or NVMe-oF exports", err)
	}
	var exported []string
	for _, d := range devs {
		if deviceOfDataset(d, datasetName) {
			exported = append(exported, d)
		}
	}
	if len(exported) > 0 {
		attachments = append(attachments, fmt.Sprintf("iSCSI or NVMe-oF export(s) of %s", strings.Join(exported, ", ")))
	}

	if len(attachments) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"Dataset %q cannot be deleted: it is still referenced by active services: %s. "+
			"Remove these attachments first, then re-apply the plan.",
		datasetName, strings.Join(attachments, "; "),
	)
}

// stacksUsingPath lists the stacks whose compose file names path or
// something below it (bind mounts are written as host paths).
func stacksUsingPath(path string) ([]string, error) {
	entries, err := os.ReadDir(attachStacksDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	path = strings.TrimSuffix(path, "/")
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(attachStacksDir, e.Name(), "docker-compose.yml"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if composeUsesPath(string(data), path) {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// composeUsesPath: path appears as a whole path prefix (/mnt/tank/data in
// "/mnt/tank/data/app:/config", not in "/mnt/tank/database").
func composeUsesPath(yaml, path string) bool {
	for rest := yaml; ; {
		i := strings.Index(rest, path)
		if i < 0 {
			return false
		}
		before := byte(' ')
		if i > 0 {
			before = rest[i-1]
		}
		after := byte(' ')
		if j := i + len(path); j < len(rest) {
			after = rest[j]
		}
		startOK := strings.IndexByte(" \t\n\"'-:=[,", before) >= 0
		endOK := strings.IndexByte(" \t\r\n\"':/,]", after) >= 0
		if startOK && endOK {
			return true
		}
		rest = rest[i+len(path):]
	}
}

// exportedDevices returns the backing devices of all iSCSI (LIO block
// backstores) and NVMe-oF exports configured in the kernel.
func exportedDevices() ([]string, error) {
	var devs []string
	for _, pattern := range []string{
		filepath.Join(lioCoreDir, "iblock_*", "*", "udev_path"),
		filepath.Join(lioCoreDir, "fileio_*", "*", "udev_path"),
		filepath.Join(nvmetSubsysDir, "*", "namespaces", "*", "device_path"),
	} {
		files, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			if d := strings.TrimSpace(string(data)); d != "" {
				devs = append(devs, d)
			}
		}
	}
	return devs, nil
}

// deviceOfDataset: dev is the volume dataset itself or one below it
// (/dev/zvol/tank/vm or /dev/zvol/tank/vm/disk0 for tank/vm), by its zvol
// path or through a symlink to it.
func deviceOfDataset(dev, dataset string) bool {
	match := func(p string) bool {
		rest, ok := strings.CutPrefix(filepath.ToSlash(p), "/dev/zvol/")
		return ok && (rest == dataset || strings.HasPrefix(rest, dataset+"/"))
	}
	if match(dev) {
		return true
	}
	// A backstore given as /dev/zdN: find the zvol link that points to it.
	if !strings.HasPrefix(dev, "/dev/zd") {
		return false
	}
	found := false
	root := "/dev/zvol/" + dataset
	_ = filepath.WalkDir(filepath.Dir(root), func(p string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 && match(p) {
			if target, err := filepath.EvalSymlinks(p); err == nil && target == dev {
				found = true
			}
		}
		return nil
	})
	return found
}
