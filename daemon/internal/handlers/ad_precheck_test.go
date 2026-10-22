package handlers

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestADJoinReady(t *testing.T) {
	member := "# Global parameters\n[global]\n\tkerberos method = secrets and keytab\n\trealm = CORP.TEST\n\tsecurity = ADS\n\tworkgroup = CORP\n\n[share]\n\tpath = /x\n"
	wg, err := adJoinReady("corp.test", func() (string, error) { return member, nil })
	if err != nil || wg != "CORP" {
		t.Fatalf("member config: %q %v", wg, err)
	}
	for name, conf := range map[string]string{
		"user mode":     "[global]\n\tsecurity = USER\n\tworkgroup = CORP\n",
		"other realm":   "[global]\n\tsecurity = ADS\n\trealm = OTHER.TEST\n\tworkgroup = CORP\n",
		"default group": "[global]\n\tsecurity = ADS\n\trealm = CORP.TEST\n\tworkgroup = WORKGROUP\n",
	} {
		if _, err := adJoinReady("corp.test", func() (string, error) { return conf, nil }); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !strings.Contains(err.Error(), "apply the pending NixOS changes") {
			t.Errorf("%s: unhelpful error %v", name, err)
		}
	}
	if adWorkgroup("", "corp.example.com") != "CORP" || adWorkgroup("finance", "corp.test") != "FINANCE" || adWorkgroup("WORKGROUP", "corp.test") != "CORP" || len(adWorkgroup("", "averyveryverylongname.test")) != 15 {
		t.Error("adWorkgroup")
	}
}

func TestClockSkew(t *testing.T) {
	old := dcTime
	t.Cleanup(func() { dcTime = old })
	for skew, ok := range map[time.Duration]bool{0: true, 3 * time.Minute: true, -3 * time.Minute: true, 6 * time.Minute: false, -6 * time.Minute: false} {
		dcTime = func(string) (time.Time, error) { return time.Now().Add(skew), nil }
		if err := checkClockSkew("dc"); (err == nil) != ok {
			t.Errorf("skew %v: %v", skew, err)
		}
	}
	dcTime = func(string) (time.Time, error) { return time.Time{}, fmt.Errorf("unreachable") }
	if err := checkClockSkew("dc"); err == nil {
		t.Error("an unreachable DC passed")
	}
}
