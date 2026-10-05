package ha

import (
	"errors"
	"strings"
	"testing"
)

type fakeOps struct {
	importErr  error
	readonly   map[string]string
	setErr     map[string]error
	runErr     map[string]error
	ctdb       bool
	patroniErr error
	calls      []string
}

func (f *fakeOps) ops() promoteOps {
	return promoteOps{
		importAll:    func() error { f.calls = append(f.calls, "import"); return f.importErr },
		listDatasets: func() ([]string, error) { return []string{"tank", "tank/rep"}, nil },
		datasetGet: func(ds, prop string) (string, error) {
			if prop == "readonly" {
				return f.readonly[ds], nil
			}
			return "-", nil
		},
		datasetSet:     func(ds, prop, val string) error { return f.setErr[ds] },
		datasetPromote: func(string) error { return nil },
		run: func(name string, args ...string) error {
			f.calls = append(f.calls, name)
			if name == "systemctl_ha_ctdb_enabled" && !f.ctdb {
				return errors.New("disabled")
			}
			return f.runErr[name]
		},
		patroni: func(c, l string) error { return f.patroniErr },
	}
}

func TestPromotionAllOK(t *testing.T) {
	f := &fakeOps{readonly: map[string]string{"tank/rep": "on"}}
	r := executePromotion(f.ops(), "b", "a")
	if r.Failed || r.Degraded || r.Err() != nil {
		t.Fatalf("unexpected problems: %v", r.Err())
	}
	if len(r.Steps) != 8 {
		t.Errorf("steps: %+v", r.Steps)
	}
}

func TestPromotionImportFailureStops(t *testing.T) {
	f := &fakeOps{importErr: errors.New("no pools available")}
	r := executePromotion(f.ops(), "b", "a")
	if !r.Failed || len(r.Steps) != 1 || len(f.calls) != 1 {
		t.Fatalf("import failure must stop the promotion: %+v calls=%v", r, f.calls)
	}
}

func TestPromotionReportsEveryFailedStep(t *testing.T) {
	f := &fakeOps{
		readonly:   map[string]string{"tank/rep": "on"},
		setErr:     map[string]error{"tank/rep": errors.New("permission denied")},
		runErr:     map[string]error{"systemctl_ha_smbd": errors.New("Unit samba-smbd.service not found")},
		patroniErr: errors.New("HTTP 503"),
		ctdb:       true,
	}
	r := executePromotion(f.ops(), "b", "a")
	if r.Failed || !r.Degraded {
		t.Fatalf("pools imported, so degraded not failed: %+v", r)
	}
	msg := r.Err().Error()
	for _, want := range []string{"tank/rep stays read-only", "samba-smbd.service not found", "HTTP 503"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q missing from %q", want, msg)
		}
	}
}
