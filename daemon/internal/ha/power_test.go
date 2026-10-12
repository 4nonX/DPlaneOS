package ha

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dplaned/internal/bmc"
	"dplaned/internal/secrets"
	"dplaned/internal/testdb"
)

// A takeover powers off the node that failed, through the target of that
// node, and no other machine: with three nodes, the one configured target
// used to be switched off whoever had failed.
func TestPowerOffGoesToTheNodeThatFailed(t *testing.T) {
	db := testdb.Open(t)
	if err := secrets.Init(filepath.Join(t.TempDir(), "secrets.key")); err != nil {
		t.Fatal(err)
	}
	oldOff, oldState, oldSleep := bmcPowerOff, bmcPowerState, sleep
	t.Cleanup(func() { bmcPowerOff, bmcPowerState, sleep = oldOff, oldState, oldSleep })
	sleep = func(time.Duration) {}

	off := map[string]string{} // host -> password it was switched off with
	bmcPowerOff = func(_ context.Context, c bmc.Credentials, _ bmc.PinnedCert) error {
		off[c.Host] = c.Password
		return nil
	}
	bmcPowerState = func(_ context.Context, c bmc.Credentials, _ bmc.PinnedCert) (bmc.PowerState, error) {
		if _, ok := off[c.Host]; ok {
			return bmc.PowerOff, nil
		}
		return bmc.PowerOn, nil
	}

	pwB, pwC := "secret-of-b", "secret-of-c"
	for _, tgt := range []struct {
		t  PowerTarget
		pw *string
	}{
		{PowerTarget{Node: "node-b", Kind: "bmc", Enabled: true, Host: "10.0.0.2", Username: "admin"}, &pwB},
		{PowerTarget{Node: "node-c", Kind: "bmc", Enabled: true, Host: "10.0.0.3", Username: "admin"}, &pwC},
		{PowerTarget{Node: "node-d", Kind: "bmc", Enabled: false, Host: "10.0.0.4", Username: "admin"}, &pwC},
	} {
		if err := SavePowerTarget(db, tgt.t, tgt.pw); err != nil {
			t.Fatal(err)
		}
	}

	if err := PowerOff(db, "node-c"); err != nil {
		t.Fatalf("power off node-c: %v", err)
	}
	if len(off) != 1 || off["10.0.0.3"] != pwC {
		t.Fatalf("node-c failed; switched off: %v", off)
	}
	// A node without a target, or with one that is not enabled: nothing is
	// switched off, and the caller is told so.
	for _, node := range []string{"node-d", "node-e"} {
		if err := PowerOff(db, node); !errors.Is(err, ErrNoPowerTarget) {
			t.Errorf("%s: want ErrNoPowerTarget, got %v", node, err)
		}
	}
	if len(off) != 1 {
		t.Errorf("a machine was switched off for a node without a target: %v", off)
	}

	// The password is stored sealed, kept when a later save leaves it out,
	// and never returned.
	var stored string
	if err := db.QueryRow(`SELECT password FROM power_targets WHERE node = 'node-b'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "" || strings.Contains(stored, pwB) {
		t.Errorf("the password is not sealed in the database: %q", stored)
	}
	if err := SavePowerTarget(db, PowerTarget{Node: "node-b", Kind: "bmc", Enabled: true, Host: "10.0.0.2", Username: "root"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := PowerOff(db, "node-b"); err != nil || off["10.0.0.2"] != pwB {
		t.Errorf("the password was not kept: %v %v", err, off)
	}
	list, _ := ListPowerTargets(db)
	if len(list) != 3 || !list[0].HasPassword {
		t.Errorf("list: %+v", list)
	}

	// A machine that does not go dark is a failed fencing, not a done one.
	powerOffConfirm, powerOffPoll = 20*time.Millisecond, time.Millisecond
	t.Cleanup(func() { powerOffConfirm, powerOffPoll = 60*time.Second, 5*time.Second })
	sleep = time.Sleep
	bmcPowerState = func(context.Context, bmc.Credentials, bmc.PinnedCert) (bmc.PowerState, error) {
		return bmc.PowerOn, nil
	}
	if err := PowerOff(db, "node-c"); err == nil || !strings.Contains(err.Error(), "did not report the machine as off") {
		t.Errorf("want a failure when the machine stays on, got %v", err)
	}
}

func TestPowerTargetTestAndPDU(t *testing.T) {
	db := testdb.Open(t)
	if err := secrets.Init(filepath.Join(t.TempDir(), "secrets.key")); err != nil {
		t.Fatal(err)
	}
	oldState, oldSleep := bmcPowerState, sleep
	t.Cleanup(func() { bmcPowerState, sleep = oldState, oldSleep })
	sleep = func(time.Duration) {}

	var hits []string
	pdu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pw, _ := r.BasicAuth()
		hits = append(hits, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/" {
			return
		}
		if user != "pdu" || pw != "outlet-pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}))
	defer pdu.Close()

	pw := "outlet-pw"
	target := PowerTarget{Node: "node-b", Kind: "pdu", Enabled: true, OffURL: pdu.URL + "/outlet/2/off", StatusURL: pdu.URL + "/outlet/2", Username: "pdu", Method: "post"}
	if err := SavePowerTarget(db, target, &pw); err != nil {
		t.Fatal(err)
	}

	// The test asks for the status and switches nothing.
	res := TestPowerTarget(context.Background(), db, "node-b")
	if !res.OK || res.Confirms != "status address answers" {
		t.Errorf("test: %+v", res)
	}
	for _, h := range hits {
		if strings.Contains(h, "/off") {
			t.Fatalf("the test switched the outlet: %v", hits)
		}
	}
	// A wrong password is found by the test.
	wrong := "nope"
	if err := SavePowerTarget(db, target, &wrong); err != nil {
		t.Fatal(err)
	}
	if res := TestPowerTarget(context.Background(), db, "node-b"); res.OK || !strings.Contains(res.Detail, "401") {
		t.Errorf("a wrong password passed the test: %+v", res)
	}
	// Without a status address the test says what it could not check.
	noStatus := target
	noStatus.StatusURL = ""
	if err := SavePowerTarget(db, noStatus, &pw); err != nil {
		t.Fatal(err)
	}
	if res := TestPowerTarget(context.Background(), db, "node-b"); !res.OK || res.Confirms != "reachable" || !strings.Contains(res.Detail, "not known") {
		t.Errorf("without a status address: %+v", res)
	}

	// Fencing switches the outlet off, with the stored method and sign-in.
	hits = nil
	if err := PowerOff(db, "node-b"); err != nil {
		t.Fatalf("PDU power off: %v", err)
	}
	if len(hits) != 1 || hits[0] != "POST /outlet/2/off" {
		t.Errorf("requests to the PDU: %v", hits)
	}

	// A BMC that does not answer fails the test with what happened.
	bmcPowerState = func(context.Context, bmc.Credentials, bmc.PinnedCert) (bmc.PowerState, error) {
		return bmc.PowerUnknown, errors.New("connection timed out")
	}
	if err := SavePowerTarget(db, PowerTarget{Node: "node-c", Kind: "bmc", Enabled: true, Host: "10.0.0.3", Username: "admin"}, &pw); err != nil {
		t.Fatal(err)
	}
	if res := TestPowerTarget(context.Background(), db, "node-c"); res.OK || !strings.Contains(res.Detail, "timed out") {
		t.Errorf("unreachable BMC: %+v", res)
	}
	if res := TestPowerTarget(context.Background(), db, "node-x"); res.OK {
		t.Errorf("a node without a target passed: %+v", res)
	}
}

// What becomes an argument of ipmitool, or a request, is checked when it is
// stored.
func TestPowerTargetValidation(t *testing.T) {
	bad := map[string]PowerTarget{
		"host is an option":      {Node: "n", Kind: "bmc", Host: "-oProxyCommand=x"},
		"host with a space":      {Node: "n", Kind: "bmc", Host: "10.0.0.1 -U root"},
		"user is an option":      {Node: "n", Kind: "bmc", Host: "10.0.0.1", Username: "-f/etc/shadow"},
		"node with a slash":      {Node: "../x", Kind: "bmc", Host: "10.0.0.1"},
		"pdu without address":    {Node: "n", Kind: "pdu"},
		"pdu file url":           {Node: "n", Kind: "pdu", OffURL: "file:///etc/shadow"},
		"pdu credentials in url": {Node: "n", Kind: "pdu", OffURL: "http://u:p@pdu/off"},
		"unknown kind":           {Node: "n", Kind: "ssh", Host: "10.0.0.1"},
		"own BMC as a target":    {Node: "", Kind: "bmc", Host: "10.0.0.1", Enabled: true},
	}
	for name, tgt := range bad {
		if err := tgt.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, tgt := range map[string]PowerTarget{
		"bmc":         {Node: "8f3a-node", Kind: "bmc", Host: "bmc-b.lan", Username: "ADMIN"},
		"bmc ipv6":    {Node: "n", Kind: "bmc", Host: "fd00::12", Username: "root"},
		"domain user": {Node: "n", Kind: "bmc", Host: "10.0.0.1", Username: `LAB\svc-fence`},
		"pdu":         {Node: "n", Kind: "pdu", OffURL: "https://pdu.lan/outlet/3/off", StatusURL: "https://pdu.lan/outlet/3"},
		"own BMC":     {Node: "", Kind: "bmc", Host: "10.0.0.9", Username: "admin"},
	} {
		if err := tgt.Validate(); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}
