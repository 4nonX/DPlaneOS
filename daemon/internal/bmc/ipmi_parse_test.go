package bmc

import "testing"

func TestParseSDRLine(t *testing.T) {
	cases := []struct {
		line, name, reading, status string
	}{
		// ipmitool sdr elist all
		{"CPU Temp         | 30h | ok  |  3.1 | 38 degrees C", "CPU Temp", "38 degrees C", "ok"},
		{"FAN1             | 41h | cr  | 29.1 | 300 RPM", "FAN1", "300 RPM", "cr"},
		{"PS1 Status       | C8h | ok  | 10.1 | Presence detected", "PS1 Status", "Presence detected", "ok"},
		// ipmitool sdr list
		{"CPU Temp         | 38 degrees C      | ok", "CPU Temp", "38 degrees C", "ok"},
	}
	for _, c := range cases {
		n, r, s, ok := parseSDRLine(c.line)
		if !ok || n != c.name || r != c.reading || s != c.status {
			t.Errorf("%q: %q %q %q %v", c.line, n, r, s, ok)
		}
	}
	if _, _, _, ok := parseSDRLine("garbage"); ok {
		t.Error("garbage parsed")
	}
	for in, want := range map[string]string{"ok": "ok", "unc": "warning", "cr": "critical", "lnr": "critical", "ns": "unknown"} {
		if got := ipmiSensorStatus(in); got != want {
			t.Errorf("status %q: %q, want %q", in, got, want)
		}
	}
}
