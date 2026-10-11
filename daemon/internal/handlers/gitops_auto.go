package handlers

// Applying new commits by themselves.
//
// Off by default. When it is on, a commit that arrives in the repository
// (found by the regular check every five minutes, or at once when the Git
// server calls the webhook) is applied without anybody pressing Apply, if
// its plan is one that needs no judgement: it only creates and changes
// things, and none of the changes is marked high risk. Anything else is
// held for the operator, who is told why: deletions, items that need an
// approval, ambiguous states, manual steps, high-risk changes (network
// addresses, host name).
//
// A commit is tried once. If it was held or failed, nothing more happens
// until the operator applies it on the GitOps page or the next commit
// arrives. Changes made to the live system outside Git are not reverted by
// this (the check reports them as drift; they are the operator's to judge).

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"dplaned/internal/gitops"
	"dplaned/internal/secrets"
)

const (
	EventGitOpsApplied     = "gitops.applied"
	EventGitOpsApplyHeld   = "gitops.apply_held"
	EventGitOpsApplyFailed = "gitops.apply_failed"
)

// autoApplyVerdict says whether a plan may be applied without an operator,
// and if not, why (one line per reason, at most a handful).
func autoApplyVerdict(plan *gitops.Plan) (ok bool, reasons []string) {
	add := func(format string, a ...any) {
		if len(reasons) < 8 {
			reasons = append(reasons, fmt.Sprintf(format, a...))
		}
	}
	for _, it := range plan.Items {
		id := fmt.Sprintf("%s %s", it.Kind, it.Name)
		switch it.Action {
		case gitops.ActionNOP:
		case gitops.ActionCreate, gitops.ActionModify:
			switch {
			case it.Kind == gitops.KindPool:
				// Creating a pool takes disks; changing one is not undone.
				add("%s: pools are not created or changed automatically", id)
			case it.RiskLevel == "high" || it.RiskLevel == "critical":
				add("%s: a %s-risk change (%s)", id, it.RiskLevel, strings.Join(it.Changes, "; "))
			}
		case gitops.ActionBlocked:
			add("%s: needs an approval (%s)", id, it.BlockReason)
		case gitops.ActionAmbiguous:
			add("%s: the system is in a state that has to be resolved by hand (%s)", id, it.BlockReason)
		default:
			// Deletions, manual steps, and anything this list does not know.
			add("%s: %s is not applied automatically", id, strings.ToLower(string(it.Action)))
		}
	}
	return len(reasons) == 0, reasons
}

func (h *GitOpsHandler) recordAuto(commit, outcome, message string) {
	if _, err := h.db.Exec(`UPDATE gitops_config SET last_auto_commit = $1, last_auto_result = $2, last_auto_message = $3,
		last_auto_at = NOW() WHERE id = 1`, commit, outcome, message); err != nil {
		log.Printf("GITOPS AUTO: recording the outcome: %v", err)
	}
}

