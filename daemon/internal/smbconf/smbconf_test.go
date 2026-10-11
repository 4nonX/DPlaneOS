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

func global(conf string) string {
	start := strings.Index(conf, "[global]\n")
	if start < 0 {
		return ""
	}
	rest := conf[start:]
	if end := strings.Index(rest, "\n["); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func TestRenderGlobal(t *testing.T) {
	conf := Render(Options{ExtraGlobal: "server min protocol = SMB3\ninclude = /etc/evil\n[x]"}, []Share{share("data")})
	g := global(conf)
	for _, want := range []string{
		"workgroup = WORKGROUP\n", "server string = DPlaneOS NAS\n", "security = user\n", "map to guest = Never\n",
		"passdb backend = tdbsam\n", "idmap config * : range = 3000-7999\n", "server min protocol = SMB3\n",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("[global] lacks %q:\n%s", want, g)
		}
	}
	if strings.Contains(conf, "/etc/evil") || strings.Contains(conf, "[x]") {
		t.Error("dangerous extra global lines must be dropped")
	}
	if strings.Contains(g, "realm") || strings.Contains(g, "winbind") {
		t.Errorf("directory settings without a directory:\n%s", g)
	}
	if !strings.Contains(stanza(conf, "data"), "path = /tank/data") {
		t.Errorf("share stanza missing:\n%s", conf)
	}
	// Nothing after the shares: the file is the whole configuration.
	if strings.Count(conf, "[global]") != 1 {
		t.Errorf("more than one [global]:\n%s", conf)
	}
}

func TestGlobalSettings(t *testing.T) {
	g := global(Render(Options{Workgroup: "office", ServerString: "Files\n[evil]", SystemdLog: true}, nil))
	if !strings.Contains(g, "workgroup = OFFICE\n") || !strings.Contains(g, "logging = systemd\n") {
		t.Errorf("settings not rendered:\n%s", g)
	}
	if strings.Contains(g, "\n[evil]") {
		t.Errorf("server string injection:\n%s", g)
	}
	// An unusable workgroup falls back instead of breaking the file.
	if g := global(Render(Options{Workgroup: "bad name\n[x]"}, nil)); !strings.Contains(g, "workgroup = WORKGROUP\n") {
		t.Errorf("invalid workgroup rendered:\n%s", g)
	}
}

// A share with guest access only works when unknown users are mapped to
// the guest account.
func TestGuestShareEnablesGuestMapping(t *testing.T) {
	pub := share("public")
	pub.GuestOK = true
	conf := Render(Options{}, []Share{share("private"), pub})
	if !strings.Contains(global(conf), "map to guest = Bad User\n") {
		t.Errorf("guest share without guest mapping:\n%s", global(conf))
	}
	if !strings.Contains(stanza(conf, "public"), "guest ok = yes") || strings.Contains(stanza(conf, "private"), "guest ok") {
		t.Errorf("guest ok on the wrong share:\n%s", conf)
	}
}

func TestDirectoryMember(t *testing.T) {
	g := global(Render(Options{Workgroup: "corp", SecurityMode: "ads", Realm: "corp.example.com"}, nil))
	for _, want := range []string{
		"security = ads\n", "realm = CORP.EXAMPLE.COM\n", "kerberos method = secrets and keytab\n",
		"idmap config * : range = 3000-7999\n", "idmap config CORP : backend = rid\n", "idmap config CORP : range = 10000-999999\n",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("[global] lacks %q:\n%s", want, g)
		}
	}
	g = global(Render(Options{Workgroup: "corp", SecurityMode: "ads", Realm: "corp.example.com", IDMAP: []IDMAPDomain{
		{Name: "*", Backend: "tdb", Low: 3000, High: 7999},
		{Name: "corp", Backend: "rid", Low: 100000, High: 199999},
		{Name: "partner", Backend: "weird", Low: 200000, High: 299999},
		{Name: "overlap", Backend: "rid", Low: 1000, High: 2000}, // would collide with local users
	}}, nil))
	for _, want := range []string{"idmap config CORP : range = 100000-199999\n", "idmap config PARTNER : backend = rid\n"} {
		if !strings.Contains(g, want) {
			t.Errorf("[global] lacks %q:\n%s", want, g)
		}
	}
	if strings.Contains(g, "OVERLAP") || strings.Count(g, "idmap config * : range") != 1 {
		t.Errorf("unsafe or duplicate ID ranges:\n%s", g)
	}
	// A realm that is not one keeps the server out of directory mode.
	if g := global(Render(Options{SecurityMode: "ads", Realm: "x\nsecurity = share"}, nil)); !strings.Contains(g, "security = user\n") {
		t.Errorf("invalid realm:\n%s", g)
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

	conf := Render(Options{}, []Share{tm, docs, plain})

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
	conf := Render(Options{}, []Share{evil})
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
