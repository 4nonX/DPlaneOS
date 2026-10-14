package gitsync

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dplaned/internal/configstore"
)

// Engine synchronises this node's scopes with the repository (Design 0001
// section 5.8, phase 5).
//
// One cycle: fetch the newest commit; for every scope this node owns,
// compare base, local and repository versions (Compare); record what is in
// sync; hand conflicts to the review screen; apply repository changes when
// automatic apply is on (or they were approved) and the scope is not on
// hold; push this node's changes, to the branch or as a pull request.
//
// Nothing is kept in an outbox table: what a node still has to push is
// exactly what differs between its store and the base of the reconciled
// commit, so a crash or a failover loses nothing (the new owner computes the
// same difference from its own store, which holds the same revisions).
type Engine struct {
	env     Env
	runMu   sync.Mutex // one cycle at a time
	mu      sync.Mutex // status and requests
	status  Status
	approve map[string]string // scope → commit approved for apply
	restore map[string]bool   // scope → take the repository's version of local-only changes
	trigger chan struct{}
	last    time.Time
	backoff time.Duration
}

// Env is what the engine needs from the daemon.
type Env struct {
	DB         *sql.DB
	Syncer     *configstore.Syncer
	Dir        string // state directory: clone, known_hosts, signing key
	NodeName   func() string
	IsOwner    func(scope string) (bool, string) // and why not
	GroupOf    func(pool string) string
	Groups     func() []string
	Paired     func() bool
	Alert      func(level, event, resource, message string)
	Broadcast  func(event string, data any, level string)
	WebhookURL string // path shown to the operator
}

// Status is what the GitOps page shows.
type Status struct {
	Enabled   bool          `json:"enabled"`
	CheckedAt time.Time     `json:"checked_at"`
	Head      string        `json:"head"`
	Remotes   []RemoteState `json:"remotes"`
	Scopes    []ScopeStatus `json:"scopes"`
	Error     string        `json:"error,omitempty"`
	NextRun   time.Time     `json:"next_run"`
	SigningPK string        `json:"signing_public_key,omitempty"`
}

// RemoteState is one remote as seen in the last cycle.
type RemoteState struct {
	URL   string `json:"url"`
	Head  string `json:"head"`
	Error string `json:"error,omitempty"`
}

// ScopeStatus is one scope as seen in the last cycle.
type ScopeStatus struct {
	Scope       string    `json:"scope"`
	Path        string    `json:"path"`
	Owner       bool      `json:"owner"`
	OwnerReason string    `json:"owner_reason,omitempty"`
	SyncedSHA   string    `json:"synced_sha"`
	Push        []string  `json:"push"`
	Pull        []Change  `json:"pull"`
	Conflicts   []Change  `json:"conflicts"`
	EmptyKinds  []string  `json:"empty_kinds,omitempty"`
	PR          *PR       `json:"pr,omitempty"`
	Declined    bool      `json:"declined"` // the pull request was closed without merging
	Hold        *Hold     `json:"hold,omitempty"`
	Error       string    `json:"error,omitempty"`
	Applied     []string  `json:"applied,omitempty"` // applied in the last cycle
	CheckedAt   time.Time `json:"checked_at"`
}

// New creates the engine.
func New(env Env) *Engine {
	return &Engine{env: env, approve: map[string]string{}, restore: map[string]bool{}, trigger: make(chan struct{}, 1)}
}

// Trigger asks for a cycle soon (after a change, a webhook, a request).
func (e *Engine) Trigger() {
	select {
	case e.trigger <- struct{}{}:
	default:
	}
}

// Start runs cycles on the configured interval and when triggered.
func (e *Engine) Start() {
	go func() {
		for {
			wait := e.wait()
			select {
			case <-time.After(wait):
			case <-e.trigger:
				// Coalesce bursts of changes (one GUI action records several).
				time.Sleep(3 * time.Second)
				select {
				case <-e.trigger:
				default:
				}
			}
			if err := e.Run(); err != nil {
				log.Printf("GIT: %v", err)
			}
		}
	}()
}

func (e *Engine) wait() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.backoff > 0 {
		return e.backoff
	}
	s, err := Load(e.env.DB)
	if err != nil || !s.Enabled {
		return 5 * time.Minute
	}
	return time.Duration(s.IntervalSecs) * time.Second
}

// Status returns the last cycle's view.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.status
	st.Scopes = append([]ScopeStatus(nil), st.Scopes...)
	return st
}

