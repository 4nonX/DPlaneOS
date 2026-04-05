package gitops

import (
	"fmt"
	"sort"
	"strings"
)

func nvmeExportEqual(a, b DesiredNVMeExport) bool {
	if a.SubsystemNQN != b.SubsystemNQN || a.Zvol != b.Zvol || a.Transport != b.Transport ||
		a.ListenAddr != b.ListenAddr || a.ListenPort != b.ListenPort || a.NamespaceID != b.NamespaceID ||
		a.AllowAnyHost != b.AllowAnyHost {
		return false
	}
	if len(a.HostNQNs) != len(b.HostNQNs) {
		return false
	}
	aa := append([]string(nil), a.HostNQNs...)
	bb := append([]string(nil), b.HostNQNs...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func diffNVMeExport(des, liv DesiredNVMeExport) []string {
	var ch []string
	if des.Zvol != liv.Zvol {
		ch = append(ch, fmt.Sprintf("zvol: %q → %q", liv.Zvol, des.Zvol))
	}
	dt := strings.ToLower(strings.TrimSpace(des.Transport))
	if dt == "" {
		dt = "tcp"
	}
	lt := strings.ToLower(strings.TrimSpace(liv.Transport))
	if lt == "" {
		lt = "tcp"
	}
	if dt != lt {
		ch = append(ch, fmt.Sprintf("transport: %s → %s", lt, dt))
	}
	da := des.ListenAddr
	if da == "" {
		da = "0.0.0.0"
	}
	la := liv.ListenAddr
	if la == "" {
		la = "0.0.0.0"
	}
	if da != la {
		ch = append(ch, fmt.Sprintf("listen_addr: %s → %s", la, da))
	}
	dp := des.ListenPort
	if dp <= 0 {
		dp = 4420
	}
	lp := liv.ListenPort
	if lp <= 0 {
		lp = 4420
	}
	if dp != lp {
		ch = append(ch, fmt.Sprintf("listen_port: %d → %d", lp, dp))
	}
	dn := des.NamespaceID
	if dn <= 0 {
		dn = 1
	}
	ln := liv.NamespaceID
	if ln <= 0 {
		ln = 1
	}
	if dn != ln {
		ch = append(ch, fmt.Sprintf("namespace_id: %d → %d", ln, dn))
	}
	if des.AllowAnyHost != liv.AllowAnyHost {
		ch = append(ch, fmt.Sprintf("allow_any_host: %v → %v", liv.AllowAnyHost, des.AllowAnyHost))
	}
	if !hostListEqual(des.HostNQNs, liv.HostNQNs) {
		ch = append(ch, "host_nqns: changed")
	}
	return ch
}

func hostListEqual(a, b []string) bool {
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	for i := range aa {
		aa[i] = strings.TrimSpace(aa[i])
	}
	for i := range bb {
		bb[i] = strings.TrimSpace(bb[i])
	}
	sort.Strings(aa)
	sort.Strings(bb)
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
