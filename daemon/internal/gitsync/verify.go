package gitsync

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Commit signatures (ADR-0007): with verification on, every commit a node
// takes from the repository must be signed by an allowed key, so write
// access to the repository alone cannot change the configuration.
//
// Allowed keys are SSH public keys in allowed-signers format ("email
// namespaces="git" ssh-ed25519 AAAA…", or just "* ssh-ed25519 AAAA…") and
// armored GPG public keys. git verify-commit picks the method from the
// signature.

// Signers are the keys allowed to sign commits.
type Signers struct {
	SSH string // allowed-signers file content
	GPG string // armored public keys
}

// Empty reports whether no key is configured.
func (s Signers) Empty() bool {
	return strings.TrimSpace(s.SSH) == "" && strings.TrimSpace(s.GPG) == ""
}

// ErrUnsigned: a commit is not signed by an allowed key.
var ErrUnsigned = errors.New("not signed by an allowed key")

// VerifyCommits checks every commit in shas. It returns the first commit
// that fails and why.
func (c *Clone) VerifyCommits(shas []string, s Signers) (string, error) {
	if len(shas) == 0 {
		return "", nil
	}
	if s.Empty() {
		return shas[0], fmt.Errorf("signature verification is on, but no allowed key is configured")
	}
	dir := filepath.Join(c.Dir, ".git", "dplaneos-verify")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	allowed := filepath.Join(dir, "allowed_signers")
	if err := os.WriteFile(allowed, []byte(normalizeAllowedSigners(s.SSH)), 0o600); err != nil {
		return "", err
	}
	gnupg := filepath.Join(dir, "gnupg")
	if err := os.MkdirAll(gnupg, 0o700); err != nil {
		return "", err
	}
	if strings.TrimSpace(s.GPG) != "" {
		cmd := exec.Command("gpg", "--batch", "--quiet", "--import")
		cmd.Env = append(os.Environ(), "GNUPGHOME="+gnupg)
		cmd.Stdin = strings.NewReader(s.GPG)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("importing the GPG keys: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	for _, sha := range shas {
		if !shaRe.MatchString(sha) {
			return sha, fmt.Errorf("invalid commit %q", sha)
		}
		if _, err := c.git([]string{"GNUPGHOME=" + gnupg}, nil, "-c", "gpg.ssh.allowedSignersFile="+allowed, "verify-commit", sha); err != nil {
			return sha, fmt.Errorf("commit %.12s (%s): %w", sha, c.Subject(sha), ErrUnsigned)
		}
	}
	return "", nil
}

// normalizeAllowedSigners accepts bare public keys ("ssh-ed25519 AAAA…
// comment") and gives them the principal "*", as git requires one.
func normalizeAllowedSigners(in string) string {
	var b strings.Builder
	for _, line := range strings.Split(in, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "ssh-") || strings.HasPrefix(line, "ecdsa-") || strings.HasPrefix(line, "sk-") {
			line = "* " + line
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// ── Webhooks ──────────────────────────────────────────────────────────────────

// VerifyWebhook authenticates a push notification from GitHub, Gitea or
// Forgejo (HMAC-SHA256 of the body) or GitLab (token header) with the
// shared secret. It returns the provider that signed it.
func VerifyWebhook(h http.Header, body []byte, secret string) (string, bool) {
	if secret == "" {
		return "", false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)
	if sig := h.Get("X-Hub-Signature-256"); strings.HasPrefix(sig, "sha256=") {
		got, err := hex.DecodeString(strings.TrimPrefix(sig, "sha256="))
		return "github", err == nil && hmac.Equal(got, want)
	}
	for _, name := range []string{"X-Gitea-Signature", "X-Forgejo-Signature", "X-Gogs-Signature"} {
		if sig := h.Get(name); sig != "" {
			got, err := hex.DecodeString(sig)
			return "gitea", err == nil && hmac.Equal(got, want)
		}
	}
	if tok := h.Get("X-Gitlab-Token"); tok != "" {
		return "gitlab", subtle.ConstantTimeCompare([]byte(tok), []byte(secret)) == 1
	}
	return "", false
}
