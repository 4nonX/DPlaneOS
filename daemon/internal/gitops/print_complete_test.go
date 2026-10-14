package gitops

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Every field of every section must survive PrintStateYAML → ParseStateYAML:
// the export (Change History) and the Git backend write state.yaml with the
// printer, so a field it leaves out would silently be lost or reset. The
// state is filled by reflection, so a new field is covered without editing
// this test.
func TestPrintParseKeepsEveryField(t *testing.T) {
	in := &DesiredState{Version: "1"}
	fill(reflect.ValueOf(in).Elem(), "s")
	in.Version, in.IgnoreExtraneous = "1", false
	// Fields with a constrained shape get valid values.
	constrain(in)
	out := PrintStateYAML(in)
	back, err := ParseStateYAML(out)
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
	compare(t, "state", reflect.ValueOf(*in), reflect.ValueOf(*back), out)
}

func compare(t *testing.T, path string, want, got reflect.Value, doc string) {
	t.Helper()
	switch want.Kind() {
	case reflect.Struct:
		for i := 0; i < want.NumField(); i++ {
			f := want.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			compare(t, path+"."+f.Name, want.Field(i), got.Field(i), doc)
		}
	case reflect.Pointer:
		if want.IsNil() != got.IsNil() {
			t.Errorf("%s: nil=%v after the round trip, want nil=%v", path, got.IsNil(), want.IsNil())
			return
		}
		if !want.IsNil() {
			compare(t, path, want.Elem(), got.Elem(), doc)
		}
	case reflect.Slice:
		if want.Len() != got.Len() {
			t.Errorf("%s: %d entries after the round trip, want %d", path, got.Len(), want.Len())
			return
		}
		for i := 0; i < want.Len(); i++ {
			compare(t, fmt.Sprintf("%s[%d]", path, i), want.Index(i), got.Index(i), doc)
		}
	default:
		if !reflect.DeepEqual(want.Interface(), got.Interface()) {
			t.Errorf("%s: %#v after the round trip, want %#v", path, got.Interface(), want.Interface())
		}
	}
}

// fill sets every field to a non-zero value derived from its path.
func fill(v reflect.Value, path string) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), path+"_"+strings.ToLower(v.Type().Field(i).Name))
			}
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), path)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(s.Index(0), path)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(k, path+"_k")
		fill(e, path+"_v")
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.String:
		v.SetString(strings.ReplaceAll(path, ".", "_"))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(int64(len(path)))
	}
}

// constrain gives validated fields valid values (the rest keeps its filler).
func constrain(s *DesiredState) {
	p := &s.Pools[0]
	p.Name, p.Ashift, p.GUID = "tank", 12, "1234567890"
	p.Topology = PoolTopology{
		Data:  []VdevGroup{{Type: "mirror", Disks: []string{"/dev/disk/by-id/ata-a", "/dev/disk/by-id/ata-b"}}},
		Log:   []VdevGroup{{Type: "mirror", Disks: []string{"/dev/disk/by-id/nvme-c", "/dev/disk/by-id/nvme-d"}}},
		Cache: []VdevGroup{{Type: "stripe", Disks: []string{"/dev/disk/by-id/nvme-e"}}},
		Spare: []VdevGroup{{Type: "stripe", Disks: []string{"/dev/disk/by-id/ata-f"}}},
	}
	p.Options = map[string]string{"autotrim": "on"}
	d := &s.Datasets[0]
	d.Name, d.Compression, d.Atime, d.Sync, d.Recordsize = "tank/data", "lz4", "off", "standard", "128K"
	d.Xattr, d.Secondarycache, d.Mountpoint = "sa", "all", "/mnt/tank/data"
	sh := &s.Shares[0]
	sh.Name, sh.Path = "media", "/mnt/tank/media"
	s.NFS[0].Path = "/mnt/tank/data"
	s.Stacks[0].Name, s.Stacks[0].YAML = "app", "services:\n  web:\n    image: nginx"
	// Transport, address, port and namespace at their defaults: the printer omits defaults.
	s.Fabrics.NVMe[0] = DesiredNVMeExport{SubsystemNQN: "nqn.2014-08.org.dplaneos:vol1", Zvol: "tank/vol1",
		HostNQNs: []string{"nqn.2014-08.org.example:host1"}}
	s.System.Hostname, s.System.SSH.PermitRootLogin, s.System.SSH.Port = "nas1", "no", 22
	s.Users[0].Username, s.Users[0].PasswordHash = "alice", "secret://user/alice/password"
	s.LDAP.BindPassword = "secret://ldap/ldap/bind_password"
	s.ACME.Enabled, s.ACME.DNSConfig = true, map[string]string{"CF_API_TOKEN": "secret://acme/acme/dns_config.CF_API_TOKEN"}
	s.Certificates[0] = DesiredCertificate{Name: "web",
		Cert: "-----BEGIN CERTIFICATE-----\nMIIBszCCAVmgAwIBAgIU\n-----END CERTIFICATE-----",
		Key:  "secret://certificate/web/key"}
	s.SMART[0] = DesiredSMARTTask{Device: "/dev/disk/by-id/ata-a", Type: "short", Schedule: "*-*-* 02:00:00", Enabled: true}
	s.Replication[0].Interval = "daily"
}
