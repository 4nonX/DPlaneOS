package gitops

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// A setting that is not part of the format is refused, with its place.
func TestUnknownSettingsAreRefused(t *testing.T) {
	for name, c := range map[string]struct{ yaml, want string }{
		"top level": {"version: \"6\"\nshare:\n  - name: a\n", "share"},
		"dataset":   {"version: \"6\"\ndatasets:\n  - name: tank/a\n    compress: lz4\n", "datasets[0].compress"},
		"second":    {"version: \"6\"\ndatasets:\n  - name: tank/a\n  - name: tank/b\n    qouta: 1T\n", "datasets[1].qouta"},
		"nested":    {"version: \"6\"\nsystem:\n  ssh:\n    prot: 22\n", "system.ssh.prot"},
		"network":   {"version: \"6\"\nsystem:\n  networking:\n    statics:\n      eth0:\n        cidr: 192.168.1.5/24\n        gatway: 192.168.1.1\n", "system.networking.statics.eth0.gatway"},
		"removed":   {"version: \"6\"\ncertificates:\n  - name: web\n", "certificates"},
	} {
		_, err := ParseStateYAML(c.yaml)
		if err == nil || !strings.Contains(err.Error(), "- "+c.want) {
			t.Errorf("%s: want an error naming %q, got %v", name, c.want, err)
		}
	}
	// Free keys stay free: interface names and pool properties.
	ok := "version: \"6\"\nsystem:\n  networking:\n    statics:\n      enp3s0f1:\n        cidr: 192.168.1.5/24\n"
	if _, err := ParseStateYAML(ok); err != nil {
		t.Errorf("an interface name is not a setting: %v", err)
	}
}

// What the system writes itself (the snapshot it commits, and Capture) is
// accepted by the same check, with every option set.
func TestOwnOutputHasNoUnknownSettings(t *testing.T) {
	yes := true
	quota, allow, deny := "500G", "192.168.1.0/24", "10.0.0.9"
	live := &LiveState{
		Shares: []LiveShare{{Name: "macs", Path: "/tank/macs", Comment: "c", Enabled: true, ReadOnly: true, GuestOK: true,
			ValidUsers: "@family", TimeMachine: true, TimeMachineQuota: quota, ShadowCopy: true, RecycleBin: true,
			HostsAllow: allow, HostsDeny: deny}},
		LDAP: &DesiredLDAP{Enabled: true, Server: "ldap.example", Port: 636, UseTLS: true, BindDN: "cn=x", BaseDN: "dc=x",
			UserFilter: "(uid=%s)", UserIDAttr: "uid", UserNameAttr: "cn", UserEmailAttr: "mail", GroupBaseDN: "ou=g",
			GroupFilter: "(cn=*)", GroupMemberAttr: "member", JITProvisioning: true, DefaultRole: "user", SyncInterval: 60, Timeout: 5},
	}
	if _, err := ParseStateYAML(GenerateStateYAML(live)); err != nil {
		t.Errorf("the snapshot of the live system is refused: %v\n%s", err, GenerateStateYAML(live))
	}
	printed := PrintStateYAML(&DesiredState{Version: "6", Shares: []DesiredShare{{Name: "macs", Path: "/tank/macs",
		TimeMachine: &yes, TimeMachineQuota: &quota, ShadowCopy: &yes, RecycleBin: &yes, HostsAllow: &allow, HostsDeny: &deny}}})
	back, err := ParseStateYAML(printed)
	if err != nil {
		t.Fatalf("the printed file is refused: %v\n%s", err, printed)
	}
	sh := back.Shares[0]
	if sh.TimeMachine == nil || !*sh.TimeMachine || sh.ShadowCopy == nil || !*sh.ShadowCopy || sh.RecycleBin == nil || !*sh.RecycleBin ||
		sh.TimeMachineQuota == nil || *sh.TimeMachineQuota != quota || sh.HostsAllow == nil || *sh.HostsAllow != allow ||
		sh.HostsDeny == nil || *sh.HostsDeny != deny {
		t.Errorf("share options are lost between printing and parsing: %+v\n%s", sh, printed)
	}
}

// A share option in the file is a change to make: it is planned.
func TestShareOptionsFromTheFileArePlanned(t *testing.T) {
	desired, err := ParseStateYAML("version: \"6\"\nshares:\n  - name: data\n    path: /tank/data\n    shadow_copy: true\n    recycle_bin: false\n    hosts_allow: \"10.0.0.0/8\"\n")
	if err != nil {
		t.Fatal(err)
	}
	live := LiveShare{Name: "data", Path: "/tank/data", RecycleBin: true, TimeMachine: true}
	changes := strings.Join(diffShare(desired.Shares[0], live), "; ")
	for _, want := range []string{"shadow_copy", "recycle_bin", "hosts_allow"} {
		if !strings.Contains(changes, want) {
			t.Errorf("%s is in the file and differs, but is not planned: %s", want, changes)
		}
	}
	if strings.Contains(changes, "time_machine") {
		t.Errorf("time_machine is not in the file and must be left alone: %s", changes)
	}
	if _, err := ParseStateYAML("version: \"6\"\nshares:\n  - name: data\n    path: /tank/data\n    shadow_copy: yes please\n"); err == nil {
		t.Error("a switch that is neither true nor false was accepted")
	}
}

// The parser reads exactly the keys the format declares: one it read
// without a tag would make the strict check refuse a valid file, and a tag
// it never reads is a setting that does nothing.
func TestParserReadsWhatTheFormatDeclares(t *testing.T) {
	src, err := os.ReadFile("state.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	body = body[strings.Index(body, "func mapToState("):]
	read := map[string]bool{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?:strField|intField|optBool|optStr)\(\w*,? ?"([a-z_0-9]+)"\)`),
		regexp.MustCompile(`\b\w+\["([a-z_0-9]+)"\]`),
		regexp.MustCompile(`\[\]string\{("[a-z_0-9", ]+)\}`),
	} {
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			for _, k := range strings.Split(m[1], ",") {
				read[strings.Trim(strings.TrimSpace(k), `"`)] = true
			}
		}
	}
	declared := map[string]bool{}
	var collect func(t reflect.Type)
	seen := map[reflect.Type]bool{}
	collect = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
			if name != "" && name != "-" {
				declared[name] = true
			}
			collect(t.Field(i).Type)
		}
	}
	collect(reflect.TypeOf(DesiredState{}))
	for k := range declared {
		if !read[k] {
			t.Errorf("%q is declared in the format but the parser never reads it", k)
		}
	}
	for k := range read {
		if !declared[k] {
			t.Errorf("the parser reads %q, which the format does not declare (the strict check would refuse it)", k)
		}
	}
}
