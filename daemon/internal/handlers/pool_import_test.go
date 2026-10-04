package handlers

import (
	"strings"
	"testing"
)

const importScanTwoPools = `   pool: archive
     id: 15813948561712957512
  state: ONLINE
 status: The pool was last accessed by another system.
 action: The pool can be imported using its name or numeric identifier and
	the '-f' flag.
   see: https://openzfs.github.io/openzfs-docs/msg/ZFS-8000-EY
 config:

	archive                                   ONLINE
	  raidz2-0                                ONLINE
	    ata-ST4000VN008-2DR166_ZDH0A1B2       ONLINE
	    ata-ST4000VN008-2DR166_ZDH0A1B3       ONLINE
	    ata-ST4000VN008-2DR166_ZDH0A1B4       ONLINE
	    ata-ST4000VN008-2DR166_ZDH0A1B5       ONLINE

   pool: scratch
     id: 4242
  state: DEGRADED
 status: One or more devices are missing from the system.
 action: The pool can be imported despite missing or damaged devices.  The
	fault tolerance of the pool may be compromised if imported.
 config:

	scratch                    DEGRADED
	  mirror-0                 DEGRADED
	    ata-WDC_WD10_ABC       ONLINE
	    1234567890123456789    UNAVAIL
`

func TestParseImportScan(t *testing.T) {
	pools := parseImportScan(importScanTwoPools)
	if len(pools) != 2 {
		t.Fatalf("got %d pools, want 2: %+v", len(pools), pools)
	}

	a := pools[0]
	if a.Name != "archive" || a.GUID != "15813948561712957512" || a.State != "ONLINE" {
		t.Errorf("archive parsed wrong: %+v", a)
	}
	if !a.NeedsForce {
		t.Errorf("archive should need -f (last accessed by another system)")
	}
	if !strings.Contains(a.Action, "'-f' flag.") {
		t.Errorf("multi-line action not joined: %q", a.Action)
	}
	if !strings.HasPrefix(a.Config, "\tarchive") || !strings.Contains(a.Config, "ZDH0A1B5") {
		t.Errorf("config block wrong: %q", a.Config)
	}

	s := pools[1]
	if s.Name != "scratch" || s.GUID != "4242" || s.State != "DEGRADED" || s.NeedsForce {
		t.Errorf("scratch parsed wrong: %+v", s)
	}
	if strings.Contains(s.Config, "pool:") || !strings.Contains(s.Config, "UNAVAIL") {
		t.Errorf("scratch config wrong: %q", s.Config)
	}
}

func TestParseImportScanEmpty(t *testing.T) {
	for _, out := range []string{"", "no pools available to import\n"} {
		if pools := parseImportScan(out); len(pools) != 0 {
			t.Errorf("parseImportScan(%q) = %+v, want none", out, pools)
		}
	}
}
