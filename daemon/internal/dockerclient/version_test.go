package dockerclient

import (
	"strings"
	"testing"
)

func TestNegotiateAPIVersion(t *testing.T) {
	cases := []struct{ clientMax, server, serverMin, want string }{
		{"1.47", "1.52", "1.44", "1.47"}, // Docker 29: our newest; 1.41 got HTTP 400 there
		{"1.47", "1.43", "1.12", "1.43"}, // Docker 24: the daemon's
		{"1.47", "1.41", "1.12", "1.41"}, // Docker 20.10
		{"1.47", "1.60", "1.50", "1.50"}, // a future engine with a higher minimum
		{"1.47", "1.47", "", "1.47"},
	}
	for _, c := range cases {
		if got := negotiateAPIVersion(c.clientMax, c.server, c.serverMin); got != c.want {
			t.Errorf("negotiate(%s, %s, %s) = %s, want %s", c.clientMax, c.server, c.serverMin, got, c.want)
		}
	}
	if !apiVersionLess("1.9", "1.41") || apiVersionLess("1.41", "1.41") {
		t.Error("versions must compare numerically")
	}
}

// A pull that fails after it started reports the error in the stream with
// HTTP 200 (the shape below is what Engine 29 sends for a missing blob).
func TestPullStreamError(t *testing.T) {
	ok := `{"status":"Pulling from library/busybox","id":"1.36"}
{"status":"Digest: sha256:abc"}
{"status":"Status: Downloaded newer image for busybox:1.36"}`
	if err := pullStreamError(strings.NewReader(ok)); err != nil {
		t.Errorf("successful pull: %v", err)
	}
	failed := `{"status":"Pulling from lab/app","id":"v2"}
{"status":"Pulling fs layer","progressDetail":{},"id":"0123"}
{"errorDetail":{"message":"blob unknown: blob unknown to registry"},"error":"blob unknown: blob unknown to registry"}`
	err := pullStreamError(strings.NewReader(failed))
	if err == nil || !strings.Contains(err.Error(), "blob unknown") {
		t.Errorf("failed pull reported as %v", err)
	}
}
