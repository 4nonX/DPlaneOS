package handlers

import (
	"reflect"
	"testing"
)

func TestParseZfsList_fullRow(t *testing.T) {
	out := "tank\t1.2G\t500G\tnone\t/tank\tlz4\t2.10x\t2.05x\ntank/ds\t100M\t500G\tnone\t/tank/ds\tzstd-3\t1.50x\t1.48x\n"
	got := parseZfsList(out)
	want := []map[string]string{
		{
			"name": "tank", "used": "1.2G", "avail": "500G", "quota": "none", "mountpoint": "/tank",
			"compression": "lz4", "compressratio": "2.10x", "refcompressratio": "2.05x",
		},
		{
			"name": "tank/ds", "used": "100M", "avail": "500G", "quota": "none", "mountpoint": "/tank/ds",
			"compression": "zstd-3", "compressratio": "1.50x", "refcompressratio": "1.48x",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseZfsList() = %#v, want %#v", got, want)
	}
}

func TestParseZfsList_legacyFiveColumns(t *testing.T) {
	out := "tank\t1.2G\t500G\tnone\t/tank\n"
	got := parseZfsList(out)
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	if got[0]["name"] != "tank" || got[0]["compression"] != "" {
		t.Fatalf("%#v", got[0])
	}
}
