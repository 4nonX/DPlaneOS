package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// staticKeySource is an in-memory KeySource for tests, so verification logic is
// exercised without any network access.
type staticKeySource struct {
	keys map[string]*jose.JSONWebKey
}

func (s staticKeySource) KeyForKID(_ context.Context, kid string) (*jose.JSONWebKey, error) {
	if k, ok := s.keys[kid]; ok {
		return k, nil
	}
	if kid == "" && len(s.keys) == 1 {
		for _, k := range s.keys {
			return k, nil
		}
	}
	return nil, ErrKeyNotFound
}

// testSigner builds an RSA key, a matching KeySource keyed by kid, and a signer.
type testSigner struct {
	priv   *rsa.PrivateKey
	kid    string
	source staticKeySource
}

func newTestSigner(t *testing.T, kid string) testSigner {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubJWK := jose.JSONWebKey{Key: &priv.PublicKey, KeyID: kid, Algorithm: string(jose.RS256), Use: "sig"}
	src := staticKeySource{keys: map[string]*jose.JSONWebKey{kid: &pubJWK}}
	return testSigner{priv: priv, kid: kid, source: src}
}

// sign serializes the given claims into a compact JWS signed with the test key.
func (ts testSigner) sign(t *testing.T, alg jose.SignatureAlgorithm, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: alg, Key: ts.priv},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), ts.kid),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	compact, err := obj.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return compact
}

func baseConfig() Config {
	return Config{
		Issuer:       "https://idp.example.com",
		ClientID:     "dplaneos",
		AllowedAlgs:  []string{"RS256", "ES256"},
		ClockSkew:    2 * time.Minute,
		RequireNonce: true,
	}
}

func baseClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss":   "https://idp.example.com",
		"sub":   "abc-123",
		"aud":   "dplaneos",
		"exp":   now.Add(5 * time.Minute).Unix(),
		"iat":   now.Unix(),
		"nonce": "the-nonce",
		"email": "dan@example.com",
	}
}

func TestVerifyIDToken_Valid(t *testing.T) {
	ts := newTestSigner(t, "key-1")
	now := time.Now()
	token := ts.sign(t, jose.RS256, baseClaims(now))

	claims, err := VerifyIDToken(context.Background(), ts.source, baseConfig(), token, "the-nonce")
	if err != nil {
		t.Fatalf("expected valid token, got error: %v", err)
	}
	if claims.Subject != "abc-123" {
		t.Errorf("subject = %q, want abc-123", claims.Subject)
	}
	if claims.Email != "dan@example.com" {
		t.Errorf("email = %q", claims.Email)
	}
}

