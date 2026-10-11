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

	a, err := SHA512Crypt("x")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SHA512Crypt("x")
	if a == b || !strings.HasPrefix(a, "$6$") {
		t.Errorf("salts must differ: %s %s", a, b)
	}
}
