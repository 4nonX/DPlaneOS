package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Captured from OpenZFS 2.2 (`zpool import -d <dir>` of an exported pool).
const zpoolImportOutput = `   pool: hs
     id: 4050927702022983963
  state: ONLINE
 action: The pool can be imported using its name or numeric identifier.
 config:

	hs                                ONLINE
	  mirror-0                        ONLINE
	    /var/tmp/hs/ata-DISK_SERIAL1  ONLINE
	    /var/tmp/hs/ata-DISK_SERIAL2  ONLINE
	spares
	  /var/tmp/hs/ata-DISK_SERIAL4
	logs
	  /var/tmp/hs/ata-DISK_SERIAL3    ONLINE
`

// Captured from OpenZFS 2.2: a mirror member that is gone.
const zpoolStatusMissingMember = `  pool: hs
 state: DEGRADED
status: One or more devices could not be opened.  Sufficient replicas exist for
	the pool to continue functioning in a degraded state.
action: Attach the missing device and online it using 'zpool online'.
   see: https://openzfs.github.io/openzfs-docs/msg/ZFS-8000-2Q
config:

	NAME                              STATE     READ WRITE CKSUM
	hs                                DEGRADED     0     0     0
	  mirror-0                        DEGRADED     0     0     0
	    /var/tmp/hs/ata-DISK_SERIAL1  ONLINE       0     0     0
	    10021633744408138498          UNAVAIL      0     0     0  was /var/tmp/hs/ata-DISK_SERIAL2
	logs
	  /var/tmp/hs/ata-DISK_SERIAL3    ONLINE       0     0     0
	spares
	  /var/tmp/hs/ata-DISK_SERIAL4    AVAIL

errors: No known data errors
`

func TestParseImportablePools(t *testing.T) {
	pools := parseImportablePools(zpoolImportOutput)
	if len(pools) != 1 || pools[0].name != "hs" || pools[0].guid != "4050927702022983963" {
		t.Fatalf("pools: %+v", pools)
	}
	want := map[string]bool{"/var/tmp/hs/ata-DISK_SERIAL1": true, "/var/tmp/hs/ata-DISK_SERIAL2": true, "/var/tmp/hs/ata-DISK_SERIAL3": true}
	for _, m := range pools[0].members {
		if !want[m] {
			t.Errorf("unexpected member %q (groups and the pool itself are not members)", m)
		}
		delete(want, m)
	}
	if len(want) != 0 {
		t.Errorf("members missing: %v", want)
	}
}

// The registry used to be updated (pool name "", the pool is not imported)
// before it was asked which pool the disk belonged to, so no import ever ran.
// Now: import only the pool the disk was last seen in, and only if that pool
// lists the disk as a member.
func TestImportDecision(t *testing.T) {
	cases := []struct {
		disk, lastPool string
		want           bool
	}{
		{"ata-DISK_SERIAL2", "hs", true},
		{"ata-DISK_SERIAL3", "hs", true},    // log device
		{"ata-DISK_SERIAL2", "", false},     // never seen in a pool here: a foreign disk
		{"ata-DISK_SERIAL2", "tank", false}, // registry says another pool
		{"ata-OTHER", "hs", false},          // not a member of hs
	}
	for _, c := range cases {
		name, guid, ok := importDecision(zpoolImportOutput, c.disk, c.lastPool)
		if ok != c.want || (ok && (name != "hs" || guid != "4050927702022983963")) {
			t.Errorf("disk %s last pool %q: got %q %q %v, want import=%v", c.disk, c.lastPool, name, guid, ok, c.want)
		}
	}
	if !vdevIsDisk("ata-DISK_SERIAL2-part1", "ata-DISK_SERIAL2") || vdevIsDisk("ata-DISK_SERIAL20", "ata-DISK_SERIAL2") {
		t.Error("partition matching")
	}
}

// A missing member is listed by GUID; its former path follows "was".
func TestFindFaultedVdevsWasPath(t *testing.T) {
	fv := findFaultedVdevs(zpoolStatusMissingMember)
	if len(fv) != 1 {
		t.Fatalf("faulted vdevs: %+v", fv)
	}
	if fv[0].Pool != "hs" || fv[0].VdevPath != "10021633744408138498" || fv[0].State != "UNAVAIL" || fv[0].Was != "/var/tmp/hs/ata-DISK_SERIAL2" {
		t.Errorf("got %+v", fv[0])
	}
}

// Requests through nginx come over the same Unix socket as the udev helper,
// with a user's session: only the internal token admits a disk event.
func TestDiskEventNeedsInternalToken(t *testing.T) {
	old := cronToken
	cronToken = "boot-token"
	defer func() { cronToken = old }()
	cases := []struct {
		name, remote, token string
		want                int
	}{
		{"socket without token (nginx, any session)", "@", "", http.StatusForbidden},
		{"socket with a wrong token", "@", "guess", http.StatusForbidden},
		{"remote with the token", "192.168.1.9:4000", "boot-token", http.StatusForbidden},
		// Admitted: the body is then checked (empty: 400)
		{"socket with the token", "@", "boot-token", http.StatusBadRequest},
		{"loopback with the token", "127.0.0.1:4000", "boot-token", http.StatusBadRequest},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/api/internal/disk-event", strings.NewReader(""))
		r.RemoteAddr = c.remote
		r.Header.Set("X-Session-ID", "a-valid-user-session")
		if c.token != "" {
			r.Header.Set("X-Internal-Token", c.token)
		}
		w := httptest.NewRecorder()
		HandleDiskEvent(w, r)
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.want)
		}
	}
	cronToken = ""
	r := httptest.NewRequest("POST", "/api/internal/disk-event", strings.NewReader(""))
	r.RemoteAddr = "@"
	w := httptest.NewRecorder()
	HandleDiskEvent(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("no daemon token: status %d, want 403", w.Code)
	}
}