func TestVerifyIDToken_Rejections(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name    string
		mutate  func(c map[string]any)
		nonce   string
		cfg     func(Config) Config
		wantErr error
	}{
		{
			name:    "issuer mismatch",
			mutate:  func(c map[string]any) { c["iss"] = "https://evil.example.com" },
			nonce:   "the-nonce",
			wantErr: ErrIssuerMismatch,
		},
		{
			name:    "audience mismatch",
			mutate:  func(c map[string]any) { c["aud"] = "someone-else" },
			nonce:   "the-nonce",
			wantErr: ErrAudienceMismatch,
		},
		{
			name: "azp mismatch with multiple aud",
			mutate: func(c map[string]any) {
				c["aud"] = []string{"dplaneos", "other"}
				c["azp"] = "other"
			},
			nonce:   "the-nonce",
			wantErr: ErrAzpMismatch,
		},
		{
			name:    "expired",
			mutate:  func(c map[string]any) { c["exp"] = now.Add(-10 * time.Minute).Unix() },
			nonce:   "the-nonce",
			wantErr: ErrExpired,
		},
		{
			name:    "issued in future",
			mutate:  func(c map[string]any) { c["iat"] = now.Add(10 * time.Minute).Unix() },
			nonce:   "the-nonce",
			wantErr: ErrIssuedInFuture,
		},
		{
			name:    "missing subject",
			mutate:  func(c map[string]any) { delete(c, "sub") },
			nonce:   "the-nonce",
			wantErr: ErrMissingSubject,
		},
		{
			name:    "nonce mismatch",
			mutate:  func(c map[string]any) {},
			nonce:   "wrong-nonce",
			wantErr: ErrNonceMismatch,
		},
		{
			name:    "nonce missing",
			mutate:  func(c map[string]any) { delete(c, "nonce") },
			nonce:   "the-nonce",
			wantErr: ErrNonceMissing,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestSigner(t, "key-1")
			claims := baseClaims(now)
			tc.mutate(claims)
			token := ts.sign(t, jose.RS256, claims)

			cfg := baseConfig()
			if tc.cfg != nil {
				cfg = tc.cfg(cfg)
			}
			_, err := VerifyIDToken(context.Background(), ts.source, cfg, token, tc.nonce)
			if err == nil {
				t.Fatalf("expected error %v, got nil", tc.wantErr)
			}
			if tc.wantErr != nil && !errorsIs(err, tc.wantErr) {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestVerifyIDToken_WrongKeyFailsSignature(t *testing.T) {
	signer := newTestSigner(t, "key-1")
	// Verifier is given a DIFFERENT key under the same kid.
	otherPriv, _ := rsa.GenerateKey(rand.Reader, 2048)
	otherJWK := jose.JSONWebKey{Key: &otherPriv.PublicKey, KeyID: "key-1", Algorithm: string(jose.RS256), Use: "sig"}
	source := staticKeySource{keys: map[string]*jose.JSONWebKey{"key-1": &otherJWK}}

	token := signer.sign(t, jose.RS256, baseClaims(time.Now()))
	_, err := VerifyIDToken(context.Background(), source, baseConfig(), token, "the-nonce")
	if !errorsIs(err, ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature, got %v", err)
	}
}

func TestVerifyIDToken_UnknownKID(t *testing.T) {
	signer := newTestSigner(t, "key-1")
	source := staticKeySource{keys: map[string]*jose.JSONWebKey{}}
	token := signer.sign(t, jose.RS256, baseClaims(time.Now()))
	_, err := VerifyIDToken(context.Background(), source, baseConfig(), token, "the-nonce")
	if !errorsIs(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestVerifyIDToken_DisallowedAlgRejected(t *testing.T) {
	signer := newTestSigner(t, "key-1")
	token := signer.sign(t, jose.RS256, baseClaims(time.Now()))
	cfg := baseConfig()
	cfg.AllowedAlgs = []string{"ES256"} // RS256 not allowed
	_, err := VerifyIDToken(context.Background(), signer.source, cfg, token, "the-nonce")
	if !errorsIs(err, ErrUnsupportedAlg) {
		t.Fatalf("expected ErrUnsupportedAlg, got %v", err)
	}
}

func TestConfig_AllowedAlgsDropsUnsafe(t *testing.T) {
	cfg := Config{AllowedAlgs: []string{"none", "HS256", "RS256", "garbage", "ES256"}}
	algs := cfg.allowedSignatureAlgorithms()
	if len(algs) != 2 {
		t.Fatalf("expected only RS256+ES256 to survive, got %v", algs)
	}
	for _, a := range algs {
		if a != jose.RS256 && a != jose.ES256 {
			t.Fatalf("unsafe alg leaked into allowlist: %v", a)
		}
	}
}

// errorsIs is a tiny local wrapper so the test file does not depend on import
// ordering churn; it mirrors errors.Is semantics for wrapped errors.
func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestGroupsFromPayload(t *testing.T) {
	cases := []struct {
		payload, claim string
		want           []string
		present        bool
	}{
		{`{"groups":["admin","x"]}`, "", []string{"admin", "x"}, true},
		{`{"roles":"admin operator"}`, "roles", []string{"admin", "operator"}, true},
		{`{"groups":"admin,viewer"}`, "groups", []string{"admin", "viewer"}, true},
		{`{"sub":"x"}`, "groups", nil, false},
		{`{"groups":{"weird":1}}`, "groups", nil, true},
	}
	for _, c := range cases {
		got, present := groupsFromPayload([]byte(c.payload), c.claim)
		if present != c.present || strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s / %q: got %v %v", c.payload, c.claim, got, present)
		}
	}
}
