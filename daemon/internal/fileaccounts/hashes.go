// Package fileaccounts gives DPlaneOS users the system accounts that file
// services authenticate against: a Unix account (file ownership, FTP) and a
// Samba account (SMB). The web interface's users live in the database; this
// package keeps /etc/passwd, /etc/shadow, /etc/group and Samba's password
// database in line with them.
package fileaccounts

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"strings"
	"unicode/utf16"

	"golang.org/x/crypto/md4" //nolint:staticcheck // the NT hash is MD4 by definition
)

// NTHash is the hash SMB authenticates with: MD4 of the password in
// UTF-16LE, as 32 upper-case hex digits (the form pdbedit takes).
func NTHash(password string) string {
	u := utf16.Encode([]rune(password))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	h := md4.New()
	h.Write(b)
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
}

const cryptAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// SHA512Crypt returns a "$6$salt$hash" entry for /etc/shadow (the SHA-512
// scheme of crypt(3), 5000 rounds) with a random salt.
func SHA512Crypt(password string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	for i, b := range raw {
		salt[i] = cryptAlphabet[int(b)%len(cryptAlphabet)]
	}
	return sha512Crypt([]byte(password), salt), nil
}

// sha512Crypt implements the algorithm of
// https://www.akkadia.org/drepper/SHA-crypt.txt with the default 5000 rounds.
func sha512Crypt(key, salt []byte) string {
	const rounds = 5000
	if len(salt) > 16 {
		salt = salt[:16]
	}

	// Digest B: key, salt, key.
	b := sha512.New()
	b.Write(key)
	b.Write(salt)
	b.Write(key)
	digestB := b.Sum(nil)

	// Digest A.
	a := sha512.New()
	a.Write(key)
	a.Write(salt)
	for n := len(key); n > 0; n -= 64 {
		if n > 64 {
			a.Write(digestB)
		} else {
			a.Write(digestB[:n])
		}
	}
	for n := len(key); n > 0; n >>= 1 {
		if n&1 != 0 {
			a.Write(digestB)
		} else {
			a.Write(key)
		}
	}
	digestA := a.Sum(nil)

	// Sequence P: from the digest of the key repeated len(key) times.
	dp := sha512.New()
	for range key {
		dp.Write(key)
	}
	p := repeatTo(dp.Sum(nil), len(key))

	// Sequence S: from the digest of the salt repeated 16+A[0] times.
	ds := sha512.New()
	for i := 0; i < 16+int(digestA[0]); i++ {
		ds.Write(salt)
	}
	s := repeatTo(ds.Sum(nil), len(salt))

	c := digestA
	for i := 0; i < rounds; i++ {
		h := sha512.New()
		if i&1 != 0 {
			h.Write(p)
		} else {
			h.Write(c)
		}
		if i%3 != 0 {
			h.Write(s)
		}
		if i%7 != 0 {
			h.Write(p)
		}
		if i&1 != 0 {
			h.Write(c)
		} else {
			h.Write(p)
		}
		c = h.Sum(nil)
	}

	var out strings.Builder
	out.WriteString("$6$")
	out.Write(salt)
	out.WriteByte('$')
	b64 := func(b2, b1, b0 byte, n int) {
		w := uint(b2)<<16 | uint(b1)<<8 | uint(b0)
		for ; n > 0; n-- {
			out.WriteByte(cryptAlphabet[w&0x3f])
			w >>= 6
		}
	}
	for _, t := range [][3]int{
		{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48},
		{28, 49, 7}, {50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13},
		{56, 14, 35}, {15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41},
	} {
		b64(c[t[0]], c[t[1]], c[t[2]], 4)
	}
	b64(0, 0, c[63], 2)
	return out.String()
}

func repeatTo(digest []byte, n int) []byte {
	out := make([]byte, 0, n)
	for len(out)+len(digest) <= n {
		out = append(out, digest...)
	}
	return append(out, digest[:n-len(out)]...)
}
