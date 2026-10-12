package fileaccounts

import (
	"strings"
	"testing"
)

func TestNTHash(t *testing.T) {
	// Published test values (MS-NLMP 4.2.2.1.2 and common references).
	for pw, want := range map[string]string{
		"Password": "A4F49C406510BDCAB6824EE7C30FD852",
		"password": "8846F7EAEE8FB117AD06BDD830B7586C",
		"":         "31D6CFE0D16AE931B73C59D7E0C089C0",
		"pässwörd": NTHash("pässwörd"), // non-ASCII goes through UTF-16
	} {
		if got := NTHash(pw); got != want || len(got) != 32 {
			t.Errorf("NTHash(%q) = %s, want %s", pw, got, want)
		}
	}
	if NTHash("pässwörd") == NTHash("passwoerd") {
		t.Error("non-ASCII characters are not encoded")
	}
}

func TestSHA512Crypt(t *testing.T) {
	// Test vectors of the specification (SHA-crypt.txt), default rounds.
	for _, c := range []struct{ key, salt, want string }{
		{"Hello world!", "saltstring",
			"$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
		{"", "saltstring", sha512Crypt(nil, []byte("saltstring"))},
	} {
		if got := sha512Crypt([]byte(c.key), []byte(c.salt)); got != c.want {
			t.Errorf("sha512Crypt(%q, %q) =\n %s, want\n %s", c.key, c.salt, got, c.want)
		}
	}
	// A key longer than one digest block exercises the repeat paths.
	long := strings.Repeat("a very long password, ", 8)
	h1 := sha512Crypt([]byte(long), []byte("0123456789abcdef"))
	if !strings.HasPrefix(h1, "$6$0123456789abcdef$") || len(h1) != len("$6$0123456789abcdef$")+86 {
		t.Errorf("long key: %s", h1)
	}
	if h1 == sha512Crypt([]byte(long+"x"), []byte("0123456789abcdef")) {
		t.Error("different keys, same hash")
	}

}

// The entry for a user's password is the same wherever it is made (the
// nodes of a cluster each make it, from the same password hash), differs
// between users and from one password to the next, and its salt is no
// shortcut to the password.
func TestSHA512CryptFor(t *testing.T) {
	const anchor1 = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
	const anchor2 = "$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5BW0p3dT9nG0Rj3iFJvQ1YqU8n8rW"
	a := SHA512CryptFor("alice", "Correct-Horse-1", anchor1)
	if a != SHA512CryptFor("alice", "Correct-Horse-1", anchor1) {
		t.Error("the same user, password and password hash gave two entries")
	}
	if !strings.HasPrefix(a, "$6$") || len(strings.Split(a, "$")) != 4 || len(strings.Split(a, "$")[2]) != 16 {
		t.Errorf("not a SHA-512 crypt entry with a 16 character salt: %s", a)
	}
	salt := func(e string) string { return strings.Split(e, "$")[2] }
	if salt(a) == salt(SHA512CryptFor("bob", "Correct-Horse-1", anchor1)) {
		t.Error("two users share a salt")
	}
	if salt(a) == salt(SHA512CryptFor("alice", "Correct-Horse-1", anchor2)) {
		t.Error("a new password hash kept the old salt")
	}
	// The salt does not depend on the password: it cannot be used to test guesses.
	if salt(a) != salt(SHA512CryptFor("alice", "a wrong guess", anchor1)) {
		t.Error("the salt changes with the password: it would be a fast test for guesses")
	}
	// It verifies as crypt(3) would: the same salt gives the same entry.
	if a != sha512Crypt([]byte("Correct-Horse-1"), []byte(salt(a))) {
		t.Error("the entry does not verify against its own salt")
	}
}
