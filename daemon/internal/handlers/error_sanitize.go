// error_sanitize.go - Systematic error sanitization for user-facing API responses.
//
// Rule: raw Go errors, CLI output, system paths, and library internals must never
// reach the HTTP response body. This file provides:
//
//  1. userErr() - builds a structured { error, code, guide } response.
//  2. Category-specific sanitizers that map technical errors to user messages
//     while logging the technical details separately.
//  3. respondUserErr() / respondUserErrStatus() - response helpers that enforce
//     the structured format and add logging.
//
// Usage:
//
//	if err != nil {
//	    respondUserErr(w, sanitizeZFS(err), "zfs_dataset_error", "Check pool health under Storage → Pools.")
//	    return
//	}
package handlers

import (
	"fmt"
	"log"
	"net/http"
	"strings"
)

// UserError is the canonical structured error response sent to clients.
// error: one sentence, plain English, no technical details.
// code:  machine-readable identifier the UI can route on.
// guide: optional next step for the user (where to go, what to do).
type UserError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
	Guide string `json:"guide,omitempty"`
}

// userErr constructs a UserError. guide is optional; pass "" to omit.
func userErr(message, code, guide string) UserError {
	return UserError{Error: message, Code: code, Guide: guide}
}

// respondUserErr sends a 400 Bad Request with the structured UserError.
// The raw technical error (if provided) is logged at WARNING level but
// never included in the response body.
func respondUserErr(w http.ResponseWriter, ue UserError, rawErr error) {
	respondUserErrStatus(w, http.StatusBadRequest, ue, rawErr)
}

// respondUserErrStatus sends the given HTTP status with the structured UserError.
func respondUserErrStatus(w http.ResponseWriter, status int, ue UserError, rawErr error) {
	if rawErr != nil {
		log.Printf("API ERROR [%s]: %v", ue.Code, rawErr)
	}
	respondJSON(w, status, map[string]any{
		"success": false,
		"error":   ue.Error,
		"code":    ue.Code,
		"guide":   ue.Guide,
	})
}

// ── Category sanitizers ───────────────────────────────────────────────────────
// Each sanitizer classifies a raw technical error into a user-readable message
// and returns a UserError. The caller decides the HTTP status code.

// SanitizeSSH maps SSH library errors to user-facing messages.
// SSH errors routinely contain server identity details, cipher negotiation
// specifics, and authentication method lists that must not reach the client.
func SanitizeSSH(err error) UserError {
	if err == nil {
		return userErr("", "", "")
	}
	msg := err.Error()
	switch {
	case contains(msg, "no supported methods remain", "unable to authenticate"):
		return userErr("SSH authentication failed. Check that the correct SSH key is configured for this remote host.",
			"ssh_auth_failed",
			"Go to Replication → Remotes and verify the SSH key is installed on the peer.")
	case contains(msg, "unable to connect", "connection refused", "no route to host", "i/o timeout"):
		return userErr("Cannot reach the remote host. Check that the host address is correct and the host is online.",
			"ssh_connection_failed",
			"Verify the host address and that SSH is running on the remote system.")
	case contains(msg, "host key verification failed", "known_hosts", "fingerprint"):
		return userErr("The remote host's SSH key has changed or has not been trusted yet.",
			"ssh_host_key_changed",
			"Go to Replication → Remotes → Test Connection to review and accept the new host key.")
	case contains(msg, "permission denied"):
		return userErr("SSH connection was refused. The key may not be authorized on the remote host.",
			"ssh_permission_denied",
			"Ensure the DPlaneOS SSH public key is added to the remote host's authorized_keys.")
	default:
		return userErr("SSH connection failed. Check network connectivity and SSH configuration.",
			"ssh_error", "")
	}
}

// SanitizeZFS maps ZFS command and library errors to user-facing messages.
func SanitizeZFS(err error) UserError {
	if err == nil {
		return userErr("", "", "")
	}
	msg := err.Error()
	switch {
	case contains(msg, "dataset already exists", "already exists"):
		return userErr("A dataset or snapshot with that name already exists.",
			"zfs_already_exists", "")
	case contains(msg, "dataset is busy", "device busy"):
		return userErr("The dataset is in use and cannot be modified right now. Check for active NFS exports, SMB shares, or open files.",
			"zfs_dataset_busy",
			"Stop any active exports or shares on this dataset, then retry.")
	case contains(msg, "no space left", "out of space", "quota exceeded"):
		return userErr("The pool has no space available for this operation.",
			"zfs_no_space",
			"Free space by deleting snapshots or datasets, or add capacity to the pool.")
	case contains(msg, "pool or dataset is read only", "read-only"):
		return userErr("This dataset is in read-only mode.",
			"zfs_readonly", "")
	case contains(msg, "permission denied"):
		return userErr("Insufficient permissions for this ZFS operation.",
			"zfs_permission_denied", "")
	case contains(msg, "dataset does not exist", "no such pool"):
		return userErr("The dataset or pool no longer exists.",
			"zfs_not_found", "")
	case contains(msg, "snapshot has dependent clones"):
		return userErr("This snapshot has dependent clones that must be removed first.",
			"zfs_has_clones", "")
	default:
		return userErr("The storage operation failed. Check pool health under Storage → Pools.",
			"zfs_error", "")
	}
}

