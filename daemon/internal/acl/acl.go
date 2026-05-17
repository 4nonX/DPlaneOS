package acl

import (
	"fmt"
	"regexp"
	"strings"
)

// NFSv4ACEType represents an NFSv4 ACE type character (RFC 5661 section 6.2.1.1).
type NFSv4ACEType string

const (
	ACETypeAllow NFSv4ACEType = "A" // ALLOW
	ACETypeDeny  NFSv4ACEType = "D" // DENY
	ACETypeAudit NFSv4ACEType = "U" // AUDIT
	ACETypeAlarm NFSv4ACEType = "L" // ALARM
)

// NFSv4ACE represents a single NFSv4 Access Control Entry.
// Format used by nfs4_getfacl / nfs4_setfacl: type:flags:principal:perms
type NFSv4ACE struct {
	Type      NFSv4ACEType `json:"type"`
	Flags     string       `json:"flags"`
	Principal string       `json:"principal"`
	Perms     string       `json:"perms"`
}

// ACLResult is the JSON envelope returned by GetNFS4ACL.
type ACLResult struct {
	Path      string     `json:"path"`
	NFSv4ACEs []NFSv4ACE `json:"nfsv4_aces,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// aceLineRe matches a single NFSv4 ACE line from nfs4_getfacl output.
// Captures: type, flags, principal, perms
var aceLineRe = regexp.MustCompile(`^([ADUL]):([gdfpniGSF]*):([^:]+):([rwaxdDtTnNcCoy]*)$`)

// validFlagsRe and validPermsRe guard ValidateACE.
var validFlagsRe = regexp.MustCompile(`^[gdfpniGSF]*$`)
var validPermsRe = regexp.MustCompile(`^[rwaxdDtTnNcCoy]*$`)
var safePrincipalRe = regexp.MustCompile(`^[a-zA-Z0-9@._\-]+$`)

// ParseNFSv4ACL parses the output of "nfs4_getfacl <path>".
// Lines beginning with '#' are metadata comments and are skipped.
func ParseNFSv4ACL(output string) []NFSv4ACE {
	var aces []NFSv4ACE
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := aceLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		aces = append(aces, NFSv4ACE{
			Type:      NFSv4ACEType(m[1]),
			Flags:     m[2],
			Principal: m[3],
			Perms:     m[4],
		})
	}
	return aces
}

// FormatACESpec converts a slice of ACEs into the comma-separated spec
// accepted by "nfs4_setfacl -s <spec> <path>".
func FormatACESpec(aces []NFSv4ACE) string {
	parts := make([]string, len(aces))
	for i, ace := range aces {
		parts[i] = fmt.Sprintf("%s:%s:%s:%s", ace.Type, ace.Flags, ace.Principal, ace.Perms)
	}
	return strings.Join(parts, ",")
}

// ValidateACE returns a non-nil error if any field of the ACE is malformed.
func ValidateACE(ace NFSv4ACE) error {
	switch ace.Type {
	case ACETypeAllow, ACETypeDeny, ACETypeAudit, ACETypeAlarm:
	default:
		return fmt.Errorf("invalid ACE type %q (must be A, D, U, or L)", ace.Type)
	}
	if !validFlagsRe.MatchString(ace.Flags) {
		return fmt.Errorf("invalid ACE flags %q", ace.Flags)
	}
	if ace.Principal == "" {
		return fmt.Errorf("ACE principal cannot be empty")
	}
	switch ace.Principal {
	case "OWNER@", "GROUP@", "EVERYONE@":
	default:
		if !safePrincipalRe.MatchString(ace.Principal) {
			return fmt.Errorf("invalid ACE principal %q", ace.Principal)
		}
	}
	if !validPermsRe.MatchString(ace.Perms) {
		return fmt.Errorf("invalid ACE perms %q (allowed chars: rwaxdDtTnNcCoy)", ace.Perms)
	}
	return nil
}

// POSIXModeToNFSv4 converts the lower 9 bits of a POSIX mode integer to a
// minimal set of NFSv4 Allow ACEs covering OWNER@, GROUP@, and EVERYONE@.
// Mapping follows RFC 5661 section 6.4 with Windows-compat synchronize bit.
func POSIXModeToNFSv4(mode uint32) []NFSv4ACE {
	ownerR := mode&0400 != 0
	ownerW := mode&0200 != 0
	ownerX := mode&0100 != 0
	groupR := mode&0040 != 0
	groupW := mode&0020 != 0
	groupX := mode&0010 != 0
	otherR := mode&0004 != 0
	otherW := mode&0002 != 0
	otherX := mode&0001 != 0

	var aces []NFSv4ACE
	if p := buildPerms(ownerR, ownerW, ownerX, true); p != "" {
		aces = append(aces, NFSv4ACE{Type: ACETypeAllow, Flags: "", Principal: "OWNER@", Perms: p})
	}
	if p := buildPerms(groupR, groupW, groupX, false); p != "" {
		aces = append(aces, NFSv4ACE{Type: ACETypeAllow, Flags: "g", Principal: "GROUP@", Perms: p})
	}
	if p := buildPerms(otherR, otherW, otherX, false); p != "" {
		aces = append(aces, NFSv4ACE{Type: ACETypeAllow, Flags: "", Principal: "EVERYONE@", Perms: p})
	}
	return aces
}

// buildPerms maps three POSIX permission bits to an NFSv4 permission string.
// Permission ordering follows RFC 5661 section 6.2.1.1: rwaxdDtTnNcCoy.
// isOwner adds delete_child (D), write_acl (C), and write_owner (o) when write is set.
// Returns "" when all three bits are false, signaling no ACE should be emitted.
func buildPerms(r, w, x, isOwner bool) string {
	if !r && !w && !x {
		return ""
	}
	var p strings.Builder
	if r {
		p.WriteByte('r') // read_data / list_directory
		p.WriteByte('t') // read_attrs
		p.WriteByte('n') // read_named_attrs (xattrs)
		p.WriteByte('c') // read_acl
	}
	if w {
		p.WriteByte('w') // write_data / create_file
		p.WriteByte('a') // append_data / create_subdir
		p.WriteByte('T') // write_attrs
		p.WriteByte('N') // write_named_attrs
	}
	if x {
		p.WriteByte('x') // execute / traverse
	}
	// d (delete self) is intentionally omitted: POSIX mode bits encode permission
	// to delete a file via the parent directory's write bit, not the file's own mode.
	if isOwner && w {
		p.WriteByte('D') // delete_child: owner can remove entries in this dir
		p.WriteByte('C') // write_acl
		p.WriteByte('o') // write_owner
	}
	p.WriteByte('y') // synchronize: always set for Windows client compat
	return p.String()
}
