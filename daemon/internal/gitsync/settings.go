package gitsync

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"dplaned/internal/secrets"
)

// Policies (ADR-0007).
const (
	PolicyDirect      = "direct"       // commit to the branch
	PolicyPullRequest = "pull_request" // commit to dplaneos/<scope> and open a pull request
)

// RemoteSetting is one remote of the repository.
type RemoteSetting struct {
	URL       string `json:"url"`
	FetchCred string `json:"fetch_cred"` // credential name ("" = none)
	PushCred  string `json:"push_cred"`
}

// Settings of the repository (one per deployment).
type Settings struct {
	Enabled          bool            `json:"enabled"`
	Remotes          []RemoteSetting `json:"remotes"`
	Branch           string          `json:"branch"`
	Layout           string          `json:"layout"`
	StatePath        string          `json:"state_path"` // single layout
	Categories       map[string]bool `json:"categories"`
	Policy           string          `json:"policy"`
	Provider         string          `json:"provider"`
	ProviderAPIURL   string          `json:"provider_api_url"`
	ProviderRepo     string          `json:"provider_repo"`
	ProviderCred     string          `json:"provider_cred"`
	AutoApply        bool            `json:"auto_apply"`
	Prune            bool            `json:"prune"`
	SelfHeal         bool            `json:"self_heal"`
	VerifySignatures bool            `json:"verify_signatures"`
	AllowedSigners   string          `json:"allowed_signers"`
	GPGKeys          string          `json:"gpg_keys"`
	SignCommits      bool            `json:"sign_commits"`
	IntervalSecs     int             `json:"interval_secs"`
	WebhookSecret    string          `json:"-"`
	Version          int64           `json:"version"`
	Node             string          `json:"node"` // node that saved this version
}

var categoryColumns = []struct{ cat, col string }{
	{CatStorage, "sync_storage"}, {CatAccess, "sync_access"}, {CatApps, "sync_app"},
	{CatIdentity, "sync_identity"}, {CatProtect, "sync_protection"}, {CatSystem, "sync_system"},
}

// Load reads the settings.
func Load(db *sql.DB) (*Settings, error) {
	s := &Settings{Categories: map[string]bool{}}
	var enabled int
	var sealed string
	cats := make([]int, len(categoryColumns))
	dest := []any{&enabled, &s.Branch, &s.Layout, &s.StatePath, &s.Policy, &s.Provider, &s.ProviderAPIURL, &s.ProviderRepo,
		&s.ProviderCred, &s.AutoApply, &s.Prune, &s.SelfHeal, &s.VerifySignatures, &s.AllowedSigners, &s.GPGKeys,
		&s.SignCommits, &s.IntervalSecs, &sealed, &s.Version, &s.Node}
	cols := `enabled, branch, layout, state_path, policy, provider, provider_api_url, provider_repo, provider_cred,
		auto_apply, prune, self_heal, verify_signatures, allowed_signers, gpg_keys, sign_commits, interval_secs,
		webhook_secret, settings_version, settings_node`
	for i, c := range categoryColumns {
		cols += ", " + c.col
		dest = append(dest, &cats[i])
	}
	if err := db.QueryRow(`SELECT ` + cols + ` FROM gitops_config WHERE id = 1`).Scan(dest...); err != nil {
		return nil, fmt.Errorf("reading the Git settings: %w", err)
	}
	s.Enabled = enabled == 1
	for i, c := range categoryColumns {
		s.Categories[c.cat] = cats[i] == 1
	}
	if sealed != "" {
		secret, err := secrets.Open(sealed)
		if err != nil {
			return nil, fmt.Errorf("opening the webhook secret: %w", err)
		}
		s.WebhookSecret = secret
	}
	rows, err := db.Query(`SELECT url, fetch_cred, push_cred FROM git_remotes ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r RemoteSetting
		if err := rows.Scan(&r.URL, &r.FetchCred, &r.PushCred); err != nil {
			return nil, err
		}
		s.Remotes = append(s.Remotes, r)
	}
	if s.Remotes == nil {
		s.Remotes = []RemoteSetting{}
	}
	return s, rows.Err()
}

var (
	branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)
	scpRe    = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._/~-]+$`)
	urlRe    = regexp.MustCompile(`^(https?|ssh|git|file)://[^\s]+$`)
	credRe   = regexp.MustCompile(`^[A-Za-z0-9 ._-]{0,64}$`)
)

// ValidRemoteURL accepts https, http, ssh, git and file URLs and scp-like
// SSH addresses (git@host:org/repo.git).
func ValidRemoteURL(u string) bool {
	if strings.HasPrefix(u, "-") {
		return false
	}
	return urlRe.MatchString(u) || scpRe.MatchString(u)
}

