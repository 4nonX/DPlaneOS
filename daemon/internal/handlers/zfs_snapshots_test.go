package handlers

import (
	"reflect"
	"testing"
)

func TestRollbackArgs(t *testing.T) {
	const snap = "tank/data@auto-2026-10-01"
	cases := []struct {
		mode    string
		force   bool
		want    []string
		wantErr bool
	}{
		{mode: "safe", want: []string{"rollback", snap}},
		{mode: "safe", force: true, want: []string{"rollback", snap}}, // mode wins over legacy force
		{mode: "destroy_newer", want: []string{"rollback", "-r", snap}},
		{mode: "destroy_clones", want: []string{"rollback", "-R", snap}},
		{mode: "", force: false, want: []string{"rollback", snap}},
		{mode: "", force: true, want: []string{"rollback", "-r", snap}},
		{mode: "-R", wantErr: true},
		{mode: "yolo", wantErr: true},
	}
	for _, c := range cases {
		got, err := rollbackArgs(c.mode, c.force, snap)
		if c.wantErr {
			if err == nil {
				t.Errorf("rollbackArgs(%q, %v): expected error, got %v", c.mode, c.force, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("rollbackArgs(%q, %v): unexpected error %v", c.mode, c.force, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("rollbackArgs(%q, %v) = %v, want %v", c.mode, c.force, got, c.want)
		}
	}
}
