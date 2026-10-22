package handlers

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// Alert mail. net/smtp.SendMail (used before) cannot speak implicit TLS
// (port 465, what many providers give), ignored the TLS setting, and the
// message had no Date, Message-ID or charset (mail servers reject or file
// such mail as spam; the alerts contain non-ASCII text). Recipients written
// "a@x, b@y" became " b@y", which servers refuse.

// smtpTimeout bounds one delivery (connect, TLS, auth and data).
var smtpTimeout = 30 * time.Second

// buildMail renders an RFC 5322 message.
func buildMail(from string, to []*mail.Address, subject, body string, now time.Time) []byte {
	host, _ := os.Hostname()
	if host == "" {
		host = "dplaneos"
	}
	id := make([]byte, 12)
	rand.Read(id) //nolint:errcheck
	rcpt := make([]string, len(to))
	for i, a := range to {
		rcpt[i] = a.String()
	}
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(rcpt, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: <" + hex.EncodeToString(id) + "@" + host + ">\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\r\n") {
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}

// sendMail delivers one message with the configured server.
//
//   - Port 465: implicit TLS (SMTPS).
//   - Otherwise STARTTLS when the server offers it; required when TLS is set.
func sendMail(cfg SMTPConfig, subject, body string) error {
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return fmt.Errorf("from address: %w", err)
	}
	to, err := mail.ParseAddressList(cfg.To)
	if err != nil {
		return fmt.Errorf("recipients: %w", err)
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	deadline := time.Now().Add(smtpTimeout)
	dialer := &net.Dialer{Timeout: 15 * time.Second, Deadline: deadline}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	implicit := cfg.Port == 465
	var conn net.Conn
	if implicit {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	conn.SetDeadline(deadline) //nolint:errcheck
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP greeting: %w", err)
	}
	defer c.Close()
	if host, _ := os.Hostname(); host != "" {
		if err := c.Hello(host); err != nil {
			return fmt.Errorf("HELO: %w", err)
		}
	}
	if !implicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("STARTTLS: %w", err)
			}
		} else if cfg.TLS {
			return fmt.Errorf("the server does not offer STARTTLS (TLS is required in the settings; port 465 uses implicit TLS)")
		}
	}
	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return fmt.Errorf("the server does not offer authentication")
		}
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("login: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, a := range to {
		if err := c.Rcpt(a.Address); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", a.Address, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(buildMail(cfg.From, to, subject, body, time.Now())); err != nil {
		return fmt.Errorf("writing the message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("the server refused the message: %w", err)
	}
	return c.Quit()
}