// Approve applies the repository's changes of scope at commit sha on the
// next cycle (automatic apply off, or deletions without prune).
func (e *Engine) Approve(scope, sha string) error {
	e.mu.Lock()
	cur := e.status.Head
	known := false
	for _, s := range e.status.Scopes {
		known = known || s.Scope == scope
	}
	if !known {
		e.mu.Unlock()
		return fmt.Errorf("unknown scope %q", scope)
	}
	if cur == "" || cur != sha {
		e.mu.Unlock()
		return fmt.Errorf("the repository changed since you looked (now %.12s): review the changes again", cur)
	}
	e.approve[scope] = sha
	e.mu.Unlock()
	return e.Run()
}

// File returns a scope's file in the newest commit fetched.
func (e *Engine) File(scope string) (content, path, sha string, err error) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	s, err := Load(e.env.DB)
	if err != nil {
		return "", "", "", err
	}
	if !s.Enabled {
		return "", "", "", errors.New("Git is not enabled")
	}
	if path, err = ScopePath(s.Layout, s.StatePath, scope); err != nil {
		return "", "", "", err
	}
	e.mu.Lock()
	sha = e.status.Head
	e.mu.Unlock()
	if sha == "" {
		return "", path, "", nil
	}
	content, err = (&Clone{Dir: filepath.Join(e.env.Dir, "clone")}).Show(sha, path)
	return content, path, sha, err
}

// TakeRepository replaces this node's local-only changes of scope with the
// repository's version (for example after a pull request was declined).
func (e *Engine) TakeRepository(scope string) error {
	e.mu.Lock()
	e.restore[scope] = true
	e.mu.Unlock()
	return e.Run()
}

func (e *Engine) setBackoff(failed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !failed {
		e.backoff = 0
		return
	}
	switch {
	case e.backoff == 0:
		e.backoff = 30 * time.Second
	case e.backoff < time.Hour:
		e.backoff *= 2
	}
}

// ── One cycle ─────────────────────────────────────────────────────────────────

type cycle struct {
	e        *Engine
	s        *Settings
	clone    *Clone
	head     string
	node     string
	cleanups []func()
	provider Provider
}

// Run performs one cycle now.
func (e *Engine) Run() error {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	s, err := Load(e.env.DB)
	if err != nil {
		return err
	}
	if !s.Enabled {
		e.mu.Lock()
		e.status = Status{Enabled: false}
		e.mu.Unlock()
		return nil
	}
	c := &cycle{e: e, s: s, node: e.env.NodeName()}
	defer func() {
		for _, f := range c.cleanups {
			f()
		}
	}()
	st := Status{Enabled: true, CheckedAt: time.Now()}
	if s.SignCommits {
		if pk, err := e.signingKey(); err == nil {
			st.SigningPK = pk
		}
	}
	err = c.run(&st)
	if err != nil {
		st.Error = err.Error()
	}
	e.setBackoff(err != nil)
	st.NextRun = time.Now().Add(e.wait())
	e.mu.Lock()
	e.status = st
	e.mu.Unlock()
	return err
}

func (c *cycle) run(st *Status) error {
	e, s := c.e, c.s
	if s.Layout == LayoutSingle && e.env.Paired() {
		return errors.New("this node is paired: the single-file layout cannot say which node writes what. Switch to one file per scope")
	}
	remotes, err := c.remotes()
	if err != nil {
		return err
	}
	c.clone = &Clone{Dir: filepath.Join(e.env.Dir, "clone"), Branch: s.Branch, Remotes: remotes}
	if err := c.clone.Ensure(); err != nil {
		// A broken clone is disposable.
		_ = c.clone.Reset()
		if err := c.clone.Ensure(); err != nil {
			return fmt.Errorf("preparing the local clone: %w", err)
		}
	}
	head, heads, err := c.clone.FetchBranch(s.Branch)
	for i, h := range heads {
		rs := RemoteState{URL: s.Remotes[i].URL, Head: h.SHA}
		if h.Err != nil {
			rs.Error = h.Err.Error()
		}
		st.Remotes = append(st.Remotes, rs)
	}
	if err != nil {
		return err
	}
	c.head, st.Head = head, head
	if s.Provider != ProviderNone && s.Provider != "" {
		cred, err := LoadCredential(e.env.DB, s.ProviderCred)
		if err != nil {
			return fmt.Errorf("provider: %w", err)
		}
		if c.provider, err = NewProvider(ProviderConfig{Kind: s.Provider, APIURL: s.ProviderAPIURL, Repo: s.ProviderRepo, Token: cred.Token}); err != nil {
			return fmt.Errorf("provider: %w", err)
		}
	}

	latest, err := configstore.Latest(e.env.DB)
	if err != nil {
		return err
	}
	byScope := map[string]map[string]configstore.Revision{}
	scopes := map[string]bool{}
	if s.Layout == LayoutSingle {
		scopes[ScopeAll] = true
	} else {
		scopes[ScopeCluster] = true
		scopes[NodeScope(c.node)] = true
		for _, g := range e.env.Groups() {
			scopes[GroupScope(g)] = true
		}
	}
	for id, r := range latest {
		if !Managed(r.Kind, s.Categories) {
			continue
		}
		sc := ScopeOf(s.Layout, r.Resource(), e.env.GroupOf, c.node)
		if byScope[sc] == nil {
			byScope[sc] = map[string]configstore.Revision{}
		}
		byScope[sc][id] = r
		scopes[sc] = true
	}
	names := make([]string, 0, len(scopes))
	for sc := range scopes {
		names = append(names, sc)
	}
	sort.Strings(names)
	var firstErr error
	for _, sc := range names {
		ss := c.scope(sc, byScope[sc])
		if ss.Error != "" && firstErr == nil && ss.Owner {
			firstErr = fmt.Errorf("%s: %s", sc, ss.Error)
		}
		st.Scopes = append(st.Scopes, ss)
	}
	return firstErr
}

