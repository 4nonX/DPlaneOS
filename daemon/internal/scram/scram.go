// Package scram implements SCRAM-SHA-512 (RFC 5802) credential derivation and
// verification for DPlaneOS local authentication.
//
// Passwords are never stored in plaintext. On password set the caller derives a
// Keys bundle and stores only {Salt, Iterations, StoredKey, ServerKey}. The raw
// SaltedPassword is discarded immediately after derivation.
//
// REST handshake (two-round, challenge/response):
//
//	POST /api/auth/scram/challenge  – client sends {username, client_nonce}
//	                                   server returns {challenge_id, server_nonce, salt_b64, iterations}
//	POST /api/auth/scram/verify     – client sends {challenge_id, client_proof_b64}
//	                                   server verifies, returns {session_token, server_proof_b64}
package scram

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

const (
	DefaultIterations = 100_000
	SaltLength        = 16
	NonceLength       = 24
	challengeTTL      = 2 * time.Minute
)

// Keys holds the precomputed SCRAM-SHA-512 credential set stored in the database.
// The raw password and SaltedPassword are never persisted.
type Keys struct {
	Salt       []byte
	Iterations int
	StoredKey  []byte // H(ClientKey)  – used to verify client proof
	ServerKey  []byte // HMAC(SaltedPassword, "Server Key") – used to produce server proof
}

// Derive computes a fresh Keys bundle from a plaintext password with a new random salt.
func Derive(password string) (*Keys, error) {
	salt := make([]byte, SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("scram: generate salt: %w", err)
	}
	return deriveWithSalt(password, salt, DefaultIterations)
}

// DeriveFor computes the keys for a password that is already stored as
// passwordHash (bcrypt), with a salt taken from that hash: nodes holding the
// same password hash (installer, GitOps, config sync) derive the same keys,
// so filling them in at a login does not make the nodes' credentials differ.
// The bcrypt hash carries its own random salt, so the SCRAM salt is still
// unique per password set.
func DeriveFor(password, passwordHash string) (*Keys, error) {
	h := sha512.Sum512([]byte("dplaneos-scram-salt\x00" + passwordHash))
	return deriveWithSalt(password, h[:SaltLength], DefaultIterations)
}

func deriveWithSalt(password string, salt []byte, iterations int) (*Keys, error) { //nolint:unparam
	if iterations <= 0 {
		iterations = DefaultIterations
	}
	saltedPassword, err := pbkdf2.Key(sha512.New, password, salt, iterations, sha512.Size)
	if err != nil {
		return nil, fmt.Errorf("scram: pbkdf2: %w", err)
	}
	clientKey := hmacSHA512(saltedPassword, []byte("Client Key"))
	storedKey := sha512Hash(clientKey)
	serverKey := hmacSHA512(saltedPassword, []byte("Server Key"))
	return &Keys{
		Salt:       salt,
		Iterations: iterations,
		StoredKey:  storedKey,
		ServerKey:  serverKey,
	}, nil
}

// Verify checks whether clientProof is valid for the given storedKey and authMessage.
// Returns the ServerProof on success so the client can confirm server identity.
func Verify(storedKey, serverKey, clientProof, authMessage []byte) (serverProof []byte, ok bool) {
	clientSignature := hmacSHA512(storedKey, authMessage)
	if len(clientProof) != len(clientSignature) {
		return nil, false
	}
	recoveredClientKey := xorBytes(clientProof, clientSignature)
	recoveredStoredKey := sha512Hash(recoveredClientKey)
	if !hmac.Equal(recoveredStoredKey, storedKey) {
		return nil, false
	}
	return hmacSHA512(serverKey, authMessage), true
}

// EncodeBase64 encodes bytes to standard base64.
func EncodeBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// DecodeBase64 decodes standard base64.
func DecodeBase64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// RandomNonce generates a cryptographically random base64 nonce string.
func RandomNonce() (string, error) {
	b := make([]byte, NonceLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthMessage assembles the SCRAM auth-message from its three parts per RFC 5802.
//
//	client-first-message-bare  = "n=<user>,r=<clientNonce>"
//	server-first-message       = "r=<clientNonce+serverNonce>,s=<saltB64>,i=<iterations>"
//	client-final-without-proof = "c=biws,r=<clientNonce+serverNonce>"
func AuthMessage(username, clientNonce, serverNonce, saltB64 string, iterations int) []byte {
	combined := clientNonce + serverNonce
	cfmb := "n=" + username + ",r=" + clientNonce
	sfm := fmt.Sprintf("r=%s,s=%s,i=%d", combined, saltB64, iterations)
	cfwp := "c=biws,r=" + combined
	return []byte(cfmb + "," + sfm + "," + cfwp)
}

// ─── In-memory challenge store ───────────────────────────────────────────────

// Challenge holds the server side of one SCRAM handshake in progress.
type Challenge struct {
	Username    string
	ClientNonce string
	ServerNonce string
	StoredKey   []byte
	ServerKey   []byte
	SaltB64     string
	Iterations  int
	ExpiresAt   time.Time
}

type challengeStore struct {
	mu         sync.Mutex
	challenges map[string]*Challenge
}

var store = &challengeStore{
	challenges: make(map[string]*Challenge),
}

// NewChallenge stores a pending SCRAM challenge and returns its ID.
func NewChallenge(c *Challenge) string {
	id := make([]byte, 16)
	rand.Read(id) //nolint:errcheck
	cid := base64.RawURLEncoding.EncodeToString(id)
	c.ExpiresAt = time.Now().Add(challengeTTL)
	store.mu.Lock()
	store.challenges[cid] = c
	store.mu.Unlock()
	return cid
}

// TakeChallenge retrieves and removes a challenge by ID. Returns nil if expired or absent.
func TakeChallenge(id string) *Challenge {
	store.mu.Lock()
	defer store.mu.Unlock()
	c, ok := store.challenges[id]
	if !ok {
		return nil
	}
	delete(store.challenges, id)
	if time.Now().After(c.ExpiresAt) {
		return nil
	}
	return c
}

// PruneExpired removes stale challenges. Call periodically.
func PruneExpired() {
	store.mu.Lock()
	defer store.mu.Unlock()
	now := time.Now()
	for id, c := range store.challenges {
		if now.After(c.ExpiresAt) {
			delete(store.challenges, id)
		}
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func hmacSHA512(key, msg []byte) []byte {
	mac := hmac.New(sha512.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

func sha512Hash(data []byte) []byte {
	h := sha512.Sum512(data)
	return h[:]
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}
