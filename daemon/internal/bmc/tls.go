package bmc

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// tlsPolicy controls how TLS certificates are verified for BMC connections.
// BMCs ship with self-signed certificates that cannot be CA-signed at scale.
// The correct response is TOFU (Trust On First Use) certificate pinning, not
// blanket InsecureSkipVerify. This matches the SSH key pinning pattern already
// used for replication peers in this codebase.
//
// On first connection: the presented certificate's SHA-256 fingerprint is
// captured and returned to the caller for storage. Subsequent connections
// verify the fingerprint matches. A MITM after initial enrollment is detected.
//
// An operator who needs to reset the pin (e.g. after a BMC firmware update
// that regenerates the cert) uses POST /api/bmc/reset-cert, which
// requires an AAL2 session.
type tlsPolicy int

const (
	// tlsPolicyPinned verifies the cert fingerprint against a stored value.
	// Used for all connections after the initial enrollment.
	tlsPolicyPinned tlsPolicy = iota

	// tlsPolicyCaptureFirst accepts any cert and returns its fingerprint.
	// Used ONLY during the initial probe / enrollment call.
	// The fingerprint must be stored before the session is used for any
	// sensitive operation (power management, STONITH).
	tlsPolicyCaptureFirst
)

// PinnedCert holds the BMC TLS certificate fingerprint.
type PinnedCert struct {
	Fingerprint string // SHA-256 of DER-encoded leaf certificate, hex-encoded
	PinnedAt    time.Time
}

// httpClientPinned returns an HTTP client that verifies the BMC cert against
// the stored fingerprint. All Redfish calls after initial enrollment use this.
func httpClientPinned(pinned PinnedCert, timeoutSeconds int) (*http.Client, error) {
	if pinned.Fingerprint == "" {
		return nil, fmt.Errorf("no pinned certificate fingerprint: run BMC probe first to enroll the certificate")
	}
	expected := strings.ToLower(pinned.Fingerprint)
	return &http.Client{
		Timeout: time.Duration(timeoutSeconds) * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				// MinVersion hardens the TLS session regardless of pinning.
				MinVersion: tls.VersionTLS12,
				// We perform our own certificate verification via VerifyPeerCertificate.
				// InsecureSkipVerify must be true to disable Go's default chain
				// validation (BMC certs are self-signed so the chain check always
				// fails), but our custom VerifyPeerCertificate re-establishes
				// security by pinning the leaf certificate fingerprint.
				InsecureSkipVerify:    true, //nolint:gosec -- intentional; VerifyPeerCertificate below is the actual check
				VerifyPeerCertificate: pinnedVerifier(expected),
			},
		},
	}, nil
}

// httpClientCapture returns an HTTP client that accepts any certificate and
// captures its SHA-256 fingerprint. Used ONLY during initial enrollment.
// The fingerprint is stored via the capturedFP channel and must be saved
// before this client is used for any further requests.
func httpClientCapture(capturedFP chan<- string, timeoutSeconds int) *http.Client {
	return &http.Client{
		Timeout: time.Duration(timeoutSeconds) * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: true, //nolint:gosec -- TOFU enrollment only; fingerprint captured below
				VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
					if len(rawCerts) == 0 {
						return fmt.Errorf("no certificate presented")
					}
					fp := certFingerprint(rawCerts[0])
					select {
					case capturedFP <- fp:
					default:
					}
					return nil // accept; fingerprint stored by caller
				},
			},
		},
	}
}

// pinnedVerifier returns a VerifyPeerCertificate function that enforces the
// stored fingerprint. Called by the TLS stack after the handshake completes.
func pinnedVerifier(expectedFingerprint string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("BMC presented no certificate")
		}
		actual := certFingerprint(rawCerts[0])
		if actual != expectedFingerprint {
			return fmt.Errorf(
				"BMC certificate fingerprint mismatch: got %s, want %s. "+
					"If the BMC firmware was recently updated (which may regenerate its certificate), "+
					"reset the stored fingerprint via POST /api/bmc/reset-cert (BMC page: Reset certificate). "+
					"If you did not update the BMC firmware, this may indicate a MITM attack.",
				actual, expectedFingerprint)
		}
		return nil
	}
}

// certFingerprint returns the SHA-256 hex fingerprint of a DER-encoded cert.
func certFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}