// remotes builds the remotes with their credentials (push credentials are
// used for both directions when they differ: git uses one per command, so
// fetch runs with the fetch credential and push with the push credential).
func (c *cycle) remotes() ([]Remote, error) {
	var out []Remote
	known := filepath.Join(c.e.env.Dir, "known_hosts")
	for i, r := range c.s.Remotes {
		env, err := c.credentialEnv(r.FetchCred, known)
		if err != nil {
			return nil, fmt.Errorf("remote %d: %w", i+1, err)
		}
		out = append(out, Remote{Name: fmt.Sprintf("r%d", i), URL: r.URL, Env: env})
	}
	return out, nil
}

func (c *cycle) credentialEnv(name, known string) ([]string, error) {
	if name == "" {
		return nil, nil
	}
	cred, err := LoadCredential(c.e.env.DB, name)
	if err != nil {
		return nil, err
	}
	env, cleanup, err := credEnv(cred, known)
	c.cleanups = append(c.cleanups, cleanup)
	return env, err
}

// pushRemotes are the remotes with their push credentials.
func (c *cycle) pushRemotes() (*Clone, error) {
	known := filepath.Join(c.e.env.Dir, "known_hosts")
	pc := &Clone{Dir: c.clone.Dir, Branch: c.clone.Branch}
	for i, r := range c.s.Remotes {
		env, err := c.credentialEnv(r.PushCred, known)
		if err != nil {
			return nil, fmt.Errorf("remote %d: %w", i+1, err)
		}
		pc.Remotes = append(pc.Remotes, Remote{Name: fmt.Sprintf("r%d", i), URL: r.URL, Env: env})
	}
	return pc, nil
}

