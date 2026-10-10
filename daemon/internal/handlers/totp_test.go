package handlers

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dplaned/internal/secrets"

	"golang.org/x/crypto/bcrypt"
)

// RFC 6238 test vector (SHA-1, 6 digits): secret "12345678901234567890".
func TestComputeTOTPVector(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		got, err := computeTOTP(secret, time.Unix(ts, 0))
		if err != nil || got != want {
			t.Errorf("T=%d: %s %v, want %s", ts, got, err, want)
		}
	}
}

func TestTOTPGuard(t *testing.T) {
	g := &totpGuard{fails: map[int][]time.Time{}, lastStep: map[int]int64{}}
	now := time.Now()
	for i := 0; i < totpMaxFailures; i++ {
		if g.locked(7, now) {
			t.Fatalf("locked after %d failures", i)
		}
		g.fail(7, now)
	}
	if !g.locked(7, now) || g.locked(8, now) {
		t.Error("lock is per user")
	}
	if g.locked(7, now.Add(totpFailWindow+time.Second)) {
		t.Error("failures expire")
	}
	if !g.useStep(7, 100) || g.useStep(7, 100) || g.useStep(7, 99) || !g.useStep(7, 101) {
		t.Error("a used time step must not be accepted again")
	}
}

type totpEnv struct {
	db     *sql.DB
	h      *TOTPHandler
	user   int
	secret string
	codes  []string
}

func newTOTPEnv(t *testing.T) totpEnv {
	db := testDB(t)
	if err := secrets.Init(filepath.Join(t.TempDir(), "secrets.key")); err != nil {
		t.Fatal(err)
	}
	prev := totpAttempts
	totpAttempts = &totpGuard{fails: map[int][]time.Time{}, lastStep: map[int]int64{}}
	t.Cleanup(func() { totpAttempts = prev })

	e := totpEnv{db: db, h: NewTOTPHandler(db)}
	e.user = addUser(t, db, "tina", "Tina-Passw0rd", "user", true)
	if _, err := db.Exec(`UPDATE users SET totp_enabled = 1 WHERE id = $1`, e.user); err != nil {
		t.Fatal(err)
	}
	var err error
	if e.secret, err = generateTOTPSecret(); err != nil {
		t.Fatal(err)
	}
	sealed, err := secrets.Seal(e.secret)
	if err != nil {
		t.Fatal(err)
	}
	codes, hashes, err := generateBackupCodes()
	if err != nil {
		t.Fatal(err)
	}
	e.codes = codes
	if _, err := db.Exec(`INSERT INTO totp_secrets (user_id, secret, backup_codes, enabled) VALUES ($1, $2, $3, 1)`, e.user, sealed, hashes); err != nil {
		t.Fatal(err)
	}
	return e
}

// pending creates a pending_totp session as the login does.
func (e totpEnv) pending(t *testing.T) string {
	t.Helper()
	tok, _ := generateSessionID()
	if _, err := e.db.Exec(`INSERT INTO sessions (session_id, user_id, username, created_at, expires_at, status) VALUES ($1, $2, 'tina', $3, $4, 'pending_totp')`,
		tok, e.user, time.Now().Unix(), time.Now().Add(5*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e totpEnv) verify(t *testing.T, tok, code string) resp {
	return call(t, e.h.HandleTOTPVerify, req{method: "POST", body: map[string]any{"pending_token": tok, "code": code}})
}

func (e totpEnv) code(t *testing.T) string {
	c, err := computeTOTP(e.secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTOTPVerify(t *testing.T) {
	e := newTOTPEnv(t)
	tok := e.pending(t)
	if r := e.verify(t, "nope", e.code(t)); r.code != 401 {
		t.Errorf("unknown pending token: %s", r)
	}
	used := e.code(t)
	r := e.verify(t, tok, used)
	if !r.ok() {
		t.Fatalf("verify: %s", r)
	}
	sid := r.body["session_id"].(string)
	var status string
	var aal int
	_ = e.db.QueryRow(`SELECT status, aal FROM sessions WHERE session_id = $1`, sid).Scan(&status, &aal)
	if status != "active" || aal != 2 {
		t.Errorf("session %q aal %d", status, aal)
	}
	// The pending token is spent.
	if r := e.verify(t, tok, e.code(t)); r.ok() {
		t.Errorf("pending token reused: %s", r)
	}
	// The same code again, with a new pending login: replay refused.
	if r := e.verify(t, e.pending(t), used); r.ok() {
		t.Errorf("TOTP code replayed: %s", r)
	}
}

func TestTOTPVerifyLockout(t *testing.T) {
	e := newTOTPEnv(t)
	for i := 0; i < totpMaxFailures; i++ {
		if r := e.verify(t, e.pending(t), "000000"); r.code != 401 {
			t.Fatalf("failure %d: %s", i, r)
		}
	}
	// New pending logins do not reset the limit; the right code is refused too.
	tok := e.pending(t)
	if r := e.verify(t, tok, e.code(t)); r.code != 429 {
		t.Errorf("after %d failures: %s", totpMaxFailures, r)
	}
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE session_id = $1`, tok).Scan(&n)
	if n != 0 {
		t.Error("pending session kept after lockout")
	}
}

func TestTOTPBackupCodeSingleUse(t *testing.T) {
	e := newTOTPEnv(t)
	code := e.codes[0]
	// Concurrent use of one backup code: at most one login.
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 4; i++ {
		tok := e.pending(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := e.verify(t, tok, code); r.ok() {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("backup code accepted %d times", ok)
	}
	var stored string
	_ = e.db.QueryRow(`SELECT backup_codes FROM totp_secrets WHERE user_id = $1`, e.user).Scan(&stored)
	for _, h := range strings.Split(stored, ",") {
		if h != "" && bcrypt.CompareHashAndPassword([]byte(h), []byte(code)) == nil {
			t.Error("used backup code still stored")
		}
	}
	if r := e.verify(t, e.pending(t), e.codes[1]); !r.ok() {
		t.Errorf("another backup code: %s", r)
	}
}
