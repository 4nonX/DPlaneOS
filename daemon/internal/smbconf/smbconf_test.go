package smbconf

import (
	"strings"
	"testing"
)

func share(name string) Share {
	return Share{Name: name, Path: "/tank/" + name, Browsable: true, CreateMask: "0664", DirectoryMask: "0775"}
}

// stanza returns the text of one share section.
func stanza(conf, name string) string {
	start := strings.Index(conf, "["+name+"]\n")
	if start < 0 {
		return ""
	}
	rest := conf[start+len(name)+3:]
	if end := strings.Index(rest, "\n["); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func TestRenderNixOSSharesOnly(t *testing.T) {
	conf := Render(Options{NixOSManaged: true}, []Share{share("data")})
	if strings.HasPrefix(conf, "[global]") || strings.Contains(conf, "security =") || strings.Contains(conf, "workgroup =") {
		t.Errorf("NixOS-managed output must not set global keys:\n%s", conf)
	}
	if !strings.HasSuffix(conf, "[global]\n") {
		t.Errorf("NixOS-managed output must end by re-opening [global]:\n%s", conf)
	}
	if !strings.Contains(stanza(conf, "data"), "path = /tank/data") {
		t.Errorf("share stanza missing:\n%s", conf)
	}
}

func TestRenderStandaloneHasGlobal(t *testing.T) {
	conf := Render(Options{ExtraGlobal: "server min protocol = SMB3\ninclude = /etc/evil\n[x]"}, []Share{share("data")})
	if !strings.HasPrefix(conf, "[global]\n") {
		t.Fatalf("standalone output must start with [global]:\n%s", conf)
	}
	if !strings.Contains(conf, "server min protocol = SMB3") {
		t.Error("extra global parameter missing")
	}
	if strings.Contains(conf, "/etc/evil") || strings.Contains(conf, "[x]") {
		t.Error("dangerous extra global lines must be dropped")
	}
}

func TestPerShareOptions(t *testing.T) {
	tm := share("macs")
	tm.TimeMachine = true
	tm.TimeMachineQuota = "500G"
	docs := share("docs")
	docs.ShadowCopy = true
	docs.RecycleBin = true
	docs.HostsAllow = "192.168.1.0/24, 10.0.0.5"
	docs.HostsDeny = "ALL"
	plain := share("plain")

	conf := Render(Options{NixOSManaged: true}, []Share{tm, docs, plain})

	m := stanza(conf, "macs")
	for _, want := range []string{"vfs objects = catia fruit streams_xattr\n", "fruit:time machine = yes", "fruit:time machine max size = 500G"} {
		if !strings.Contains(m, want) {
			t.Errorf("macs missing %q:\n%s", want, m)
		}
	}

	d := stanza(conf, "docs")
	for _, want := range []string{
		"vfs objects = catia fruit streams_xattr shadow_copy2 recycle",
		"shadow:snapdir = .zfs/snapshot", "recycle:repository = .recycle/%U",
		"hosts allow = 192.168.1.0/24 10.0.0.5", "hosts deny = ALL",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("docs missing %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "fruit:time machine") {
		t.Error("docs is not a Time Machine target")
	}

	// fruit must be on every share once any share needs it (AAPL is
	// negotiated on the first tree connect), but only TM shares are targets.
	p := stanza(conf, "plain")
	if !strings.Contains(p, "vfs objects = catia fruit streams_xattr") || strings.Contains(p, "time machine") {
		t.Errorf("plain share wrong:\n%s", p)
	}
}

func TestNoAppleWithoutTimeMachine(t *testing.T) {
	conf := Render(Options{}, []Share{share("a")})
	if strings.Contains(conf, "fruit") || strings.Contains(conf, "vfs objects") {
		t.Errorf("no share needs fruit, none should load it:\n%s", conf)
	}
	conf = Render(Options{AppleExtensions: true}, []Share{share("a")})
	if !strings.Contains(stanza(conf, "a"), "catia fruit streams_xattr") || strings.Contains(conf, "fruit:time machine") {
		t.Errorf("global macOS toggle loads fruit without making shares TM targets:\n%s", conf)
	}
}

func TestSanitizationAndValidation(t *testing.T) {
	evil := share("x")
	evil.Comment = "hi\n[global]\nsecurity = share"
	evil.HostsAllow = "1.2.3.4; rm -rf /"
	evil.TimeMachineQuota = "lots"
	evil.TimeMachine = true
	evil.CreateMask = "0777\n[y]"
	conf := Render(Options{NixOSManaged: true}, []Share{evil})
	s := stanza(conf, "x")
	if strings.Contains(s, "\n[") || strings.Contains(s, "security = share") {
		t.Errorf("comment injection not neutralised:\n%s", conf)
	}
	if strings.Contains(s, "hosts allow") || strings.Contains(s, "max size") || !strings.Contains(s, "create mask = 0664") {
		t.Errorf("invalid values must be dropped or defaulted:\n%s", s)
	}

	for _, ok := range []string{"", "192.168.1.0/24", "10.0.0.5, nas.lan", "192.168. EXCEPT 192.168.1.5", "fe80::/10"} {
		if err := ValidateHostList(ok); err != nil {
			t.Errorf("ValidateHostList(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"a;b", "x y=z", "$(id)"} {
		if ValidateHostList(bad) == nil {
			t.Errorf("ValidateHostList(%q) should fail", bad)
		}
	}
	for _, ok := range []string{"", "500G", "2T", "750M"} {
		if err := ValidateTimeMachineQuota(ok); err != nil {
			t.Errorf("ValidateTimeMachineQuota(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"0G", "500", "1.5T", "500GB"} {
		if ValidateTimeMachineQuota(bad) == nil {
			t.Errorf("ValidateTimeMachineQuota(%q) should fail", bad)
		}
	}
}

func TestAvahiTimeMachineXML(t *testing.T) {
	x := AvahiTimeMachineXML([]string{"macs", "a&b"})
	for _, want := range []string{"dk0=adVN=macs,adVF=0x82", "dk1=adVN=a&amp;b,adVF=0x82", "_adisk._tcp"} {
		if !strings.Contains(x, want) {
			t.Errorf("avahi XML missing %q:\n%s", want, x)
		}
	}
}