func (c *cycle) scope(sc string, local map[string]configstore.Revision) ScopeStatus {
	e, s := c.e, c.s
	ss := ScopeStatus{Scope: sc, Push: []string{}, Pull: []Change{}, Conflicts: []Change{}, CheckedAt: time.Now()}
	if local == nil {
		local = map[string]configstore.Revision{}
	}
	path, err := ScopePath(s.Layout, s.StatePath, sc)
	if err != nil {
		ss.Error = err.Error()
		return ss
	}
	ss.Path = path
	ss.Owner, ss.OwnerReason = e.env.IsOwner(sc)
	state, err := loadScope(e.env.DB, sc)
	if err != nil {
		ss.Error = err.Error()
		return ss
	}
	ss.SyncedSHA = state.SyncedSHA
	if state.PRNumber > 0 {
		ss.PR = &PR{Number: state.PRNumber, URL: state.PRURL, State: state.PRState}
	}
	hold, err := OpenHold(e.env.DB, sc)
	if err != nil {
		ss.Error = err.Error()
		return ss
	}
	ss.Hold = hold
	if !ss.Owner {
		return ss // another node writes this scope
	}

	// The repository's version of the scope.
	content, err := c.clone.Show(c.head, path)
	if err != nil {
		ss.Error = err.Error()
		return ss
	}
	parsed, err := Parse(content, c.node)
	if err != nil {
		ss.Hold = c.hold(sc, fmt.Sprintf("%s in commit %.12s cannot be read: %v", path, c.head, err), state.SyncedSHA, hold)
		return ss
	}
	var remote []configstore.Resource
	for _, r := range parsed {
		if Managed(r.Kind, s.Categories) && ScopeOf(s.Layout, r, e.env.GroupOf, c.node) == sc {
			remote = append(remote, r)
		}
	}

	// Commits to take must be signed by an allowed key.
	if s.VerifySignatures && c.head != "" && c.head != state.SyncedSHA {
		commits, err := c.clone.Commits(state.SyncedSHA, c.head)
		if err != nil || state.SyncedSHA == "" {
			commits = []string{c.head} // first synchronisation: the commit taken
		}
		if bad, err := c.clone.VerifyCommits(commits, Signers{SSH: s.AllowedSigners, GPG: s.GPGKeys}); err != nil {
			ss.Hold = c.hold(sc, err.Error(), state.SyncedSHA, hold)
			c.status(StatusFailure, err.Error(), bad)
			return ss // nothing is taken from, or written on top of, an unverified history
		}
	}

	base, err := loadBase(e.env.DB, sc)
	if err != nil {
		ss.Error = err.Error()
		return ss
	}
	plan := Compare(local, remote, base)
	ss.EmptyKinds = plan.EmptyKinds

	// In sync: the base follows.
	inSync := map[string]Base{}
	for _, id := range plan.InSync {
		if l, ok := local[id]; ok && l.Payload != nil {
			inSync[id] = Base{UID: l.UID, Hash: Hash(l.Resource())}
		} else {
			inSync[id] = Base{} // absent on both sides
		}
	}
	if err := saveBase(e.env.DB, sc, inSync); err != nil {
		ss.Error = err.Error()
		return ss
	}

	if len(plan.EmptyKinds) > 0 && ss.Hold == nil {
		ss.Hold = c.hold(sc, fmt.Sprintf("the commit %.12s removes every %s this node has; refused (empty-state guard). Revert the commit, or approve it if this is intended", c.head, strings.Join(plan.EmptyKinds, ", ")), state.SyncedSHA, hold)
	}

	e.mu.Lock()
	approved := e.approve[sc] == c.head && c.head != ""
	delete(e.approve, sc)
	restore := e.restore[sc]
	delete(e.restore, sc)
	e.mu.Unlock()
	if approved && ss.Hold != nil && len(plan.EmptyKinds) > 0 {
		// Approving the guarded commit lifts the guard's hold.
		_ = ClearHold(e.env.DB, sc, "approved", "")
		ss.Hold = nil
	}

	// Local-only changes that are not pushed but replaced by the repository's
	// version: on request, and with self-heal for changes made outside the
	// web interface.
	var push []string
	var take []Change
	remoteByID := map[string]configstore.Resource{}
	for _, r := range remote {
		remoteByID[r.ID()] = r
	}
	for _, id := range plan.Push {
		l := local[id]
		if restore || (s.SelfHeal && l.Origin == configstore.OriginDetected) {
			ch := Change{ID: id, Action: Pull, BaseUID: l.UID}
			if r, ok := remoteByID[id]; ok {
				ch.Remote, ch.Kind, ch.Key = FromGit(r, l.Payload), r.Kind, r.Key
			} else {
				ch.Remote, ch.Kind, ch.Key, ch.Delete = configstore.Resource{Kind: l.Kind, Key: l.Key, Scope: l.Scope, ScopeID: l.ScopeID}, l.Kind, l.Key, true
			}
			take = append(take, ch)
			continue
		}
		push = append(push, id)
	}

	// Repository changes to apply.
	var apply []Change
	if ss.Hold == nil {
		for _, ch := range plan.Pull {
			if (s.AutoApply || approved) && (!ch.Delete || s.Prune || approved) {
				apply = append(apply, ch)
			} else {
				ss.Pull = append(ss.Pull, ch) // waits for approval
			}
		}
		apply = append(apply, take...)
	} else {
		ss.Pull = append(ss.Pull, plan.Pull...)
	}
	// Conflicts always go to the review screen.
	if err := c.merge(sc, &ss, apply, plan.Conflicts, state.SyncedSHA, hold); err != nil {
		ss.Error = err.Error()
	}
	ss.Conflicts = plan.Conflicts

	// This node's changes to the repository (first: what became of the
	// pull request proposed earlier).
	if state.PRNumber > 0 {
		c.refreshPR(&ss, &state)
	}
	if len(push) > 0 {
		if err := c.push(sc, path, &ss, &state, push, local, remote); err != nil && ss.Error == "" {
			ss.Error = err.Error()
		}
	}
	if len(ss.Push) == 0 && len(ss.Pull) == 0 && len(ss.Conflicts) == 0 && ss.Hold == nil && ss.Error == "" {
		state.SyncedSHA = c.head
		ss.SyncedSHA = c.head
	}
	state.FetchedSHA = c.head
	state.LastError = ss.Error
	if err := saveScope(e.env.DB, sc, state); err != nil && ss.Error == "" {
		ss.Error = err.Error()
	}
	return ss
}

