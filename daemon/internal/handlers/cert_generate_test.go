package handlers

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteSelfSignedCert(t *testing.T) {
	dir := t.TempDir()
	keyFile, certFile := filepath.Join(dir, "a.key"), filepath.Join(dir, "a.crt")
	if err := writeSelfSignedCert(keyFile, certFile, "nas.example.test", "files.example.test, 192.0.2.10", 30); err != nil {
		t.Fatal(err)
	}
	// A web server can serve with the pair.
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		t.Fatalf("the pair does not load: %v", err)
	}
	raw, _ := os.ReadFile(certFile)
	block, _ := pem.Decode(raw)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "nas.example.test" || len(cert.DNSNames) != 2 || len(cert.IPAddresses) != 1 {
		t.Errorf("names: %q %v %v", cert.Subject.CommonName, cert.DNSNames, cert.IPAddresses)
	}
	if left := time.Until(cert.NotAfter); left < 29*24*time.Hour || left > 31*24*time.Hour {
		t.Errorf("valid for %v", left)
	}
	if err := cert.VerifyHostname("files.example.test"); err != nil {
		t.Error(err)
	}
	if st, err := os.Stat(keyFile); err != nil || (st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/') {
		t.Errorf("the key is readable by others: %v", st.Mode())
	}
}
