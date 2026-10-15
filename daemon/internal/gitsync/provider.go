package gitsync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Pull requests and commit statuses on GitHub, Gitea/Forgejo and GitLab
// (ADR-0007, "pull request" policy). One interface, three REST dialects.

// Providers.
const (
	ProviderNone   = "none"
	ProviderGitHub = "github"
	ProviderGitea  = "gitea" // Gitea and Forgejo
	ProviderGitLab = "gitlab"
)

// PR states, normalised.
const (
	PROpen   = "open"
	PRMerged = "merged"
	PRClosed = "closed" // closed without merging
)

// Commit status states, normalised.
const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusFailure = "failure"
)

// PR is a pull (merge) request.
type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

// Provider talks to the hosting service of the repository.
type Provider interface {
	// EnsurePR finds the open pull request from head into base or opens one.
	EnsurePR(head, base, title, body string) (PR, error)
	// GetPR reads a pull request's state.
	GetPR(number int) (PR, error)
	// SetStatus reports the outcome for a commit (shown next to it).
	SetStatus(sha, state, description string) error
}

// ProviderConfig selects and configures a provider.
type ProviderConfig struct {
	Kind   string // github, gitea, gitlab
	APIURL string // e.g. https://api.github.com, https://git.lan/api/v1, https://gitlab.com/api/v4
	Repo   string // owner/name (GitLab: the project path, group/subgroup/name)
	Token  string
}

// NewProvider returns the provider, or nil for none.
func NewProvider(c ProviderConfig) (Provider, error) {
	if c.Kind == "" || c.Kind == ProviderNone {
		return nil, nil
	}
	if c.Repo == "" || strings.Count(c.Repo, "/") < 1 {
		return nil, fmt.Errorf("repository %q: expected owner/name", c.Repo)
	}
	if c.Token == "" {
		return nil, fmt.Errorf("a token with permission to open pull requests is needed")
	}
	api := strings.TrimRight(c.APIURL, "/")
	if api == "" && c.Kind == ProviderGitHub {
		api = "https://api.github.com"
	}
	if api == "" && c.Kind == ProviderGitLab {
		api = "https://gitlab.com/api/v4"
	}
	u, err := url.Parse(api)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("invalid API URL %q", c.APIURL)
	}
	h := &httpAPI{base: api, client: &http.Client{Timeout: 30 * time.Second}}
	switch c.Kind {
	case ProviderGitHub:
		h.auth = func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+c.Token)
			r.Header.Set("Accept", "application/vnd.github+json")
		}
		return &github{api: h, repo: c.Repo}, nil
	case ProviderGitea:
		h.auth = func(r *http.Request) { r.Header.Set("Authorization", "token "+c.Token) }
		return &gitea{api: h, repo: c.Repo}, nil
	case ProviderGitLab:
		h.auth = func(r *http.Request) { r.Header.Set("PRIVATE-TOKEN", c.Token) }
		return &gitlab{api: h, project: url.PathEscape(c.Repo)}, nil
	}
	return nil, fmt.Errorf("unknown provider %q", c.Kind)
}

type httpAPI struct {
	base   string
	client *http.Client
	auth   func(*http.Request)
}

// APIError is a failed request with the service's answer.
type APIError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *APIError) Error() string {
	b := strings.TrimSpace(e.Body)
	if len(b) > 300 {
		b = b[:300] + "…"
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, b)
}

func (a *httpAPI) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	a.auth(req)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Method: method, Path: path, Status: resp.StatusCode, Body: string(raw)}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// ── GitHub ────────────────────────────────────────────────────────────────────

type github struct {
	api  *httpAPI
	repo string
}

type ghPull struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p ghPull) pr() PR {
	st := PROpen
	switch {
	case p.Merged:
		st = PRMerged
	case p.State == "closed":
		st = PRClosed
	}
	return PR{Number: p.Number, URL: p.HTMLURL, State: st}
}