// gitUID is the revision uid of a resource's version in a commit: the same
// on every node that reads it.
func gitUID(sha, id string) string {
	sum := sha256.Sum256([]byte("dplaneos-git\x00" + sha + "\x00" + id))
	h := hex.EncodeToString(sum[:16])
	// Version 4 / variant bits, so it is a well-formed UUID.
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-" + string("89ab"[sum[8]&3]) + h[17:20] + "-" + h[20:32]
}

// merge hands repository changes and conflicts to the merge engine and
// records what was applied.
func (c *cycle) merge(sc string, ss *ScopeStatus, apply, conflicts []Change, good string, hold *Hold) error {
	if len(apply)+len(conflicts) == 0 {
		return nil
	}
	e := c.e
	subject := c.clone.Subject(c.head)
	var revs []configstore.Revision
	uidOf := map[string]Change{}
	for _, ch := range append(append([]Change(nil), apply...), conflicts...) {
		uid := gitUID(c.head, ch.ID)
		r := configstore.Revision{UID: uid, BaseUID: ch.BaseUID, Kind: ch.Kind, Key: ch.Key,
			Scope: ch.Remote.Scope, ScopeID: ch.Remote.ScopeID, Payload: ch.Remote.Payload,
			Author: "git", Note: fmt.Sprintf("commit %.12s: %s", c.head, subject)}
		if ch.Delete {
			r.Payload = nil
		}
		revs = append(revs, r)
		uidOf[uid] = ch
	}
	if err := configstore.StoreGitRevisions(e.env.DB, revs); err != nil {
		return err
	}
	if err := e.env.Syncer.EvaluateGit(); err != nil {
		return err
	}
	uids := make([]string, 0, len(uidOf))
	for u := range uidOf {
		uids = append(uids, u)
	}
	states, err := configstore.GitRevisionStates(e.env.DB, uids)
	if err != nil {
		return err
	}
	applied := map[string]Base{}
	var failed []string
	for uid, ch := range uidOf {
		st := states[uid]
		switch st.Status {
		case "adopted":
			applied[ch.ID] = Base{UID: uid, Hash: Hash(ch.Remote)}
			if ch.Delete {
				applied[ch.ID] = Base{}
			}
			ss.Applied = append(ss.Applied, ch.ID)
		case "blocked", "waiting":
			failed = append(failed, fmt.Sprintf("%s: %s", ch.ID, st.Detail))
		}
	}
	sort.Strings(ss.Applied)
	if err := saveBase(e.env.DB, sc, applied); err != nil {
		return err
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		msg := fmt.Sprintf("applying commit %.12s failed: %s", c.head, strings.Join(failed, "; "))
		ss.Hold = c.hold(sc, msg, good, hold)
		c.status(StatusFailure, msg, c.head)
		return nil
	}
	if len(ss.Applied) > 0 {
		c.status(StatusSuccess, fmt.Sprintf("applied on %s", c.node), c.head)
	}
	return nil
}

// push writes this node's changes of a scope: a commit on the branch
// (direct) or on dplaneos/<scope> with a pull request.
func (c *cycle) push(sc, path string, ss *ScopeStatus, state *scopeState, ids []string, local map[string]configstore.Revision, remote []configstore.Resource) error {
	e, s := c.e, c.s
	if ss.Hold != nil && strings.Contains(ss.Hold.Reason, ErrUnsigned.Error()) {
		ss.Push = ids
		return nil
	}
	// The file: the repository's version, with this node's changes on top.
	byID := map[string]configstore.Resource{}
	for _, r := range remote {
		byID[r.ID()] = r
	}
	for _, id := range ids {
		l := local[id]
		if l.Payload == nil {
			delete(byID, id)
		} else {
			byID[id] = l.Resource()
		}
	}
	rs := make([]configstore.Resource, 0, len(byID))
	for _, r := range byID {
		rs = append(rs, r)
	}
	content, err := Render(rs)
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		content = "" // removes the file
	}
	sum := sha256.Sum256([]byte(content))
	contentHash := hex.EncodeToString(sum[:])
	if s.Policy == PolicyPullRequest && state.Declined == contentHash {
		ss.Push, ss.Declined = ids, true
		return nil // proposed and declined already; changes stay local until changed again or taken back
	}
	id, err := e.identity(s)
	if err != nil {
		return err
	}
	msg := commitMessage(sc, c.node, ids, local)
	sha, err := c.clone.Commit(c.head, map[string]string{path: content}, msg, id)
	if err != nil {
		return err
	}
	pc, err := c.pushRemotes()
	if err != nil {
		return err
	}
	if s.Policy == PolicyPullRequest {
		branch := "dplaneos/" + strings.TrimSuffix(strings.TrimSuffix(path, "/state.yaml"), ".yaml")
		if err := pushedAnywhere(pc.Push(sha, branch, true)); err != nil {
			ss.Push = ids
			return err
		}
		pr, err := c.provider.EnsurePR(branch, s.Branch, prTitle(sc, c.node), msg)
		if err != nil {
			ss.Push = ids
			return fmt.Errorf("opening the pull request: %w", err)
		}
		state.PRNumber, state.PRURL, state.PRState, state.PRBranch = pr.Number, pr.URL, pr.State, branch
		state.PRContent = contentHash
		ss.PR, ss.Push = &pr, ids
		return nil
	}
	results := pc.Push(sha, s.Branch, false)
	if err := pushedAnywhere(results); err != nil {
		ss.Push = ids
		for _, r := range results {
			if r.Rejected {
				e.Trigger() // the branch moved on: fetch and try again
				return fmt.Errorf("the repository changed meanwhile; retrying")
			}
		}
		return err
	}
	pushed := map[string]Base{}
	for _, id := range ids {
		l := local[id]
		if l.Payload == nil {
			pushed[id] = Base{}
		} else {
			pushed[id] = Base{UID: l.UID, Hash: Hash(l.Resource())}
		}
	}
	if err := saveBase(e.env.DB, sc, pushed); err != nil {
		return err
	}
	c.head = sha
	var partial []string
	for _, r := range results {
		if !r.OK {
			partial = append(partial, r.Remote+": "+r.Error)
		}
	}
	if len(partial) > 0 {
		return fmt.Errorf("pushed, but not to every remote (retried next time): %s", strings.Join(partial, "; "))
	}
	return nil
}

