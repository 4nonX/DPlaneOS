package handlers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dplaned/internal/jobs"
)

func writeTestCert(t *testing.T, path string, notAfter time.Time) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "nas.example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestCertNamesAreValidated(t *testing.T) {
	prev := ConfigDir
	ConfigDir = t.TempDir()
	t.Cleanup(func() { ConfigDir = prev })
	h := NewCertHandler()
	for _, name := range []string{"../../etc/cron.d/x", "a b", "x;", ""} {
		if r := call(t, h.RequestACME, req{method: "POST", body: map[string]any{"name": name, "domain": "nas.example.com", "email": "a@example.com"}}); r.code != 400 {
			t.Errorf("ACME name %q: %s", name, r)
		}
		if r := call(t, h.ActivateCert, req{method: "POST", body: map[string]any{"name": name}}); r.code != 400 {
			t.Errorf("activate name %q: %s", name, r)
		}
	}
	for _, domain := range []string{"nas", "a b.example.com", "x.example.com/../", "-x.example.com"} {
		if r := call(t, h.RequestACME, req{method: "POST", body: map[string]any{"name": "web", "domain": domain, "email": "a@example.com"}}); r.code != 400 {
			t.Errorf("ACME domain %q: %s", domain, r)
		}
	}
}

// Only ACME certificates (with renewal data) that are due are renewed.
func TestRenewDueCertificatesSkipsOthers(t *testing.T) {
	prev := ConfigDir
	ConfigDir = t.TempDir()
	t.Cleanup(func() { ConfigDir = prev })
	ssl := filepath.Join(ConfigDir, "ssl")
	_ = os.MkdirAll(ssl, 0700)
	// self-signed, expiring tomorrow: no renewal data, not ours to renew
	writeTestCert(t, filepath.Join(ssl, "selfsigned.crt"), time.Now().Add(24*time.Hour))
	// ACME, valid for another 80 days: not due
	writeTestCert(t, filepath.Join(ssl, "web.crt"), time.Now().Add(80*24*time.Hour))
	_ = os.WriteFile(filepath.Join(ssl, "web.crt.meta"), []byte(`{"email":"a@example.com","staging":true}`), 0644)

	fakeCommands(t, nil)
	done := make(chan [2]int, 1)
	jobs.Start("test_renew", func(j *jobs.Job) {
		r, f := NewCertHandler().renewDueCertificates(j)
		done <- [2]int{r, f}
		j.Done(nil)
	})
	select {
	case got := <-done:
		if got != [2]int{0, 0} {
			t.Errorf("renewed/failed = %v, want none", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("renewal check did not finish")
	}
}

// Another certificate authority's directory: https only, no credentials in
// the address, the staging switch is Let's Encrypt's, and a CA certificate
// is a certificate.
func TestACMEOptions(t *testing.T) {
	const pem = "-----BEGIN CERTIFICATE-----\nMIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw\nDgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow\nEjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d\n7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B\n5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr\nBgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1\nNDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2zpJEPQyz6/l\nWf86aX6PepsntZv2GYlA5UpabfT2EZICICpJ5h/iI+i341gBmLiAFQOyTDT+/wQc\n6MF9+Yw1Yy0t\n-----END CERTIFICATE-----\n"
	ok := []acmeOptions{
		{Email: "a@example.com"},
		{Email: "a@example.com", Staging: true},
		{Email: "a@example.com", DirectoryURL: "https://ca.example.lan/acme/acme/directory"},
		{Email: "a@example.com", DirectoryURL: "https://192.168.1.1:14000/dir", CAPEM: pem},
	}
	for _, o := range ok {
		if err := o.validate(); err != nil {
			t.Errorf("%+v refused: %v", o, err)
		}
	}
	bad := map[string]acmeOptions{
		"no address":         {Email: "nobody"},
		"plain http":         {Email: "a@example.com", DirectoryURL: "http://ca.example.lan/dir"},
		"credentials":        {Email: "a@example.com", DirectoryURL: "https://user:pw@ca.example.lan/dir"},
		"a file":             {Email: "a@example.com", DirectoryURL: "file:///etc/shadow"},
		"staging elsewhere":  {Email: "a@example.com", DirectoryURL: "https://ca.example.lan/dir", Staging: true},
		"CA without address": {Email: "a@example.com", CAPEM: pem},
		"not a certificate":  {Email: "a@example.com", DirectoryURL: "https://ca.example.lan/dir", CAPEM: "hello"},
	}
	for name, o := range bad {
		if err := o.validate(); err == nil {
			t.Errorf("%s accepted: %+v", name, o)
		}
	}
}
