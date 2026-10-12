// Package fileaccounts gives DPlaneOS users the system accounts that file
// services authenticate against: a Unix account (file ownership, FTP) and a
// Samba account (SMB). The web interface's users live in the database; this
// package keeps /etc/passwd, /etc/shadow, /etc/group and Samba's password
// database in line with them.
package fileaccounts

import (
	"crypto/sha256"
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

// SHA512CryptFor returns the "$6$salt$hash" entry of a user's password with
// a salt derived from the user name and anchor, the user's password hash in
// the database (bcrypt, with a random salt of its own).
//
// Every node of a cluster makes this entry (at the user's first sign-in
// there), and the nodes exchange and compare a user's credentials. With a
// random salt each node arrived at a different entry for the same password,
// and the user counted as "changed on both nodes". The anchor is exchanged
// between the nodes, so they arrive at the same salt; it is random per
// user and changes with every password, so the salt is unique as a salt
// should be. It is deliberately not derived from the password itself: a
// salt that is a fast hash of the password would let anyone who reads the
// entry test guesses with one hash instead of five thousand rounds.
func SHA512CryptFor(username, password, anchor string) string {
	sum := sha256.Sum256([]byte("dplaneos file account salt\x00" + username + "\x00" + anchor))
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = cryptAlphabet[int(sum[i])%len(cryptAlphabet)]
	}
	return sha512Crypt([]byte(password), salt)
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