func (g *github) EnsurePR(head, base, title, body string) (PR, error) {
	owner, _, _ := strings.Cut(g.repo, "/")
	var open []ghPull
	q := url.Values{"state": {"open"}, "head": {owner + ":" + head}, "base": {base}}
	if err := g.api.do("GET", "/repos/"+g.repo+"/pulls?"+q.Encode(), nil, &open); err != nil {
		return PR{}, err
	}
	if len(open) > 0 {
		return open[0].pr(), nil
	}
	var created ghPull
	err := g.api.do("POST", "/repos/"+g.repo+"/pulls", map[string]string{"title": title, "head": head, "base": base, "body": body}, &created)
	return created.pr(), err
}

func (g *github) GetPR(n int) (PR, error) {
	var p ghPull
	err := g.api.do("GET", "/repos/"+g.repo+"/pulls/"+strconv.Itoa(n), nil, &p)
	return p.pr(), err
}

func (g *github) SetStatus(sha, state, desc string) error {
	st := map[string]string{StatusPending: "pending", StatusSuccess: "success", StatusFailure: "failure"}[state]
	return g.api.do("POST", "/repos/"+g.repo+"/statuses/"+sha,
		map[string]string{"state": st, "description": truncate(desc, 140), "context": "dplaneos"}, nil)
}

// ── Gitea / Forgejo ───────────────────────────────────────────────────────────

type gitea struct {
	api  *httpAPI
	repo string
}

func (g *gitea) EnsurePR(head, base, title, body string) (PR, error) {
	var open []ghPull // same shape as GitHub's for these fields
	if err := g.api.do("GET", "/repos/"+g.repo+"/pulls?state=open&limit=50", nil, &open); err != nil {
		return PR{}, err
	}
	for _, p := range open {
		if p.Head.Ref == head && p.Base.Ref == base {
			return p.pr(), nil
		}
	}
	var created ghPull
	err := g.api.do("POST", "/repos/"+g.repo+"/pulls", map[string]string{"title": title, "head": head, "base": base, "body": body}, &created)
	return created.pr(), err
}

func (g *gitea) GetPR(n int) (PR, error) {
	var p ghPull
	err := g.api.do("GET", "/repos/"+g.repo+"/pulls/"+strconv.Itoa(n), nil, &p)
	return p.pr(), err
}

func (g *gitea) SetStatus(sha, state, desc string) error {
	st := map[string]string{StatusPending: "pending", StatusSuccess: "success", StatusFailure: "failure"}[state]
	return g.api.do("POST", "/repos/"+g.repo+"/statuses/"+sha,
		map[string]string{"state": st, "description": truncate(desc, 140), "context": "dplaneos"}, nil)
}

// ── GitLab ────────────────────────────────────────────────────────────────────

type gitlab struct {
	api     *httpAPI
	project string // URL-escaped path
}

type glMR struct {
	IID    int    `json:"iid"`
	WebURL string `json:"web_url"`
	State  string `json:"state"` // opened, closed, merged, locked
}

func (m glMR) pr() PR {
	st := PROpen
	switch m.State {
	case "merged":
		st = PRMerged
	case "closed":
		st = PRClosed
	}
	return PR{Number: m.IID, URL: m.WebURL, State: st}
}

func (g *gitlab) EnsurePR(head, base, title, body string) (PR, error) {
	var open []glMR
	q := url.Values{"state": {"opened"}, "source_branch": {head}, "target_branch": {base}}
	if err := g.api.do("GET", "/projects/"+g.project+"/merge_requests?"+q.Encode(), nil, &open); err != nil {
		return PR{}, err
	}
	if len(open) > 0 {
		return open[0].pr(), nil
	}
	var created glMR
	err := g.api.do("POST", "/projects/"+g.project+"/merge_requests",
		map[string]string{"source_branch": head, "target_branch": base, "title": title, "description": body}, &created)
	return created.pr(), err
}

func (g *gitlab) GetPR(n int) (PR, error) {
	var m glMR
	err := g.api.do("GET", "/projects/"+g.project+"/merge_requests/"+strconv.Itoa(n), nil, &m)
	return m.pr(), err
}

func (g *gitlab) SetStatus(sha, state, desc string) error {
	st := map[string]string{StatusPending: "pending", StatusSuccess: "success", StatusFailure: "failed"}[state]
	return g.api.do("POST", "/projects/"+g.project+"/statuses/"+sha,
		map[string]string{"state": st, "description": truncate(desc, 140), "name": "dplaneos"}, nil)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
