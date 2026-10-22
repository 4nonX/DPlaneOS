package handlers

import (
	"fmt"
	"regexp"
	"strings"

	"dplaned/internal/cmdutil"
	"dplaned/internal/libzfs"
)

// A new share's folder usually belongs to root (mode 755): users could open
// the share but not write to it. grantShareAccess gives the share's users
// (valid users; without them the group "users", which every file-sharing
// account is in) access with a POSIX ACL on the folder, inherited by what is
// created in it. Existing contents keep their permissions. ZFS datasets need
// acltype=posix for that (OpenZFS on Linux defaults to off).

var principalRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

type aclPrincipal struct {
	group bool
	name  string
}

// sharePrincipals parses an smb.conf user list ("alice, @media +staff").
func sharePrincipals(list string) ([]aclPrincipal, error) {
	var out []aclPrincipal
	for _, tok := range strings.Fields(strings.ReplaceAll(list, ",", " ")) {
		p := aclPrincipal{}
		if t := strings.TrimLeft(tok, "@+&"); t != tok {
			p.group, tok = true, t
		}
		if !principalRe.MatchString(tok) {
			return nil, fmt.Errorf("cannot give %q access: not a user or group name", tok)
		}
		p.name = tok
		out = append(out, p)
	}
	if len(out) == 0 {
		out = []aclPrincipal{{group: true, name: "users"}}
	}
	return out, nil
}

// shareACLSpec is the setfacl -m argument: access and default (inherited)
// entries for each principal, and a mask that lets them through.
func shareACLSpec(ps []aclPrincipal, readOnly bool) string {
	perm := "rwx"
	if readOnly {
		perm = "r-x"
	}
	var e []string
	for _, p := range ps {
		kind := "u"
		if p.group {
			kind = "g"
		}
		e = append(e, kind+":"+p.name+":"+perm, "d:"+kind+":"+p.name+":"+perm)
	}
	e = append(e, "m::rwx", "d:m::rwx")
	return strings.Join(e, ",")
}

// zfsDatasetForPath returns the mounted ZFS filesystem holding path ("" when
// it is not on ZFS).
var zfsDatasetForPath = func(path string) string {
	out, err := cmdutil.RunZFS("zfs", "list", "-H", "-t", "filesystem", "-o", "name,mountpoint,mounted")
	if err != nil {
		return ""
	}
	best, bestLen := "", -1
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || f[2] != "yes" || !strings.HasPrefix(f[1], "/") {
			continue
		}
		if (path == f[1] || strings.HasPrefix(path, strings.TrimSuffix(f[1], "/")+"/")) && len(f[1]) > bestLen {
			best, bestLen = f[0], len(f[1])
		}
	}
	return best
}

// grantShareAccess applies the ACL. It returns a note on what changed.
func grantShareAccess(path, validUsers string, readOnly bool) (string, error) {
	ps, err := sharePrincipals(validUsers)
	if err != nil {
		return "", err
	}
	note := ""
	if ds := zfsDatasetForPath(path); ds != "" {
		v, err := libzfs.DatasetGet(ds, "acltype")
		if err != nil {
			return "", fmt.Errorf("acltype of %s: %w", ds, err)
		}
		if v == "off" || v == "noacl" {
			if err := libzfs.DatasetSet(ds, "acltype", "posix"); err != nil {
				return "", fmt.Errorf("enable POSIX ACLs on %s: %w", ds, err)
			}
			// ACLs live in extended attributes; sa keeps them in the inode.
			if err := libzfs.DatasetSet(ds, "xattr", "sa"); err != nil {
				return "", fmt.Errorf("xattr=sa on %s: %w", ds, err)
			}
			note = "POSIX ACLs enabled on " + ds + "; "
		}
	}
	if out, err := cmdutil.RunFast("setfacl", "-m", shareACLSpec(ps, readOnly), path); err != nil {
		return "", fmt.Errorf("setfacl: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	var names []string
	for _, p := range ps {
		if p.group {
			names = append(names, "group "+p.name)
		} else {
			names = append(names, p.name)
		}
	}
	return note + "access given to " + strings.Join(names, ", "), nil
}

// applyGrant gives the users of share id access to its folder when asked
// and reports the result in the response payload ("access", or a warning:
// the share itself is saved either way).
func (h *ShareCRUDHandler) applyGrant(payload map[string]any, grant bool, id int) {
	if !grant {
		return
	}
	var path, validUsers string
	var readOnly int
	if err := h.db.QueryRow(`SELECT path, valid_users, read_only FROM smb_shares WHERE id = $1`, id).
		Scan(&path, &validUsers, &readOnly); err != nil {
		payload["warning"] = "Folder access not changed: " + err.Error()
		return
	}
	if err := sharePathOK(path); err != nil {
		payload["warning"] = "Folder access not changed: " + err.Error()
		return
	}
	note, err := grantShareAccess(path, validUsers, readOnly == 1)
	if err != nil {
		payload["warning"] = "Folder access not changed: " + err.Error()
		return
	}
	payload["access"] = note
}