// Validate checks settings before they are saved.
func (s *Settings) Validate() error {
	var errs []string
	if s.Enabled && len(s.Remotes) == 0 {
		errs = append(errs, "add at least one remote")
	}
	if len(s.Remotes) > 8 {
		errs = append(errs, "at most 8 remotes")
	}
	seen := map[string]bool{}
	for i, r := range s.Remotes {
		if !ValidRemoteURL(r.URL) {
			errs = append(errs, fmt.Sprintf("remote %d: invalid URL %q", i+1, r.URL))
		}
		if seen[r.URL] {
			errs = append(errs, fmt.Sprintf("remote %d: listed twice", i+1))
		}
		seen[r.URL] = true
		if !credRe.MatchString(r.FetchCred) || !credRe.MatchString(r.PushCred) {
			errs = append(errs, fmt.Sprintf("remote %d: invalid credential name", i+1))
		}
	}
	if !branchRe.MatchString(s.Branch) || strings.Contains(s.Branch, "..") || strings.HasPrefix(s.Branch, "dplaneos/") {
		errs = append(errs, fmt.Sprintf("invalid branch %q (dplaneos/… is used for pull requests)", s.Branch))
	}
	switch s.Layout {
	case LayoutSingle:
		if _, err := cleanStatePath(s.StatePath); err != nil {
			errs = append(errs, err.Error())
		}
	case LayoutScoped:
	default:
		errs = append(errs, fmt.Sprintf("invalid layout %q", s.Layout))
	}
	switch s.Policy {
	case PolicyDirect:
	case PolicyPullRequest:
		if s.Provider == ProviderNone || s.Provider == "" {
			errs = append(errs, "pull requests need a provider (GitHub, Gitea/Forgejo or GitLab)")
		}
	default:
		errs = append(errs, fmt.Sprintf("invalid policy %q", s.Policy))
	}
	switch s.Provider {
	case ProviderNone, ProviderGitHub, ProviderGitea, ProviderGitLab:
	default:
		errs = append(errs, fmt.Sprintf("invalid provider %q", s.Provider))
	}
	if s.Provider != ProviderNone && s.Provider != "" {
		if s.ProviderRepo == "" || !strings.Contains(s.ProviderRepo, "/") {
			errs = append(errs, "provider repository: owner/name")
		}
		if s.ProviderCred == "" || !credRe.MatchString(s.ProviderCred) {
			errs = append(errs, "provider: choose the credential with an API token")
		}
		if s.Provider == ProviderGitea && s.ProviderAPIURL == "" {
			errs = append(errs, "Gitea/Forgejo: the API URL is needed (https://host/api/v1)")
		}
	}
	if s.VerifySignatures && strings.TrimSpace(s.AllowedSigners) == "" && strings.TrimSpace(s.GPGKeys) == "" {
		errs = append(errs, "signature verification needs at least one allowed SSH or GPG key")
	}
	if s.IntervalSecs < 30 || s.IntervalSecs > 86400 {
		errs = append(errs, "the check interval is 30 seconds to 24 hours")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Save stores settings as a new version written by node.
func Save(db *sql.DB, s *Settings, node string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	cur, err := Load(db)
	if err != nil {
		return err
	}
	s.Version, s.Node = cur.Version+1, node
	return store(db, s)
}

// Adopt stores settings received from a paired node if they are newer.
// Equal versions are ordered by node name, so all nodes settle on one.
func Adopt(db *sql.DB, s *Settings) (bool, error) {
	if err := s.Validate(); err != nil {
		return false, err
	}
	cur, err := Load(db)
	if err != nil {
		return false, err
	}
	if s.Version < cur.Version || (s.Version == cur.Version && s.Node <= cur.Node) {
		return false, nil
	}
	return true, store(db, s)
}

func store(db *sql.DB, s *Settings) error {
	sealed := ""
	if s.WebhookSecret != "" {
		var err error
		if sealed, err = secrets.Seal(s.WebhookSecret); err != nil {
			return err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	enabled := 0
	if s.Enabled {
		enabled = 1
	}
	set := `enabled = $1, branch = $2, layout = $3, state_path = $4, policy = $5, provider = $6, provider_api_url = $7,
		provider_repo = $8, provider_cred = $9, auto_apply = $10, prune = $11, self_heal = $12, verify_signatures = $13,
		allowed_signers = $14, gpg_keys = $15, sign_commits = $16, interval_secs = $17, webhook_secret = $18,
		settings_version = $19, settings_node = $20, updated_at = NOW()`
	args := []any{enabled, s.Branch, s.Layout, s.StatePath, s.Policy, s.Provider, s.ProviderAPIURL, s.ProviderRepo,
		s.ProviderCred, s.AutoApply, s.Prune, s.SelfHeal, s.VerifySignatures, s.AllowedSigners, s.GPGKeys, s.SignCommits,
		s.IntervalSecs, sealed, s.Version, s.Node}
	for _, c := range categoryColumns {
		args = append(args, boolInt(s.Categories[c.cat]))
		set += fmt.Sprintf(", %s = $%d", c.col, len(args))
	}
	if _, err := tx.Exec(`UPDATE gitops_config SET `+set+` WHERE id = 1`, args...); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM git_remotes`); err != nil {
		return err
	}
	for i, r := range s.Remotes {
		if _, err := tx.Exec(`INSERT INTO git_remotes (position, url, fetch_cred, push_cred) VALUES ($1, $2, $3, $4)`,
			i, r.URL, r.FetchCred, r.PushCred); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ── Credentials ───────────────────────────────────────────────────────────────

// Credential is a stored Git credential (secrets in plain text, in memory only).
type Credential struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	AuthType string `json:"auth_type"` // token, ssh
	Username string `json:"username"`
	Token    string `json:"-"`
	SSHKey   string `json:"-"`
}

// LoadCredential reads a credential by name.
func LoadCredential(db *sql.DB, name string) (*Credential, error) {
	c := &Credential{Name: name}
	var token, key string
	err := db.QueryRow(`SELECT host, auth_type, username, token, ssh_key FROM git_credentials WHERE name = $1`, name).
		Scan(&c.Host, &c.AuthType, &c.Username, &token, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("credential %q does not exist on this node", name)
	}
	if err != nil {
		return nil, err
	}
	if c.Token, err = secrets.Open(token); err != nil {
		return nil, fmt.Errorf("credential %q: %w", name, err)
	}
	if c.SSHKey, err = secrets.Open(key); err != nil {
		return nil, fmt.Errorf("credential %q: %w", name, err)
	}
	return c, nil
}

// SaveCredential stores (or replaces) a credential, sealing its secrets.
func SaveCredential(db *sql.DB, c *Credential) error {
	token, err := secrets.Seal(c.Token)
	if err != nil {
		return err
	}
	key, err := secrets.Seal(c.SSHKey)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO git_credentials (name, host, auth_type, username, token, ssh_key) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (name) DO UPDATE SET host = EXCLUDED.host, auth_type = EXCLUDED.auth_type, username = EXCLUDED.username,
			token = EXCLUDED.token, ssh_key = EXCLUDED.ssh_key`, c.Name, c.Host, c.AuthType, c.Username, token, key)
	return err
}

// credEnv prepares the environment for git to authenticate with c. The
// secrets go to files in a private directory, removed by cleanup.
func credEnv(c *Credential, knownHosts string) (env []string, cleanup func(), err error) {
	cleanup = func() {}
	if c == nil {
		return nil, cleanup, nil
	}
	dir, err := os.MkdirTemp("", "dplaneos-git-")
	if err != nil {
		return nil, cleanup, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	switch c.AuthType {
	case "token":
		if c.Token == "" {
			return nil, cleanup, fmt.Errorf("credential %q has no token", c.Name)
		}
		user := c.Username
		if user == "" {
			user = "x-access-token"
		}
		tok := filepath.Join(dir, "token")
		usr := filepath.Join(dir, "user")
		if err := os.WriteFile(tok, []byte(c.Token), 0o600); err != nil {
			return nil, cleanup, err
		}
		if err := os.WriteFile(usr, []byte(user), 0o600); err != nil {
			return nil, cleanup, err
		}
		askpass := filepath.Join(dir, "askpass")
		script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n*sername*) cat '%s' ;;\n*) cat '%s' ;;\nesac\n", usr, tok)
		if err := os.WriteFile(askpass, []byte(script), 0o700); err != nil {
			return nil, cleanup, err
		}
		return []string{"GIT_ASKPASS=" + askpass}, cleanup, nil
	case "ssh":
		if c.SSHKey == "" {
			return nil, cleanup, fmt.Errorf("credential %q has no SSH key", c.Name)
		}
		key := filepath.Join(dir, "key")
		material := c.SSHKey
		if !strings.HasSuffix(material, "\n") {
			material += "\n"
		}
		if err := os.WriteFile(key, []byte(material), 0o600); err != nil {
			return nil, cleanup, err
		}
		// ConnectTimeout and keepalives: an unreachable or silent remote fails
		// within about half a minute.
		cmd := fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s -o ConnectTimeout=15 -o ServerAliveInterval=10 -o ServerAliveCountMax=3", key, knownHosts)
		return []string{"GIT_SSH_COMMAND=" + cmd}, cleanup, nil
	}
	return nil, cleanup, fmt.Errorf("credential %q: unknown type %q", c.Name, c.AuthType)
}
