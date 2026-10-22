package scram

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/sha512"
	"testing"
)

func TestDerive_ProducesNonEmptyKeys(t *testing.T) {
	keys, err := Derive("correcthorsebatterystaple")
	if err != nil {
		t.Fatalf("Derive failed: %v", err)
	}
	if len(keys.Salt) != SaltLength {
		t.Errorf("salt length = %d, want %d", len(keys.Salt), SaltLength)
	}
	if keys.Iterations != DefaultIterations {
		t.Errorf("iterations = %d, want %d", keys.Iterations, DefaultIterations)
	}
	if len(keys.StoredKey) != 64 {
		t.Errorf("stored key length = %d, want 64 (SHA-512)", len(keys.StoredKey))
	}
	if len(keys.ServerKey) != 64 {
		t.Errorf("server key length = %d, want 64 (SHA-512)", len(keys.ServerKey))
	}
}

func TestDerive_DifferentSaltsEachCall(t *testing.T) {
	k1, err := Derive("password")
	if err != nil {
		t.Fatal(err)
	}
	k2, err := Derive("password")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(k1.Salt, k2.Salt) {
		t.Error("two Derive calls produced identical salts - RNG is broken")
	}
	if bytes.Equal(k1.StoredKey, k2.StoredKey) {
		t.Error("different salts should produce different StoredKeys")
	}
}

func TestVerify_CorrectProof(t *testing.T) {
	password := "s3cr3tP@ssw0rd"
	username := "alice"
	clientNonce := "rOprNGfwEbeRWgbNEkqO"
	serverNonce := "rOprNGfwEbeRWgbNEkqO3HnD"

	keys, err := Derive(password)
	if err != nil {
		t.Fatal(err)
	}

	saltB64 := EncodeBase64(keys.Salt)
	authMsg := AuthMessage(username, clientNonce, serverNonce, saltB64, keys.Iterations)

	// Simulate client-side proof computation (mirrors what a SCRAM client would do)
	saltedPassword, err := saltedPassword(password, keys.Salt, keys.Iterations)
	if err != nil {
		t.Fatal(err)
	}
	clientKey := hmacSHA512(saltedPassword, []byte("Client Key"))
	clientSignature := hmacSHA512(sha512Hash(clientKey), authMsg)
	clientProof := xorBytes(clientKey, clientSignature)

	serverProof, ok := Verify(keys.StoredKey, keys.ServerKey, clientProof, authMsg)
	if !ok {
		t.Fatal("Verify returned false for a correct client proof")
	}
	if len(serverProof) != 64 {
		t.Errorf("server proof length = %d, want 64", len(serverProof))
	}
}

func TestVerify_WrongProof(t *testing.T) {
	keys, err := Derive("correctpassword")
	if err != nil {
		t.Fatal(err)
	}
	saltB64 := EncodeBase64(keys.Salt)
	authMsg := AuthMessage("bob", "nonce1", "nonce2", saltB64, keys.Iterations)

	// Garbage proof
	badProof := make([]byte, 64)
	_, ok := Verify(keys.StoredKey, keys.ServerKey, badProof, authMsg)
	if ok {
		t.Error("Verify accepted an invalid proof")
	}
}

func TestVerify_WrongPassword(t *testing.T) {
	keys, _ := Derive("correctpassword")
	saltB64 := EncodeBase64(keys.Salt)
	authMsg := AuthMessage("carol", "c1", "s1", saltB64, keys.Iterations)

	// Build proof from wrong password using the same salt
	wrongSaltedPwd, _ := saltedPassword("wrongpassword", keys.Salt, keys.Iterations)
	wrongClientKey := hmacSHA512(wrongSaltedPwd, []byte("Client Key"))
	wrongSig := hmacSHA512(sha512Hash(wrongClientKey), authMsg)
	wrongProof := xorBytes(wrongClientKey, wrongSig)

	_, ok := Verify(keys.StoredKey, keys.ServerKey, wrongProof, authMsg)
	if ok {
		t.Error("Verify accepted a proof derived from the wrong password")
	}
}

func TestBase64RoundTrip(t *testing.T) {
	original := []byte("hello world bytes")
	encoded := EncodeBase64(original)
	decoded, err := DecodeBase64(encoded)
	if err != nil {
		t.Fatalf("DecodeBase64 failed: %v", err)
	}
	if !bytes.Equal(original, decoded) {
		t.Errorf("round-trip mismatch: got %q, want %q", decoded, original)
	}
}

func TestRandomNonce_Unique(t *testing.T) {
	n1, err := RandomNonce()
	if err != nil {
		t.Fatal(err)
	}
	n2, err := RandomNonce()
	if err != nil {
		t.Fatal(err)
	}
	if n1 == n2 {
		t.Error("two RandomNonce calls returned identical values")
	}
	if len(n1) < 20 {
		t.Errorf("nonce too short: %q", n1)
	}
}

func TestChallengeStore_RoundTrip(t *testing.T) {
	ch := &Challenge{
		Username:    "dave",
		ClientNonce: "abc",
		ServerNonce: "xyz",
		StoredKey:   []byte("stored"),
		ServerKey:   []byte("server"),
		SaltB64:     "c2FsdA==",
		Iterations:  4096,
	}

	id := NewChallenge(ch)
	if id == "" {
		t.Fatal("NewChallenge returned empty ID")
	}

	got := TakeChallenge(id)
	if got == nil {
		t.Fatal("TakeChallenge returned nil for a valid ID")
	}
	if got.Username != "dave" {
		t.Errorf("username mismatch: got %q", got.Username)
	}

	// Second take should return nil (consumed)
	if TakeChallenge(id) != nil {
		t.Error("TakeChallenge should return nil on second call (consumed)")
	}
}

func TestChallengeStore_MissingID(t *testing.T) {
	if TakeChallenge("no-such-id") != nil {
		t.Error("TakeChallenge should return nil for unknown ID")
	}
}

// saltedPassword recomputes the PBKDF2 output directly for test-side client simulation.
func saltedPassword(password string, salt []byte, iterations int) ([]byte, error) {
	return pbkdf2.Key(sha512.New, password, salt, iterations, sha512.Size)
}

// Nodes holding the same password hash fill in the same keys; another hash
// of the same password (set again) gives another salt.
func TestDeriveForIsStablePerPasswordHash(t *testing.T) {
	a, err := DeriveFor("Pass-1!", "$2a$10$abcdefghijklmnopqrstuv")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := DeriveFor("Pass-1!", "$2a$10$abcdefghijklmnopqrstuv")
	c, _ := DeriveFor("Pass-1!", "$2a$10$zyxwvutsrqponmlkjihgfe")
	if !bytes.Equal(a.Salt, b.Salt) || !bytes.Equal(a.StoredKey, b.StoredKey) || !bytes.Equal(a.ServerKey, b.ServerKey) {
		t.Fatal("same password hash, different keys")
	}
	if bytes.Equal(a.Salt, c.Salt) || len(a.Salt) != SaltLength || a.Iterations != DefaultIterations {
		t.Fatalf("salt %x / %x, iterations %d", a.Salt, c.Salt, a.Iterations)
	}
	// The keys are ordinary SCRAM keys for the password.
	d, _ := deriveWithSalt("Pass-1!", a.Salt, a.Iterations)
	if !bytes.Equal(d.StoredKey, a.StoredKey) {
		t.Fatal("keys do not match the password")
	}
}
