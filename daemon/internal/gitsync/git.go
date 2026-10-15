package gitsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Git access through one local clone. The clone is disposable: everything
// that matters is in the configuration store and the remotes, so a broken or
// missing clone is simply created again.
//
// Remotes are named r0, r1, … in the configured order. Each has its own
// credentials (env); a read-only deploy key can fetch while another
// credential pushes.

// Remote is one URL of the repository.
type Remote struct {
	Name string // r0, r1, …
	URL  string
	Env  []string // credentials for fetch and push (askpass or ssh command)
}

// Clone is the local clone.
type Clone struct {
	Dir     string
	Branch  string
	Remotes []Remote
	Timeout time.Duration
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// GitError is a failed git command with its output.
type GitError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *GitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return fmt.Sprintf("git %s: %v: %s", strings.Join(redactArgs(e.Args), " "), e.Err, msg)
}

func (e *GitError) Unwrap() error { return e.Err }

// redactArgs hides credentials embedded in URLs (https://user:token@host).
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = credInURL.ReplaceAllString(a, "${1}***@")
	}
	return out
}

var credInURL = regexp.MustCompile(`(https?://)[^/@\s]+@`)

func (c *Clone) git(env []string, stdin []byte, args ...string) (string, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.Dir
	// A clean, non-interactive environment: no user config, no prompts.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"LC_ALL=C",
		// A remote that accepts the connection but sends nothing (packets
		// dropped on the way) fails after a minute without a single byte
		// instead of holding the cycle for the whole timeout; a slow link
		// that still transfers is not affected.
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=http.lowSpeedLimit", "GIT_CONFIG_VALUE_0=1",
		"GIT_CONFIG_KEY_1=http.lowSpeedTime", "GIT_CONFIG_VALUE_1=60")
	cmd.Env = append(cmd.Env, env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		return out.String(), &GitError{Args: args, Stderr: errb.String(), Err: err}
	}
	return out.String(), nil
}

func (c *Clone) gitTrim(env []string, args ...string) (string, error) {
	out, err := c.git(env, nil, args...)
	return strings.TrimSpace(out), err
}

// Ensure creates the clone if needed and points the remotes at the
// configured URLs (stale remotes are removed).
func (c *Clone) Ensure() error {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(c.Dir, ".git")); err != nil {
		if _, err := c.git(nil, nil, "init", "--quiet"); err != nil {
			return err
		}
	}
	existing, err := c.gitTrim(nil, "remote")
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, r := range strings.Fields(existing) {
		have[r] = true
	}
	want := map[string]bool{}
	for _, r := range c.Remotes {
		want[r.Name] = true
		if have[r.Name] {
			if _, err := c.git(nil, nil, "remote", "set-url", r.Name, r.URL); err != nil {
				return err
			}
		} else if _, err := c.git(nil, nil, "remote", "add", r.Name, r.URL); err != nil {
			return err
		}
	}
	for r := range have {
		if !want[r] {
			if _, err := c.git(nil, nil, "remote", "remove", r); err != nil {
				return err
			}
		}
	}
	return nil
}

// Reset removes the clone (it is recreated on the next Ensure).
func (c *Clone) Reset() error { return os.RemoveAll(c.Dir) }

// RemoteHead is the branch head of one remote, or why it could not be read.
type RemoteHead struct {
	Remote string `json:"remote"`
	SHA    string `json:"sha"` // "" when the branch does not exist there yet
	Err    error  `json:"-"`
}

// FetchBranch fetches branch from every remote. It returns the commit to
// work from: the newest head (every other reachable head is its ancestor),
// "" when no remote has the branch yet. Remotes that diverged are an error:
// someone has to reconcile them by hand.
func (c *Clone) FetchBranch(branch string) (string, []RemoteHead, error) {
	var heads []RemoteHead
	reached := 0
	for _, r := range c.Remotes {
		h := RemoteHead{Remote: r.Name}
		ls, err := c.gitTrim(r.Env, "ls-remote", "--heads", r.Name, "refs/heads/"+branch)
		switch {
		case err != nil:
			h.Err = err
		case ls == "":
			reached++
		default:
			reached++
			if _, err := c.git(r.Env, nil, "fetch", "--quiet", "--no-tags", r.Name,
				"+refs/heads/"+branch+":refs/remotes/"+r.Name+"/"+branch); err != nil {
				h.Err = err
				reached--
			} else {
				h.SHA, h.Err = c.gitTrim(nil, "rev-parse", "refs/remotes/"+r.Name+"/"+branch)
			}
		}
		heads = append(heads, h)
	}
	if reached == 0 {
		var errs []error
		for _, h := range heads {
			errs = append(errs, fmt.Errorf("%s: %w", h.Remote, h.Err))
		}
		return "", heads, fmt.Errorf("no remote of the repository can be reached: %w", errors.Join(errs...))
	}
	newest := ""
	for _, h := range heads {
		if h.Err != nil || h.SHA == "" {
			continue
		}
		switch {
		case newest == "" || h.SHA == newest || c.IsAncestor(h.SHA, newest):
			if newest == "" {
				newest = h.SHA
			}
		case c.IsAncestor(newest, h.SHA):
			newest = h.SHA
		default:
			return "", heads, fmt.Errorf("the remotes of the repository diverged (%s has %.12s, another %.12s): bring them to the same history", h.Remote, h.SHA, newest)
		}
	}
	return newest, heads, nil
}