func pushedAnywhere(rs []PushResult) error {
	var errs []string
	for _, r := range rs {
		if r.OK {
			return nil
		}
		errs = append(errs, r.Remote+": "+r.Error)
	}
	return fmt.Errorf("push failed: %s", strings.Join(errs, "; "))
}

// refreshPR reads the open pull request's state. Merged: the next fetch
// brings the changes in. Closed without merging: the content is remembered
// and not proposed again.
func (c *cycle) refreshPR(ss *ScopeStatus, state *scopeState) {
	if c.provider == nil {
		return
	}
	pr, err := c.provider.GetPR(state.PRNumber)
	if err != nil {
		ss.Error = "reading the pull request: " + err.Error()
		return
	}
	state.PRState, ss.PR = pr.State, &pr
	switch pr.State {
	case PRMerged:
		state.PRNumber, state.PRURL, state.PRBranch, state.PRContent = 0, "", "", ""
		c.e.Trigger()
	case PRClosed:
		state.Declined = state.PRContent
		state.PRNumber, state.PRURL, state.PRBranch, state.PRContent = 0, "", "", ""
		ss.Declined = true
	}
}

func (c *cycle) status(state, desc, sha string) {
	if c.provider == nil || sha == "" {
		return
	}
	if err := c.provider.SetStatus(sha, state, desc); err != nil {
		log.Printf("GIT: commit status for %.12s: %v", sha, err)
	}
}

func (c *cycle) hold(sc, reason, good string, cur *Hold) *Hold {
	if cur != nil {
		return cur
	}
	h, err := SetHold(c.e.env.DB, sc, reason, c.head, good, c.node)
	if err != nil {
		log.Printf("GIT: setting a hold on %s: %v", sc, err)
		return &Hold{Scope: sc, Reason: reason, CommitSHA: c.head, GoodSHA: good}
	}
	log.Printf("GIT: %s on hold: %s", sc, reason)
	if c.e.env.Alert != nil {
		c.e.env.Alert("warning", "gitops_hold", sc, "Git: "+sc+" on hold: "+reason)
	}
	if c.e.env.Broadcast != nil {
		c.e.env.Broadcast("gitops.hold", h, "warning")
	}
	return h
}

func commitMessage(sc, node string, ids []string, local map[string]configstore.Revision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d change(s) from %s\n\n", sc, len(ids), node)
	for _, id := range ids {
		l := local[id]
		what := "changed"
		if l.Payload == nil {
			what = "deleted"
		}
		who := l.Author
		if who == "" {
			who = l.Origin
		}
		fmt.Fprintf(&b, "- %s %s (%s, %s)\n", id, what, who, l.CreatedAt.Format(time.RFC3339))
	}
	return b.String()
}

func prTitle(sc, node string) string { return fmt.Sprintf("DPlaneOS: %s changes from %s", sc, node) }

// identity is the author of this node's commits; with signing on, its SSH
// key signs them (created on first use).
func (e *Engine) identity(s *Settings) (Identity, error) {
	node := e.env.NodeName()
	id := Identity{Name: "DPlaneOS " + node, Email: "dplaneos@" + node}
	if s.SignCommits {
		if _, err := e.signingKey(); err != nil {
			return id, err
		}
		id.SigningKey = filepath.Join(e.env.Dir, "signing_key")
	}
	return id, nil
}

