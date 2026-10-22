// Package nfsconf formats lines of the DPlaneOS exports file. It is shared by
// the NFS handlers and the GitOps reconciler so both write the same file.
package nfsconf

import (
	"fmt"
	"log"
	"regexp"
	"strings"
)

// DefaultOptions for a new export. crossmnt: ZFS child datasets inside the
// export are visible to NFSv4 clients (without it they show up empty).
// root_squash: a client's root is not root on the export.
const DefaultOptions = "rw,sync,no_subtree_check,root_squash,crossmnt"

// EscapePath writes a path for an exports file, which splits fields at
// whitespace: space, tab, newline and backslash become octal escapes (\040),
// as exportfs reads them. Unescaped, "/tank/My Files" exported "/tank/My" to
// a host called "Files" and failed the reload of every export.
func EscapePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch r {
		case ' ', '\t', '\n', '\\':
			fmt.Fprintf(&b, `\%03o`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Option keywords of exports(5). exportfs skips an export with an unknown
// keyword (and reports an error on every reload after that).
var (
	flagOptions = map[string]bool{
		"rw": true, "ro": true, "sync": true, "async": true, "wdelay": true, "no_wdelay": true,
		"hide": true, "nohide": true, "crossmnt": true, "nocrossmnt": true,
		"subtree_check": true, "no_subtree_check": true, "secure": true, "insecure": true,
		"secure_locks": true, "insecure_locks": true, "auth_nlm": true, "no_auth_nlm": true,
		"root_squash": true, "no_root_squash": true, "all_squash": true, "no_all_squash": true,
		"nordirplus": true, "pnfs": true, "no_pnfs": true, "security_label": true,
		"acl": true, "no_acl": true, "mountpoint": true, "mp": true,
	}
	valueOptions = map[string]*regexp.Regexp{
		"anonuid":    regexp.MustCompile(`^-?[0-9]{1,10}$`),
		"anongid":    regexp.MustCompile(`^-?[0-9]{1,10}$`),
		"fsid":       regexp.MustCompile(`^(root|[0-9]{1,10}|[0-9a-fA-F-]{32,36})$`),
		"sec":        regexp.MustCompile(`^(sys|krb5|krb5i|krb5p)(:(sys|krb5|krb5i|krb5p))*$`),
		"mountpoint": regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`),
		"mp":         regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`),
		"refer":      regexp.MustCompile(`^/[A-Za-z0-9._/-]*@[A-Za-z0-9.:+-]+$`),
		"replicas":   regexp.MustCompile(`^/[A-Za-z0-9._/-]*@[A-Za-z0-9.:+-]+$`),
		"xprtsec":    regexp.MustCompile(`^(none|tls|mtls)(:(none|tls|mtls))*$`),
	}
	// A client: *, a host or wildcard name, an IP, a network (CIDR or
	// address/netmask), @netgroup.
	clientRe = regexp.MustCompile(`^(\*|@?[A-Za-z0-9*?._-]+|[0-9A-Fa-f.:]+(/[0-9A-Fa-f.:]+)?)$`)
)

// ValidateOptions checks an export's option list.
func ValidateOptions(opts string) error {
	if strings.TrimSpace(opts) == "" {
		return fmt.Errorf("no options")
	}
	for _, o := range strings.Split(opts, ",") {
		o = strings.TrimSpace(o)
		k, v, hasValue := strings.Cut(o, "=")
		switch {
		case o == "":
			return fmt.Errorf("empty option in %q", opts)
		case !hasValue && flagOptions[k]:
		case hasValue && valueOptions[k] != nil && valueOptions[k].MatchString(v):
		case hasValue && valueOptions[k] != nil:
			return fmt.Errorf("invalid value for %s: %q", k, v)
		default:
			return fmt.Errorf("unknown export option %q (see exports(5))", o)
		}
	}
	return nil
}

// ValidateClients checks a space-separated client list.
func ValidateClients(clients string) error {
	fields := strings.Fields(clients)
	if len(fields) == 0 {
		return fmt.Errorf("no clients")
	}
	for _, c := range fields {
		if len(c) > 253 || !clientRe.MatchString(c) {
			return fmt.Errorf("invalid client %q (use *, a host name, an address or a network like 192.168.1.0/24)", c)
		}
	}
	return nil
}

// Lines returns the export lines of one path: one line per client, since
// "path c1 c2(opts)" would give c1 the default options instead of these.
// Invalid clients or options give no lines (they would break the file).
func Lines(path, clients, options string) string {
	if err := ValidateClients(clients); err != nil {
		log.Printf("NFS: export %s skipped: %v", path, err)
		return ""
	}
	if err := ValidateOptions(options); err != nil {
		log.Printf("NFS: export %s skipped: %v", path, err)
		return ""
	}
	var b strings.Builder
	for client := range strings.FieldsSeq(clients) {
		fmt.Fprintf(&b, "%s\t%s(%s)\n", EscapePath(path), client, options)
	}
	return b.String()
}
