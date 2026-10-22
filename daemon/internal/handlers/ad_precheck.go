package handlers

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"time"

	"dplaned/internal/cmdutil"

	goldap "github.com/go-ldap/ldap/v3"
)

// sambaGlobals parses the [global] section of `testparm -s` output.
func sambaGlobals(testparm string) map[string]string {
	g := map[string]string{}
	inGlobal := false
	sc := bufio.NewScanner(strings.NewReader(testparm))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inGlobal = strings.EqualFold(line, "[global]")
			continue
		}
		if !inGlobal {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			g[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return g
}

// adJoinReady checks that Samba runs as an AD member of realm (net ads join
// refuses otherwise with "Invalid configuration ... modification was not
// requested"). On NixOS these settings come from the AD configuration and
// take effect when the pending NixOS changes are applied. It returns the
// workgroup (NetBIOS domain name) to join with.
func adJoinReady(realm string, testparm func() (string, error)) (string, error) {
	out, err := testparm()
	if err != nil {
		return "", fmt.Errorf("cannot read the Samba configuration: %v", err)
	}
	g := sambaGlobals(out)
	if !strings.EqualFold(g["security"], "ads") || !strings.EqualFold(g["realm"], realm) {
		return "", fmt.Errorf("Samba is not yet configured as a member of %s (security = %q, realm = %q): save the Active Directory configuration and apply the pending NixOS changes, then join", strings.ToUpper(realm), g["security"], g["realm"])
	}
	wg := g["workgroup"]
	if wg == "" || strings.EqualFold(wg, "WORKGROUP") {
		return "", fmt.Errorf("the Samba workgroup is %q, not the domain's NetBIOS name: set it in the Active Directory configuration and apply the pending NixOS changes", wg)
	}
	return wg, nil
}

func runTestparm() (string, error) {
	out, err := cmdutil.Run(30*time.Second, "testparm", "-s")
	return string(out), err
}

// adWorkgroup is the NetBIOS domain name for realm: the configured one, or
// the realm's first label (the AD default), at most 15 characters.
func adWorkgroup(configured, realm string) string {
	if w := strings.TrimSpace(configured); w != "" && !strings.EqualFold(w, "WORKGROUP") { // the Samba default is not a domain name
		return strings.ToUpper(w)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(realm), ".")
	first = strings.ToUpper(first)
	if len(first) > 15 {
		first = first[:15]
	}
	return first
}

// dcTime reads the domain controller's clock (rootDSE currentTime, readable
// without credentials on AD); replaced in tests.
var dcTime = func(dc string) (time.Time, error) {
	conn, err := goldap.DialURL("ldap://"+net.JoinHostPort(dc, "389"), goldap.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}))
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Close()
	conn.SetTimeout(10 * time.Second)
	res, err := conn.Search(goldap.NewSearchRequest("", goldap.ScopeBaseObject, goldap.NeverDerefAliases, 0, 10, false,
		"(objectClass=*)", []string{"currentTime"}, nil))
	if err != nil || len(res.Entries) == 0 {
		return time.Time{}, fmt.Errorf("rootDSE: %v", err)
	}
	v := res.Entries[0].GetAttributeValue("currentTime")
	for _, layout := range []string{"20060102150405.0Z", "20060102150405Z"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("rootDSE currentTime %q", v)
}

// maxKerberosSkew: Kerberos refuses tickets beyond 5 minutes; keep a margin.
const maxKerberosSkew = 4 * time.Minute

// checkClockSkew measures this machine's clock against the DC. Kerberos
// needs the clocks within 5 minutes of each other; whether NTP runs (the
// former check) says neither (an air-gapped NAS with a right clock was
// refused, one synchronised to a wrong source passed).
func checkClockSkew(dc string) error {
	t, err := dcTime(dc)
	if err != nil {
		return fmt.Errorf("cannot read the domain controller's time from %s: %v", dc, err)
	}
	skew := time.Since(t)
	if skew < 0 {
		skew = -skew
	}
	if skew > maxKerberosSkew {
		return fmt.Errorf("this machine's clock is %s away from the domain controller's (Kerberos allows 5 minutes)", skew.Round(time.Second))
	}
	return nil
}
