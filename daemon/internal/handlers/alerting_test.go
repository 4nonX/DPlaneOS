package handlers

import (
	"net/mail"
	"strings"
	"testing"
	"time"

	"dplaned/internal/alerts"
)

func TestSMARTHealth(t *testing.T) {
	for _, c := range []struct {
		out, risk string
		has       bool
	}{
		{`{"smart_status":{"passed":true}}`, "ok", true},
		{`{"smart_status":{"passed":false}}`, "critical", true},
		{`{"smart_status":{"passed":true},"nvme_smart_health_information_log":{"critical_warning":4,"available_spare":100,"available_spare_threshold":10}}`, "critical", true},
		{`{"nvme_smart_health_information_log":{"critical_warning":0,"available_spare":5,"available_spare_threshold":10}}`, "critical", true},
		{`{"nvme_smart_health_information_log":{"percentage_used":104,"available_spare":100,"available_spare_threshold":10}}`, "warning", true},
		{`{"nvme_smart_health_information_log":{"media_errors":3,"available_spare":100,"available_spare_threshold":10}}`, "warning", true},
		{`{"device":{"name":"/dev/sr0"}}`, "ok", false},
	} {
		risk, reasons, has := smartHealth(c.out)
		if risk != c.risk || has != c.has {
			t.Errorf("%s: %s %v %v", c.out, risk, reasons, has)
		}
	}
}

// The same alert is sent once per window, also when ZED reports every
// damaged block (65 events for one scrub in the lab).
func TestAlertRepeatWindow(t *testing.T) {
	var mails, hooks int
	prevW, prevS, prevT, prevWS := webhookAlertFn, smtpAlertFn, telegramAlertFn, webSocketAlertFn
	defer func() { webhookAlertFn, smtpAlertFn, telegramAlertFn, webSocketAlertFn = prevW, prevS, prevT, prevWS }()
	var levels []string
	SetAlertDispatchers(func(level, e, r, m string) { hooks++; levels = append(levels, level) },
		func(s, b string) { mails++ }, func(l, ti, m string) {}, nil)
	for i := 0; i < 65; i++ {
		DispatchAlert("critical", "zfs.critical.data", "tank-repeat", "data error")
	}
	DispatchAlert("info", "replication.recovered", "nightly", "works again")
	if hooks != 2 || mails != 1 {
		t.Errorf("hooks %d mails %d", hooks, mails)
	}
	// The webhook gets the real level (it always said "critical").
	if len(levels) != 2 || levels[1] != "info" {
		t.Errorf("levels: %v", levels)
	}
	if !alertDue("x", time.Now().Add(2*AlertRepeatWindow)) {
		t.Error("never sent again")
	}
}

func TestBuildMail(t *testing.T) {
	to, _ := mail.ParseAddressList("ops@example.com, Bob <bob@example.com>")
	m := string(buildMail("NAS <nas@example.com>", to, "[DPlaneOS] pool.degraded: tank – Größe", "line 1\nline 2", time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)))
	for _, want := range []string{
		"From: NAS <nas@example.com>\r\n",
		"To: <ops@example.com>, \"Bob\" <bob@example.com>\r\n",
		"Subject: =?utf-8?q?",
		"Date: Sun, 18 Oct 2026 03:00:00 +0000\r\n",
		"Message-ID: <",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"\r\n\r\nline 1\r\nline 2\r\n",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("mail lacks %q:\n%s", want, m)
		}
	}
}

// Plain text: Markdown made the API refuse messages with "_" or "*".
func TestTelegramPlainText(t *testing.T) {
	msg := alerts.FormatAlert(alerts.TelegramAlert{Level: "WARNING", Title: "docker.auto_update_failed - my_stack", Message: "x*y"})
	if !strings.Contains(msg, "docker.auto_update_failed - my_stack") || strings.Contains(msg, "\\_") {
		t.Errorf("message: %q", msg)
	}
	long := alerts.FormatAlert(alerts.TelegramAlert{Message: strings.Repeat("é", 5000)})
	if n := len([]rune(long)); n > 4096 {
		t.Errorf("message of %d characters", n)
	}
}