// autoApply runs after every check of the repository.
func (h *GitOpsHandler) autoApply(result *gitops.DriftResult) {
	if result == nil || result.Error != "" || result.Plan == nil {
		return
	}
	var auto int
	var lastCommit string
	if err := h.db.QueryRow(`SELECT auto_apply, last_auto_commit FROM gitops_config WHERE id = 1 AND enabled = 1`).Scan(&auto, &lastCommit); err != nil || auto != 1 {
		return
	}
	repoDir := filepath.Dir(h.stateYAMLPath)
	head, err := gitops.HeadCommit(repoDir)
	if err != nil || head == "" {
		log.Printf("GITOPS AUTO: the commit of the repository cannot be read: %v", err)
		return
	}
	// Only commits the live system has not seen yet, and each of them once.
	if head == gitops.SyncedCommit(h.db) || head == lastCommit {
		return
	}
	if ok, _ := gitops.IsWriter(); !ok {
		return
	}
	if !gitops.TryLock() {
		return // an apply is running; the next check looks again
	}
	defer gitops.Unlock()

	short := head
	if len(short) > 12 {
		short = short[:12]
	}
	desired, err := h.loadDesiredState()
	if err != nil {
		h.recordAuto(head, "failed", "state.yaml: "+err.Error())
		DispatchAlert("critical", EventGitOpsApplyFailed, short, fmt.Sprintf("Commit %s was not applied: state.yaml is not valid: %v", short, err))
		return
	}
	live, err := gitops.ReadLiveState(h.db)
	if err != nil {
		return // could not look: the next check tries again
	}
	plan := gitops.ComputeDiff(desired, live, &gitops.DiffContext{DB: h.db})
	h.stampApprovals(plan)

	if ok, reasons := autoApplyVerdict(plan); !ok {
		msg := strings.Join(reasons, "\n")
		h.recordAuto(head, "held", msg)
		log.Printf("GITOPS AUTO: commit %s held: %s", short, strings.Join(reasons, " | "))
		DispatchAlert("warning", EventGitOpsApplyHeld, short,
			fmt.Sprintf("Commit %s was not applied automatically; review and apply it on the GitOps page.\n%s", short, msg))
		return
	}

	res, applyErr := gitops.ApplyPlan(gitops.ApplyContext{
		DB: h.db, SmbConfPath: h.smbConfPath, OwnershipGuard: h.getOwnershipGuard(),
	}, plan, desired)
	if applyErr != nil {
		failed := ""
		if res != nil {
			failed = res.Failed
		}
		h.recordAuto(head, "failed", applyErr.Error())
		log.Printf("GITOPS AUTO: commit %s failed at %s: %v", short, failed, applyErr)
		DispatchAlert("critical", EventGitOpsApplyFailed, short,
			fmt.Sprintf("Applying commit %s failed (%s): %v. Nothing more is applied automatically until this is resolved on the GitOps page or a new commit arrives.", short, failed, applyErr))
		return
	}
	if gitops.FullyApplied(res) {
		if err := gitops.RecordApplied(h.db, repoDir); err != nil {
			log.Printf("GITOPS AUTO: %v", err)
		}
	}
	n := 0
	if res != nil {
		n = len(res.Applied)
	}
	h.recordAuto(head, "applied", fmt.Sprintf("%d changes", n))
	log.Printf("GITOPS AUTO: commit %s applied (%d changes)", short, n)
	DispatchAlert("info", EventGitOpsApplied, short, fmt.Sprintf("Commit %s was applied automatically (%d changes).", short, n))
}

// webhookSignatureOK checks a Git server's webhook call against the shared
// secret: GitHub, Gitea and Forgejo sign the body (HMAC-SHA256), GitLab
// sends the secret itself in a header.
func webhookSignatureOK(secret string, body []byte, hdr http.Header) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if v := hdr.Get("X-Hub-Signature-256"); v != "" {
		return hmac.Equal([]byte(strings.TrimPrefix(strings.ToLower(v), "sha256=")), []byte(want))
	}
	if v := hdr.Get("X-Gitea-Signature"); v != "" {
		return hmac.Equal([]byte(strings.ToLower(v)), []byte(want))
	}
	if v := hdr.Get("X-Gitlab-Token"); v != "" {
		return subtle.ConstantTimeCompare([]byte(v), []byte(secret)) == 1
	}
	return false
}

// Webhook: POST /api/gitops/webhook (public; the signature is the key)
// A Git server calls it after a push: the repository is looked at now
// instead of at the next regular check.
func (h *GitOpsHandler) Webhook(w http.ResponseWriter, r *http.Request) {
	var sealed string
	var enabled int
	if err := h.db.QueryRow(`SELECT enabled, webhook_secret FROM gitops_config WHERE id = 1`).Scan(&enabled, &sealed); err != nil && err != sql.ErrNoRows {
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}
	secret, err := secrets.Open(sealed)
	if enabled != 1 || sealed == "" || err != nil || secret == "" {
		respondErrorSimple(w, "Not found", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil || !webhookSignatureOK(secret, body, r.Header) {
		respondErrorSimple(w, "Signature missing or wrong", http.StatusUnauthorized)
		return
	}
	go h.detector.CheckNow()
	respondJSON(w, http.StatusAccepted, map[string]any{"success": true, "message": "Looking at the repository"})
}