// SanitizeDomainJoin maps Active Directory / Kerberos errors to user-facing messages.
// AD join failures often contain domain topology details that must not be exposed.
func SanitizeDomainJoin(err error, rawOutput string) UserError {
	combined := ""
	if err != nil {
		combined = err.Error()
	}
	combined += " " + rawOutput

	switch {
	case contains(combined, "ADS support not compiled in"):
		return userErr("The installed Samba has no Active Directory support.",
			"samba_no_ads", "Rebuild the system with the default services.dplaneos.samba.package.")
	case contains(combined, "Invalid credentials", "attempted logon is invalid", "NT_STATUS_LOGON_FAILURE", "NT_STATUS_WRONG_PASSWORD", "NT_STATUS_NO_SUCH_USER"):
		return userErr("The domain refused the username or password.",
			"invalid_credentials",
			"Verify the username and password are correct for this domain.")
	// before the kinit case (skew errors come from kinit too); not "time":
	// matching is case-insensitive and "timeout"/"runtime" are not skew
	case contains(combined, "clock skew", "KRB_AP_ERR_SKEW", "time skew"):
		return userErr("System clock is too far out of sync with the domain controller. Kerberos requires clocks within 5 minutes.",
			"ntp_skew",
			"Enable NTP synchronization under Settings → System → Time.")
	case contains(combined, "KDC_ERR_C_PRINCIPAL_UNKNOWN", "KDC_ERR_PREAUTH_FAILED", "kinit"):
		return userErr("Kerberos authentication failed. Check the domain admin credentials.",
			"kerberos_auth_failed",
			"Verify the username and password are correct for this domain.")
	case contains(combined, "No logon servers", "domain not found", "NETLOGON"):
		return userErr("Cannot find the domain controller. Check the domain name and network connectivity.",
			"domain_not_found",
			"Verify the domain name is correct and the NAS can reach the domain controller.")
	case contains(combined, "Account is disabled", "ACCOUNT_DISABLED", "NT_STATUS_ACCOUNT_DISABLED"):
		return userErr("The domain admin account used for join is disabled.",
			"domain_account_disabled", "")
	case contains(combined, "already joined", "already a member"):
		return userErr("This system is already joined to a domain. Leave the current domain before joining a new one.",
			"already_joined", "")
	case contains(combined, "Access denied", "NT_STATUS_ACCESS_DENIED"):
		return userErr("The domain admin account does not have permission to join computers to this domain.",
			"domain_access_denied",
			"Use an account with the 'Add workstations to domain' right, or ask your AD administrator.")
	default:
		return userErr("Domain join failed. Check credentials, network connectivity, and that the domain controller is reachable.",
			"domain_join_failed", "")
	}
}

// SanitizeDB maps database errors to user-facing messages without exposing
// query structure, table names, or constraint details.
func SanitizeDB(err error) UserError {
	if err == nil {
		return userErr("", "", "")
	}
	msg := err.Error()
	switch {
	case contains(msg, "unique constraint", "duplicate key", "already exists"):
		return userErr("A record with that name or identifier already exists.",
			"db_duplicate", "")
	case contains(msg, "foreign key constraint", "violates foreign key"):
		return userErr("This record is referenced by other data and cannot be removed.",
			"db_foreign_key", "")
	case contains(msg, "not found", "no rows"):
		return userErr("The requested record was not found.",
			"db_not_found", "")
	default:
		return userErr("A database error occurred. Please try again.",
			"db_error", "")
	}
}

// SanitizeServiceControl maps systemctl / service startup errors to safe messages.
func SanitizeServiceControl(service string, err error) UserError {
	if err == nil {
		return userErr("", "", "")
	}
	return userErr(
		fmt.Sprintf("Failed to change the state of the %s service. Check the system journal for details.", service),
		"service_control_error",
		"Use the System Logs page to view recent service errors.",
	)
}

// SanitizeHTTP maps HTTP client errors (for proxy/ACME/webhook operations).
func SanitizeHTTP(operation string, err error) UserError {
	if err == nil {
		return userErr("", "", "")
	}
	msg := err.Error()
	switch {
	case contains(msg, "connection refused", "no route to host", "dial"):
		return userErr(fmt.Sprintf("Cannot reach the %s endpoint. Check the URL and network connectivity.", operation),
			"http_connection_failed", "")
	case contains(msg, "tls", "certificate", "x509"):
		return userErr(fmt.Sprintf("TLS certificate error connecting to %s. The certificate may be expired or untrusted.", operation),
			"http_tls_error", "")
	case contains(msg, "timeout"):
		return userErr(fmt.Sprintf("The %s request timed out. The remote server may be overloaded.", operation),
			"http_timeout", "")
	default:
		return userErr(fmt.Sprintf("The %s request failed. Check the URL and try again.", operation),
			"http_error", "")
	}
}

// contains checks if s contains any of the substrings (case-insensitive).
func contains(s string, subs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
