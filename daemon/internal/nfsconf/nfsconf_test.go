package nfsconf

import "testing"

// Verified with nfs-utils 2.6: the escaped line exports "/srv/tm/My Files";
// the raw one exported "/srv/tm/My" to host "Files" and exportfs -ra failed.
func TestLinesEscapeWhitespace(t *testing.T) {
	got := Lines("/tank/My Files", "192.168.1.0/24 nas.lan", "rw,no_subtree_check")
	want := "/tank/My\\040Files\t192.168.1.0/24(rw,no_subtree_check)\n" +
		"/tank/My\\040Files\tnas.lan(rw,no_subtree_check)\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if e := EscapePath("/a\tb\\c"); e != `/a\011b\134c` {
		t.Errorf("EscapePath: %q", e)
	}
	if e := EscapePath("/tank/plain-path_1"); e != "/tank/plain-path_1" {
		t.Errorf("plain path changed: %q", e)
	}
}
