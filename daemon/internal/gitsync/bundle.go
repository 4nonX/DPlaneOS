package gitsync

import (
	"database/sql"
	"sort"
)

// Every node that may become the Git owner of a scope (the coordinator, a
// storage group's candidates) needs the repository settings and the
// credentials they name. A node that saves the settings sends them to its
// paired nodes as one bundle, encrypted with the pair's key; the newest
// version wins everywhere (Adopt).

// Bundle is the repository settings with the credentials they use.
type Bundle struct {
	Settings      Settings   `json:"settings"`
	WebhookSecret string     `json:"webhook_secret"`
	Credentials   []wireCred `json:"credentials"`
}

type wireCred struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	AuthType string `json:"auth_type"`
	Username string `json:"username"`
	Token    string `json:"token"`
	SSHKey   string `json:"ssh_key"`
}

// ExportBundle reads the settings and the credentials they name.
func ExportBundle(db *sql.DB) (*Bundle, error) {
	s, err := Load(db)
	if err != nil {
		return nil, err
	}
	b := &Bundle{Settings: *s, WebhookSecret: s.WebhookSecret}
	names := map[string]bool{}
	for _, r := range s.Remotes {
		names[r.FetchCred], names[r.PushCred] = true, true
	}
	names[s.ProviderCred] = true
	delete(names, "")
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		c, err := LoadCredential(db, n)
		if err != nil {
			return nil, err
		}
		b.Credentials = append(b.Credentials, wireCred{Name: c.Name, Host: c.Host, AuthType: c.AuthType,
			Username: c.Username, Token: c.Token, SSHKey: c.SSHKey})
	}
	return b, nil
}

// ImportBundle stores a bundle from a paired node if its settings are newer.
func ImportBundle(db *sql.DB, b *Bundle) (bool, error) {
	cur, err := Load(db)
	if err != nil {
		return false, err
	}
	s := b.Settings
	if s.Version < cur.Version || (s.Version == cur.Version && s.Node <= cur.Node) {
		return false, nil
	}
	for _, c := range b.Credentials {
		if err := SaveCredential(db, &Credential{Name: c.Name, Host: c.Host, AuthType: c.AuthType,
			Username: c.Username, Token: c.Token, SSHKey: c.SSHKey}); err != nil {
			return false, err
		}
	}
	s.WebhookSecret = b.WebhookSecret
	return Adopt(db, &s)
}