// signingKey returns this node's public signing key, creating the key pair.
func (e *Engine) signingKey() (string, error) {
	key := filepath.Join(e.env.Dir, "signing_key")
	if _, err := os.Stat(key); err != nil {
		if err := os.MkdirAll(e.env.Dir, 0o700); err != nil {
			return "", err
		}
		out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "dplaneos@"+e.env.NodeName(), "-f", key).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("creating the signing key: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	pub, err := os.ReadFile(key + ".pub")
	return strings.TrimSpace(string(pub)), err
}

// ── Holds ─────────────────────────────────────────────────────────────────────

// Hold stops automatic apply of a scope (section 5.8).
type Hold struct {
	ID        int64     `json:"id"`
	Scope     string    `json:"scope"`
	Reason    string    `json:"reason"`
	CommitSHA string    `json:"commit_sha"`
	GoodSHA   string    `json:"good_sha"`
	Since     time.Time `json:"since"`
	SetBy     string    `json:"set_by"`
}

// OpenHold returns the scope's open hold, or nil.
func OpenHold(db *sql.DB, scope string) (*Hold, error) {
	h := &Hold{}
	err := db.QueryRow(`SELECT id, scope, reason, commit_sha, good_sha, since, set_by FROM config_holds
		WHERE scope = $1 AND cleared_at IS NULL`, scope).Scan(&h.ID, &h.Scope, &h.Reason, &h.CommitSHA, &h.GoodSHA, &h.Since, &h.SetBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return h, err
}

// Holds lists the open holds.
func Holds(db *sql.DB) ([]Hold, error) {
	rows, err := db.Query(`SELECT id, scope, reason, commit_sha, good_sha, since, set_by FROM config_holds
		WHERE cleared_at IS NULL ORDER BY since`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hold{}
	for rows.Next() {
		var h Hold
		if err := rows.Scan(&h.ID, &h.Scope, &h.Reason, &h.CommitSHA, &h.GoodSHA, &h.Since, &h.SetBy); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SetHold puts a scope on hold (one open hold per scope).
func SetHold(db *sql.DB, scope, reason, commit, good, by string) (*Hold, error) {
	h := &Hold{Scope: scope, Reason: reason, CommitSHA: commit, GoodSHA: good, SetBy: by}
	err := db.QueryRow(`INSERT INTO config_holds (scope, reason, commit_sha, good_sha, set_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (scope) WHERE cleared_at IS NULL DO UPDATE SET reason = EXCLUDED.reason
		RETURNING id, since`, scope, reason, commit, good, by).Scan(&h.ID, &h.Since)
	return h, err
}

// ClearHold ends a scope's hold.
func ClearHold(db *sql.DB, scope, resolution, by string) error {
	_, err := db.Exec(`UPDATE config_holds SET cleared_at = NOW(), cleared_by = $3, resolution = $2
		WHERE scope = $1 AND cleared_at IS NULL`, scope, resolution, by)
	return err
}

// Resume clears a hold and retries the scope.
func (e *Engine) Resume(scope, by string) error {
	if err := ClearHold(e.env.DB, scope, "resumed", by); err != nil {
		return err
	}
	return e.Run()
}

// Revert writes the scope's file as it was in the last good commit on top
// of the branch (direct) or proposes it (pull request), then clears the hold.
func (e *Engine) Revert(scope, by string) error {
	e.runMu.Lock()
	h, err := OpenHold(e.env.DB, scope)
	if err != nil || h == nil {
		e.runMu.Unlock()
		if err == nil {
			err = errors.New("this scope is not on hold")
		}
		return err
	}
	err = e.revert(h)
	e.runMu.Unlock()
	if err != nil {
		return err
	}
	if err := ClearHold(e.env.DB, scope, "reverted", by); err != nil {
		return err
	}
	return e.Run()
}

func (e *Engine) revert(h *Hold) error {
	s, err := Load(e.env.DB)
	if err != nil {
		return err
	}
	c := &cycle{e: e, s: s, node: e.env.NodeName()}
	defer func() {
		for _, f := range c.cleanups {
			f()
		}
	}()
	remotes, err := c.remotes()
	if err != nil {
		return err
	}
	c.clone = &Clone{Dir: filepath.Join(e.env.Dir, "clone"), Branch: s.Branch, Remotes: remotes}
	if err := c.clone.Ensure(); err != nil {
		return err
	}
	head, _, err := c.clone.FetchBranch(s.Branch)
	if err != nil {
		return err
	}
	path, err := ScopePath(s.Layout, s.StatePath, h.Scope)
	if err != nil {
		return err
	}
	good, err := c.clone.Show(h.GoodSHA, path)
	if err != nil {
		return err
	}
	id, err := e.identity(s)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("%s: revert to %.12s\n\nCommit %.12s could not be taken on %s: %s", h.Scope, h.GoodSHA, h.CommitSHA, c.node, h.Reason)
	sha, err := c.clone.Commit(head, map[string]string{path: good}, msg, id)
	if err != nil {
		return err
	}
	if sha == head {
		return nil // already as it was
	}
	pc, err := c.pushRemotes()
	if err != nil {
		return err
	}
	if s.Policy == PolicyPullRequest {
		branch := "dplaneos/revert-" + strings.ReplaceAll(h.Scope, ":", "-")
		if err := pushedAnywhere(pc.Push(sha, branch, true)); err != nil {
			return err
		}
		if s.Provider == ProviderNone {
			return nil
		}
		cred, err := LoadCredential(e.env.DB, s.ProviderCred)
		if err != nil {
			return err
		}
		p, err := NewProvider(ProviderConfig{Kind: s.Provider, APIURL: s.ProviderAPIURL, Repo: s.ProviderRepo, Token: cred.Token})
		if err != nil {
			return err
		}
		_, err = p.EnsurePR(branch, s.Branch, "DPlaneOS: revert "+h.Scope, msg)
		return err
	}
	return pushedAnywhere(pc.Push(sha, s.Branch, false))
}

// ── Persistence of scopes and bases ───────────────────────────────────────────

type scopeState struct {
	SyncedSHA, FetchedSHA    string
	PRNumber                 int
	PRURL, PRState, PRBranch string
	PRContent                string // hash of the file proposed in the open pull request
	Declined                 string // hash of a file proposed and declined
	LastError                string
}

func loadScope(db *sql.DB, sc string) (scopeState, error) {
	var st scopeState
	err := db.QueryRow(`SELECT synced_sha, fetched_sha, pr_number, pr_url, pr_state, pr_branch, pr_content, declined, last_error
		FROM git_scopes WHERE scope = $1`, sc).Scan(&st.SyncedSHA, &st.FetchedSHA, &st.PRNumber, &st.PRURL, &st.PRState,
		&st.PRBranch, &st.PRContent, &st.Declined, &st.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	return st, err
}

func saveScope(db *sql.DB, sc string, st scopeState) error {
	_, err := db.Exec(`INSERT INTO git_scopes (scope, synced_sha, synced_at, fetched_sha, fetched_at, pr_number, pr_url, pr_state,
			pr_branch, pr_content, declined, last_error, updated_at)
		VALUES ($1, $2, NOW(), $3, NOW(), $4, $5, $6, $7, $8, $9, $10, NOW())
		ON CONFLICT (scope) DO UPDATE SET
			synced_at = CASE WHEN git_scopes.synced_sha <> EXCLUDED.synced_sha THEN NOW() ELSE git_scopes.synced_at END,
			synced_sha = EXCLUDED.synced_sha, fetched_sha = EXCLUDED.fetched_sha, fetched_at = NOW(),
			pr_number = EXCLUDED.pr_number, pr_url = EXCLUDED.pr_url, pr_state = EXCLUDED.pr_state,
			pr_branch = EXCLUDED.pr_branch, pr_content = EXCLUDED.pr_content, declined = EXCLUDED.declined,
			last_error = EXCLUDED.last_error, updated_at = NOW()`,
		sc, st.SyncedSHA, st.FetchedSHA, st.PRNumber, st.PRURL, st.PRState, st.PRBranch, st.PRContent, st.Declined, st.LastError)
	return err
}

func loadBase(db *sql.DB, sc string) (map[string]Base, error) {
	rows, err := db.Query(`SELECT resource, uid, hash FROM git_base WHERE scope = $1`, sc)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Base{}
	for rows.Next() {
		var id string
		var b Base
		if err := rows.Scan(&id, &b.UID, &b.Hash); err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, rows.Err()
}

// saveBase records bases; an empty Base (absent on both sides) removes the entry.
func saveBase(db *sql.DB, sc string, bases map[string]Base) error {
	if len(bases) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, b := range bases {
		if b.Hash == "" {
			if _, err := tx.Exec(`DELETE FROM git_base WHERE scope = $1 AND resource = $2`, sc, id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(`INSERT INTO git_base (scope, resource, uid, hash) VALUES ($1, $2, $3, $4)
			ON CONFLICT (scope, resource) DO UPDATE SET uid = EXCLUDED.uid, hash = EXCLUDED.hash`, sc, id, b.UID, b.Hash); err != nil {
			return err
		}
	}
	return tx.Commit()
}
