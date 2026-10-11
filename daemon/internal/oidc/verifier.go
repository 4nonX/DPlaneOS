package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Claims holds the validated standard and DPlaneOS-relevant claims extracted
// from a verified ID token. Fields are populated only after the signature and
// all registered-claim checks have passed.
type Claims struct {
	Issuer            string
	Subject           string
	Audience          []string
	AuthorizedParty   string
	Email             string
	EmailVerified     bool
	Name              string
	PreferredUsername string
	Groups            []string
	Nonce             string
	IssuedAt          int64
	Expiry            int64
	NotBefore         int64
}

// rawClaims mirrors the JWT payload. Audience and email_verified use flexible
// unmarshaling because IdPs legitimately encode them in more than one shape.
type rawClaims struct {
	Issuer            string       `json:"iss"`
	Subject           string       `json:"sub"`
	Audience          audience     `json:"aud"`
	AuthorizedParty   string       `json:"azp"`
	Email             string       `json:"email"`
	EmailVerified     flexibleBool `json:"email_verified"`
	Name              string       `json:"name"`
	PreferredUsername string       `json:"preferred_username"`
	Groups            []string     `json:"groups"`
	Nonce             string       `json:"nonce"`
	IssuedAt          int64        `json:"iat"`
	Expiry            int64        `json:"exp"`
	NotBefore         int64        `json:"nbf"`
}

// audience accepts either a JSON string or array of strings (RFC 7519 allows
// both for the "aud" claim).
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var single string
	if err := json.Unmarshal(b, &single); err == nil {
		*a = audience{single}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("oidc: aud claim is neither string nor []string: %w", err)
	}
	*a = many
	return nil
}

func (a audience) contains(s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}

// flexibleBool accepts a JSON bool or the strings "true"/"false", which some
// providers emit for email_verified.
type flexibleBool bool

func (f *flexibleBool) UnmarshalJSON(b []byte) error {
	var real bool
	if err := json.Unmarshal(b, &real); err == nil {
		*f = flexibleBool(real)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexibleBool(s == "true")
		return nil
	}
	return fmt.Errorf("oidc: email_verified is neither bool nor string")
}

// VerifyIDToken verifies the signature and all registered claims of an OIDC ID
// token and returns the validated claims. The order of checks is deliberate:
// the signature is verified against a key resolved by the protected-header kid
// before any payload value is trusted.
//
// expectedNonce is the nonce that was sent in the authentication request and
// stored server-side alongside the state/PKCE verifier; it is required when
// cfg.RequireNonce is true.
func VerifyIDToken(ctx context.Context, ks KeySource, cfg Config, rawIDToken, expectedNonce string) (*Claims, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	algs := cfg.allowedSignatureAlgorithms()
	if len(algs) == 0 {
		return nil, ErrConfigInvalid
	}

	// Parsing with an explicit algorithm allowlist is the structural defense:
	// go-jose v4 rejects any token whose alg is not in this list, so "none"
	// and HS* never reach signature verification.
	jws, err := jose.ParseSigned(rawIDToken, algs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedAlg, err)
	}
	switch len(jws.Signatures) {
	case 0:
		return nil, ErrNoSignatures
	case 1:
		// expected
	default:
		// Multiple signatures on an ID token are not part of the OIDC code
		// flow; reject rather than guess which one is authoritative.
		return nil, ErrMultipleSignatures
	}

	// Use the protected (signed) header for kid selection; the merged Header
	// may include unprotected values and must not be trusted for key choice.
	kid := jws.Signatures[0].Protected.KeyID
	key, err := ks.KeyForKID(ctx, kid)
	if err != nil {
		return nil, err
	}

	payload, err := jws.Verify(key.Key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSignature, err)
	}

	var rc rawClaims
	if err := json.Unmarshal(payload, &rc); err != nil {
		return nil, fmt.Errorf("oidc: id_token claims decode: %w", err)
	}

	if err := validateClaims(rc, cfg, expectedNonce, time.Now()); err != nil {
		return nil, err
	}
	// Identity providers name the claim that lists a user's groups
	// differently ("groups", "roles", a URL); it is a setting.
	if gc := cfg.GroupClaim; gc != "" && gc != "groups" {
		rc.Groups = groupsFromClaim(payload, gc)
	}

	return &Claims{
		Issuer:            rc.Issuer,
		Subject:           rc.Subject,
		Audience:          []string(rc.Audience),
		AuthorizedParty:   rc.AuthorizedParty,
		Email:             rc.Email,
		EmailVerified:     bool(rc.EmailVerified),
		Name:              rc.Name,
		PreferredUsername: rc.PreferredUsername,
		Groups:            rc.Groups,
		Nonce:             rc.Nonce,
		IssuedAt:          rc.IssuedAt,
		Expiry:            rc.Expiry,
		NotBefore:         rc.NotBefore,
	}, nil
}

// validateClaims enforces the OIDC ID-token validation rules (OIDC Core 1.0
// section 3.1.3.7) that are not handled by signature verification. now is
// injected to keep the logic deterministically testable.
func validateClaims(rc rawClaims, cfg Config, expectedNonce string, now time.Time) error {
	// iss MUST exactly equal the configured issuer.
	if rc.Issuer != cfg.Issuer {
		return ErrIssuerMismatch
	}

	// sub MUST be present; it is the stable identity key used for account
	// linking. Email is mutable and must never be used for this.
	if rc.Subject == "" {
		return ErrMissingSubject
	}

	// aud MUST contain the client_id.
	if !rc.Audience.contains(cfg.ClientID) {
		return ErrAudienceMismatch
	}
	// If aud has more than one value, or azp is present, azp MUST equal the
	// client_id (OIDC Core 3.1.3.7 rules 4 and 5).
	if len(rc.Audience) > 1 || rc.AuthorizedParty != "" {
		if rc.AuthorizedParty != cfg.ClientID {
			return ErrAzpMismatch
		}
	}

	skew := cfg.skew()

	// exp MUST be in the future (allowing skew).
	if rc.Expiry <= 0 || now.Add(-skew).After(time.Unix(rc.Expiry, 0)) {
		return ErrExpired
	}

	// iat MUST NOT be unreasonably in the future (allowing skew).
	if rc.IssuedAt > 0 && time.Unix(rc.IssuedAt, 0).After(now.Add(skew)) {
		return ErrIssuedInFuture
	}

	// Optional maximum token age bound.
	if cfg.MaxTokenAge > 0 && rc.IssuedAt > 0 {
		if now.Sub(time.Unix(rc.IssuedAt, 0)) > cfg.MaxTokenAge+skew {
			return ErrTokenTooOld
		}
	}

	// nbf, when present, MUST NOT be in the future (allowing skew).
	if rc.NotBefore > 0 && time.Unix(rc.NotBefore, 0).After(now.Add(skew)) {
		return ErrNotYetValid
	}

	// nonce binds the token to this login attempt and defends against replay.
	if cfg.RequireNonce {
		if rc.Nonce == "" {
			return ErrNonceMissing
		}
		if !subtleStringEqual(rc.Nonce, expectedNonce) {
			return ErrNonceMismatch
		}
	}

	return nil
}

// groupsFromClaim reads a list of group names from the named claim of a
// verified token: an array of strings, or one string.
func groupsFromClaim(payload []byte, claim string) []string {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(payload, &all); err != nil {
		return nil
	}
	raw, ok := all[claim]
	if !ok {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil && one != "" {
		return []string{one}
	}
	return nil
}
