package handlers

import (
	"database/sql"
	"strings"

	"dplaned/internal/gitops"
)

// destroyBlockedReason says why dataset must not be destroyed together with
// everything below it: an SMB share, NFS export, container stack, iSCSI or
// NVMe-oF export still uses it or one of its children. Empty: nothing does.
// The same rule guards GitOps plans (gitops.DatasetAttachmentReason).
func destroyBlockedReason(db *sql.DB, dataset string) string {
	out, err := executeCommandWithTimeout(TimeoutFast, "zfs",
		[]string{"list", "-H", "-r", "-t", "filesystem,volume", "-o", "name,mountpoint", dataset})
	if err != nil {
		if strings.Contains(out, "does not exist") {
			return "" // nothing to protect; zfs destroy reports it
		}
		return "Cannot check what still uses " + dataset + " (" + strings.TrimSpace(out) + "); not destroyed."
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 2 || f[0] == "" {
			continue
		}
		if reason := gitops.DatasetAttachmentReason(db, f[0], f[1]); reason != "" {
			return reason
		}
	}
	return ""
}

// destroyTarget returns the dataset a "zfs destroy" would remove, or "" when
// the arguments name a snapshot or bookmark (those have no attachments).
func destroyTarget(args []string) string {
	if len(args) < 2 || args[0] != "destroy" {
		return ""
	}
	target := args[len(args)-1]
	if strings.HasPrefix(target, "-") || strings.ContainsAny(target, "@#") {
		return ""
	}
	return target
}
