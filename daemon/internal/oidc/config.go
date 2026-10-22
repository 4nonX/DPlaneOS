// Package oidc implements the relying-party (client) side of OpenID Connect
// for DPlaneOS: provider discovery, JWKS retrieval with key rotation, and
// strict ID-token verification.
//
// This package deliberately performs NO database access and NO HTTP handler
// work. It is the cryptographic and protocol core only, so it can be unit
// tested in isolation and audited without dragging in the rest of the daemon.
// Session minting, user linking, PKCE/state storage, and route wiring live in
// the handler layer and call into this package.
package oidc

import (
	"errors"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// defaultClockSkew is the leeway applied to time-based claim checks (exp, iat,
// nbf) to tolerate small clock differences between this host and the IdP.
const defaultClockSkew = 2 * time.Minute

// Verification errors. Callers compare with errors.Is to map to HTTP responses
// without leaking detail to the end user.
var (
	ErrNoSignatures       = errors.New("oidc: id_token has no signatures")
	ErrMultipleSignatures = errors.New("oidc: id_token has multiple signatures")
	ErrUnsupportedAlg     = errors.New("oidc: id_token signed with disallowed algorithm")
	ErrKeyNotFound        = errors.New("oidc: no matching signing key for kid")
	ErrBadSignature       = errors.New("oidc: id_token signature verification failed")
	ErrIssuerMismatch     = errors.New("oidc: id_token issuer mismatch")
	ErrAudienceMismatch   = errors.New("oidc: id_token audience does not include client_id")
	ErrAzpMismatch        = errors.New("oidc: id_token azp does not match client_id")
	ErrExpired            = errors.New("oidc: id_token expired")
	ErrIssuedInFuture     = errors.New("oidc: id_token iat is in the future")
	ErrNotYetValid        = errors.New("oidc: id_token nbf is in the future")
	ErrNonceMissing       = errors.New("oidc: id_token nonce missing")
	ErrNonceMismatch      = errors.New("oidc: id_token nonce mismatch")
	ErrMissingSubject     = errors.New("oidc: id_token missing sub claim")
	ErrTokenTooOld        = errors.New("oidc: id_token iat is older than max age")
	ErrDiscoveryIssuer    = errors.New("oidc: discovery document issuer mismatch")
	ErrConfigInvalid      = errors.New("oidc: invalid configuration")
)

// Config is the verified relying-party configuration required to validate an
// ID token. It is intentionally small and value-typed; the handler layer is
// responsible for loading it (e.g. from the oidc_config singleton row) and for
// supplying per-login values such as the expected nonce.
type Config struct {
	// Issuer is the exact issuer string expected in the "iss" claim. It must
	// match the provider's discovery document issuer byte-for-byte (RFC 8414).
	Issuer string

	// ClientID is this relying party's client identifier, expected in "aud".
	ClientID string

	// AllowedAlgs restricts which JWS signing algorithms are accepted. Only
	// asymmetric algorithms should ever appear here. "none" and the HS* family
	// are rejected structurally (see allowedSignatureAlgorithms).
	AllowedAlgs []string

	// ClockSkew is the tolerated leeway for exp/iat/nbf. Zero selects the
	// package default (defaultClockSkew).
	ClockSkew time.Duration

	// MaxTokenAge, if non-zero, rejects tokens whose iat is older than this.
	// Useful to bound replay windows; zero disables the check.
	MaxTokenAge time.Duration

	// RequireNonce enforces that the token carries a nonce equal to the value
	// supplied at verification time. This must be true for the Authorization
	// Code flow used by DPlaneOS.
	RequireNonce bool

	// GroupsClaim names the claim that carries group membership
	// (default "groups"). It may be a list of strings or one string with
	// space- or comma-separated names.
	GroupsClaim string
}

// validate returns ErrConfigInvalid if the configuration cannot be used safely.
func (c Config) validate() error {
	if c.Issuer == "" || c.ClientID == "" {
		return ErrConfigInvalid
	}
	if len(c.AllowedAlgs) == 0 {
		return ErrConfigInvalid
	}
	return nil
}

// skew returns the effective clock skew, applying the default when unset.
func (c Config) skew() time.Duration {
	if c.ClockSkew <= 0 {
		return defaultClockSkew
	}
	return c.ClockSkew
}

// allowedSignatureAlgorithms converts the configured algorithm names into the
// go-jose type, dropping anything that is not an explicitly supported,
// asymmetric algorithm. This is the structural defense against "alg: none" and
// HMAC confusion attacks: such algorithms can never enter the allowlist passed
// to jose.ParseSigned, so a token using them fails to parse.
func (c Config) allowedSignatureAlgorithms() []jose.SignatureAlgorithm {
	supported := map[string]jose.SignatureAlgorithm{
		"RS256": jose.RS256,
		"RS384": jose.RS384,
		"RS512": jose.RS512,
		"ES256": jose.ES256,
		"ES384": jose.ES384,
		"ES512": jose.ES512,
		"PS256": jose.PS256,
		"PS384": jose.PS384,
		"PS512": jose.PS512,
	}
	out := make([]jose.SignatureAlgorithm, 0, len(c.AllowedAlgs))
	seen := make(map[jose.SignatureAlgorithm]bool)
	for _, name := range c.AllowedAlgs {
		if alg, ok := supported[name]; ok && !seen[alg] {
			out = append(out, alg)
			seen[alg] = true
		}
	}
	return out
}
