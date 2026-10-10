package monitoring

import (
	"context"
	"errors"
	"testing"

	"dplaned/internal/cmdutil"
	"dplaned/internal/security"
)

func fakeRun(t *testing.T, fn func(c cmdutil.Call) ([]byte, error)) *[]cmdutil.Call {
	var calls []cmdutil.Call
	restore := cmdutil.SetFakeForTest(func(c cmdutil.Call) ([]byte, error) {
		calls = append(calls, c)
		return fn(c)
	})
	t.Cleanup(restore)
	return &calls
}

func TestCheckZFS(t *testing.T) {
	c := &Checker{}
	for _, tc := range []struct {
		out  string
		err  error
		want HealthStatus
	}{
		{"all pools are healthy\n", nil, HealthOK},
		{"no pools available\n", errors.New("exit 1"), HealthOK},
		{"  pool: tank\n state: DEGRADED\nstatus: One or more devices could not be used\n", nil, HealthDegraded},
		{"  pool: tank\n state: UNAVAIL\n", nil, HealthUnavailable},
		{"", errors.New("zpool: not found"), HealthUnknown},
	} {
		calls := fakeRun(t, func(cmdutil.Call) ([]byte, error) { return []byte(tc.out), tc.err })
		got := c.checkZFS(context.Background())
		if got.Status != tc.want {
			t.Errorf("%q: %s (%s)", tc.out, got.Status, got.Reason)
		}
		if len(*calls) != 1 || (*calls)[0].Key != "zpool_status" {
			t.Errorf("command: %+v", *calls)
		}
	}
	got := (&Checker{}).checkZFS(context.Background())
	_ = got
	if s := poolProblemSummary("  pool: tank\n state: DEGRADED\n  pool: backup\n state: FAULTED\n"); s != "tank DEGRADED, backup FAULTED" {
		t.Errorf("summary %q", s)
	}
}

func TestBondLinks(t *testing.T) {
	const bond = `Ethernet Channel Bonding Driver: v5.15
Bonding Mode: fault-tolerance (active-backup)
MII Status: up

Slave Interface: eno1
MII Status: up
Speed: 1000 Mbps

Slave Interface: eno2
MII Status: down
Speed: Unknown
`
	up, down := bondLinks(bond)
	if up != 1 || down != 1 {
		t.Errorf("links up %d down %d", up, down)
	}
}

func TestCheckPostgresWithoutDB(t *testing.T) {
	if got := (&Checker{}).checkPostgres(context.Background()); got.Status != HealthUnknown {
		t.Errorf("no db: %s", got.Status)
	}
}

func TestHealthCommandsPassWhitelist(t *testing.T) {
	for key, args := range map[string][]string{
		"zpool_status": {"status", "-x"},
		"docker_cli":   {"ps", "--format", "{{.ID}}"},
	} {
		if err := security.ValidateCommand(key, args); err != nil {
			t.Errorf("%s %v: %v", key, args, err)
		}
	}
}
