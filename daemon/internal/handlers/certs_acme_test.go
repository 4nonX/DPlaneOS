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
