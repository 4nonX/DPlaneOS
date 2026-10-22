package handlers

import (
	"errors"
	"testing"
)

func TestSanitizeDomainJoin(t *testing.T) {
	exit := errors.New("exit status 255")
	for _, c := range []struct{ out, code string }{
		{"Failed to join domain: failed to connect to AD: Invalid credentials", "invalid_credentials"},
		{"Failed to join domain: NT_STATUS_LOGON_FAILURE", "invalid_credentials"},
		{"Failed to join domain: failed to lookup DC info for domain 'CORP.TEST' over rpc: The attempted logon is invalid. This is either due to a bad username or authentication information.", "invalid_credentials"},
		{"ADS support not compiled in", "samba_no_ads"},
		{"kerberos_kinit_password: KRB_AP_ERR_SKEW", "ntp_skew"},
		{"Clock skew too great", "ntp_skew"},
		{"Failed to join domain: failed to connect to AD: Connection timeout", "domain_join_failed"},
		{"No logon servers are currently available", "domain_not_found"},
		{"NT_STATUS_ACCESS_DENIED", "domain_access_denied"},
	} {
		if got := SanitizeDomainJoin(exit, c.out).Code; got != c.code {
			t.Errorf("%q: code %q, want %q", c.out, got, c.code)
		}
	}
}