// IsAncestor reports whether a is an ancestor of (or equal to) b.
func (c *Clone) IsAncestor(a, b string) bool {
	_, err := c.git(nil, nil, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// Show returns a file at a commit; "" with no error when it does not exist.
func (c *Clone) Show(sha, path string) (string, error) {
	if sha == "" {
		return "", nil
	}
	if !shaRe.MatchString(sha) {
		return "", fmt.Errorf("invalid commit %q", sha)
	}
	if _, err := c.git(nil, nil, "cat-file", "-e", sha+":"+path); err != nil {
		return "", nil
	}
	return c.git(nil, nil, "show", sha+":"+path)
}

// Identity is the author and committer of commits this node writes.
type Identity struct {
	Name, Email string
	SigningKey  string // path of an SSH private key; "" = unsigned
}

// Commit writes files (path → content; "" removes the file) on top of
// parent ("" = first commit) and returns the new commit, or parent when
// nothing changed.
func (c *Clone) Commit(parent string, files map[string]string, message string, id Identity) (string, error) {
	if parent != "" && !shaRe.MatchString(parent) {
		return "", fmt.Errorf("invalid commit %q", parent)
	}
	// A private index: the working tree is not used.
	index := filepath.Join(c.Dir, ".git", "dplaneos-index")
	_ = os.Remove(index)
	defer os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index}
	if parent != "" {
		if _, err := c.git(env, nil, "read-tree", parent); err != nil {
			return "", err
		}
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if files[p] == "" {
			if _, err := c.git(env, nil, "rm", "--cached", "--quiet", "--ignore-unmatch", "--", p); err != nil {
				return "", err
			}
			continue
		}
		blob, err := c.git(nil, []byte(files[p]), "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		if _, err := c.git(env, nil, "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(blob)+","+p); err != nil {
			return "", err
		}
	}
	tree, err := c.gitTrim(env, "write-tree")
	if err != nil {
		return "", err
	}
	if parent != "" {
		old, err := c.gitTrim(nil, "rev-parse", parent+"^{tree}")
		if err != nil {
			return "", err
		}
		if old == tree {
			return parent, nil
		}
	}
	args := []string{"-c", "user.name=" + id.Name, "-c", "user.email=" + id.Email}
	if id.SigningKey != "" {
		args = append(args, "-c", "gpg.format=ssh", "-c", "user.signingkey="+id.SigningKey)
	}
	args = append(args, "commit-tree", tree, "-m", message)
	if parent != "" {
		args = append(args, "-p", parent)
	}
	if id.SigningKey != "" {
		args = append(args, "-S")
	}
	cenv := []string{"GIT_AUTHOR_NAME=" + id.Name, "GIT_AUTHOR_EMAIL=" + id.Email,
		"GIT_COMMITTER_NAME=" + id.Name, "GIT_COMMITTER_EMAIL=" + id.Email}
	return c.gitTrim(cenv, args...)
}

// PushResult is the outcome of a push to one remote.
type PushResult struct {
	Remote   string `json:"remote"`
	OK       bool   `json:"ok"`
	Rejected bool   `json:"rejected"` // the remote moved on (not a fast-forward)
	Error    string `json:"error,omitempty"`
}

// Push pushes sha to branch on every remote. force replaces the branch
// (used only for this node's own pull-request branches).
func (c *Clone) Push(sha, branch string, force bool) []PushResult {
	var out []PushResult
	for _, r := range c.Remotes {
		ref := sha + ":refs/heads/" + branch
		if force {
			ref = "+" + ref
		}
		res := PushResult{Remote: r.Name}
		if out, err := c.git(r.Env, nil, "push", "--porcelain", r.Name, ref); err != nil {
			res.Error = err.Error()
			// --porcelain reports a refused update on stdout ("!	…	[rejected] (non-fast-forward)").
			var ge *GitError
			text := out
			if errors.As(err, &ge) {
				text += ge.Stderr
			}
			res.Rejected = strings.Contains(text, "[rejected]") || strings.Contains(text, "non-fast-forward") ||
				strings.Contains(text, "fetch first")
		} else {
			res.OK = true
		}
		out = append(out, res)
	}
	return out
}

// Commits lists the commits after base up to head (oldest first).
func (c *Clone) Commits(base, head string) ([]string, error) {
	rng := head
	if base != "" {
		rng = base + ".." + head
	}
	out, err := c.gitTrim(nil, "rev-list", "--reverse", rng)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Fields(out), nil
}

// Subject returns the first line of a commit message.
func (c *Clone) Subject(sha string) string {
	s, _ := c.gitTrim(nil, "log", "-1", "--format=%s", sha)
	return s
}
