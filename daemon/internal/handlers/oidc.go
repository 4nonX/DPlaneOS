package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/oidc"
	"dplaned/internal/secrets"
	"dplaned/internal/security"
)

// OIDCHandler manages the OIDC Authorization Code + PKCE login flow.
type OIDCHandler struct {
	db         *sql.DB
	providerMu sync.RWMutex
	provider   *oidc.Provider
	issuerKey  string
}

func NewOIDCHandler(db *sql.DB) *OIDCHandler {
	return &OIDCHandler{db: db}
}

// oidcConfigRow is the in-memory representation of the oidc_config singleton.
type oidcConfigRow struct {
	Enabled       bool
	Issuer        string
	ClientID      string
	ClientSecret  string
	Scopes        string
	AllowedAlgs   string
	ButtonLabel   string
	AutoProvision bool
	DefaultRoleID sql.NullInt64
	GroupClaim    string
}

func (h *OIDCHandler) loadConfig() (*oidcConfigRow, error) {
	var cfg oidcConfigRow
	var enabledInt, autoProvInt int
	err := h.db.QueryRow(`
		SELECT enabled, issuer, client_id, client_secret,
		       COALESCE(scopes, 'openid email profile'),
		       COALESCE(allowed_algs, 'RS256'),
		       COALESCE(button_label, 'Sign in with SSO'),
		       auto_provision, default_role_id,
		       COALESCE(group_claim, 'groups')
		FROM oidc_config WHERE id = 1
	`).Scan(&enabledInt, &cfg.Issuer, &cfg.ClientID, &cfg.ClientSecret,
		&cfg.Scopes, &cfg.AllowedAlgs, &cfg.ButtonLabel,
		&autoProvInt, &cfg.DefaultRoleID, &cfg.GroupClaim)
	if err != nil {
		return nil, err
	}
	cfg.Enabled = enabledInt == 1
	cfg.AutoProvision = autoProvInt == 1

	plainSecret, openErr := secrets.Open(cfg.ClientSecret)
	if openErr != nil {
		return nil, fmt.Errorf("decrypting client_secret: %w", openErr)
	}
	cfg.ClientSecret = plainSecret

	return &cfg, nil
}

// getProvider returns the cached *oidc.Provider, recreating it only when the
// issuer has changed after a config update.
func (h *OIDCHandler) getProvider(issuer string) (*oidc.Provider, error) {
	h.providerMu.RLock()
	if h.provider != nil && h.issuerKey == issuer {
		p := h.provider
		h.providerMu.RUnlock()
		return p, nil
	}
	h.providerMu.RUnlock()

	h.providerMu.Lock()
	defer h.providerMu.Unlock()
	// Re-check under the write lock.
	if h.provider != nil && h.issuerKey == issuer {
		return h.provider, nil
	}
	p, err := oidc.NewProvider(issuer, nil)
	if err != nil {
		return nil, err
	}
	h.provider = p
	h.issuerKey = issuer
	return p, nil
}

// ─── GET /api/auth/oidc/info ─ public ────────────────────────────────────────
// Returns only the fields that the login page needs to render an SSO button.

func (h *OIDCHandler) Info(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadConfig()
	if err != nil {
		// Not configured or DB error: tell the SPA OIDC is unavailable.
		respondJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"enabled":      cfg.Enabled,
		"button_label": cfg.ButtonLabel,
	})
}

// ─── GET /api/auth/oidc/config ─ protected (system:admin) ────────────────────

func (h *OIDCHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadConfig()
	if err == sql.ErrNoRows {
		respondJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load OIDC config", err)
		return
	}
	var defaultRoleID *int64
	if cfg.DefaultRoleID.Valid {
		defaultRoleID = &cfg.DefaultRoleID.Int64
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"enabled":         cfg.Enabled,
		"issuer":          cfg.Issuer,
		"client_id":       cfg.ClientID,
		"scopes":          cfg.Scopes,
		"allowed_algs":    cfg.AllowedAlgs,
		"button_label":    cfg.ButtonLabel,
		"auto_provision":  cfg.AutoProvision,
		"default_role_id": defaultRoleID,
		"group_claim":     cfg.GroupClaim,
	})
}

// ─── POST /api/auth/oidc/config ─ protected (system:admin) ───────────────────

func (h *OIDCHandler) SaveConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled       bool   `json:"enabled"`
		Issuer        string `json:"issuer"`
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		Scopes        string `json:"scopes"`
		AllowedAlgs   string `json:"allowed_algs"`
		ButtonLabel   string `json:"button_label"`
		AutoProvision bool   `json:"auto_provision"`
		DefaultRoleID *int64 `json:"default_role_id"`
		GroupClaim    string `json:"group_claim"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if req.Scopes == "" {
		req.Scopes = "openid email profile"
	}
	if req.AllowedAlgs == "" {
		req.AllowedAlgs = "RS256"
	}
	if req.ButtonLabel == "" {
		req.ButtonLabel = "Sign in with SSO"
	}
	if req.GroupClaim == "" {
		req.GroupClaim = "groups"
	}

	enabledInt := 0
	if req.Enabled {
		enabledInt = 1
	}
	autoProvInt := 0
	if req.AutoProvision {
		autoProvInt = 1
	}

	var err error
	if req.ClientSecret == "" {
		// Preserve the existing client secret when the field is omitted.
		_, err = h.db.Exec(`
			INSERT INTO oidc_config
			    (id, enabled, issuer, client_id, scopes, allowed_algs,
			     button_label, auto_provision, default_role_id, group_claim, updated_at)
			VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
			ON CONFLICT (id) DO UPDATE SET
			    enabled        = EXCLUDED.enabled,
			    issuer         = EXCLUDED.issuer,
			    client_id      = EXCLUDED.client_id,
			    scopes         = EXCLUDED.scopes,
			    allowed_algs   = EXCLUDED.allowed_algs,
			    button_label   = EXCLUDED.button_label,
			    auto_provision = EXCLUDED.auto_provision,
			    default_role_id = EXCLUDED.default_role_id,
			    group_claim    = EXCLUDED.group_claim,
			    updated_at     = NOW()
		`, enabledInt, req.Issuer, req.ClientID, req.Scopes, req.AllowedAlgs,
			req.ButtonLabel, autoProvInt, req.DefaultRoleID, req.GroupClaim)
	} else {
		sealedSecret, sealErr := secrets.Seal(req.ClientSecret)
		if sealErr != nil {
			respondError(w, http.StatusInternalServerError, "Failed to encrypt client secret", sealErr)
			return
		}
		_, err = h.db.Exec(`
			INSERT INTO oidc_config
			    (id, enabled, issuer, client_id, client_secret, scopes, allowed_algs,
			     button_label, auto_provision, default_role_id, group_claim, updated_at)
			VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
			ON CONFLICT (id) DO UPDATE SET
			    enabled        = EXCLUDED.enabled,
			    issuer         = EXCLUDED.issuer,
			    client_id      = EXCLUDED.client_id,
			    client_secret  = EXCLUDED.client_secret,
			    scopes         = EXCLUDED.scopes,
			    allowed_algs   = EXCLUDED.allowed_algs,
			    button_label   = EXCLUDED.button_label,
			    auto_provision = EXCLUDED.auto_provision,
			    default_role_id = EXCLUDED.default_role_id,
			    group_claim    = EXCLUDED.group_claim,
			    updated_at     = NOW()
		`, enabledInt, req.Issuer, req.ClientID, sealedSecret, req.Scopes,
			req.AllowedAlgs, req.ButtonLabel, autoProvInt, req.DefaultRoleID, req.GroupClaim)
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save OIDC config", err)
		return
	}

	// Invalidate the cached provider when the issuer URL changes.
	h.providerMu.Lock()
	if h.issuerKey != req.Issuer {
		h.provider = nil
		h.issuerKey = ""
	}
	h.providerMu.Unlock()

	actor := getUserFromRequest(r)
	audit.LogAction("oidc", actor, "OIDC configuration updated", true, 0)
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ─── GET /api/auth/oidc/start ─ public ───────────────────────────────────────
// Generates state/nonce/PKCE, stores them, then redirects the browser to the
// IdP authorization endpoint.

func (h *OIDCHandler) Start(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadConfig()
	if err == sql.ErrNoRows || (err == nil && !cfg.Enabled) {
		http.Error(w, "OIDC not configured", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	provider, err := h.getProvider(cfg.Issuer)
	if err != nil {
		log.Printf("OIDC START: provider init error: %v", err)
		http.Error(w, "OIDC provider unavailable", http.StatusBadGateway)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	disco, err := provider.Discovery(ctx)
	if err != nil {
		log.Printf("OIDC START: discovery error: %v", err)
		http.Error(w, "OIDC provider unavailable", http.StatusBadGateway)
		return
	}

	state, err := oidcRandHex(32)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	nonce, err := oidcRandHex(32)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	verifier, err := oidcRandBase64URL(32)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	redirectURI := oidcBuildRedirectURI(r)

	if _, err := h.db.Exec(`
		INSERT INTO oidc_state (state, nonce, pkce_verifier, redirect_uri, expires_at)
		VALUES ($1, $2, $3, $4, NOW() + INTERVAL '10 minutes')
	`, state, nonce, verifier, redirectURI); err != nil {
		log.Printf("OIDC START: state store error: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {cfg.Scopes},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {oidcPKCEChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, disco.AuthorizationEndpoint+"?"+params.Encode(), http.StatusFound)
}

// ─── GET /api/auth/oidc/callback ─ public ────────────────────────────────────
// Receives the IdP redirect, exchanges the code, verifies the token, links or
// provisions the user, mints a session, and redirects the browser back to the
// SPA with a one-time handoff code.

func (h *OIDCHandler) Callback(w http.ResponseWriter, r *http.Request) {
	// Propagate IdP errors to the login page.
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		log.Printf("OIDC CALLBACK: IdP error=%s desc=%s",
			errParam, r.URL.Query().Get("error_description"))
		http.Redirect(w, r, "/login?error=oidc_idp_error", http.StatusFound)
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Redirect(w, r, "/login?error=oidc_missing_params", http.StatusFound)
		return
	}

	// Consume the state row atomically. DELETE...RETURNING prevents replay: if
	// the same state arrives twice the second call gets sql.ErrNoRows.
	var nonce, pkceVerifier, storedRedirectURI string
	err := h.db.QueryRow(`
		DELETE FROM oidc_state
		WHERE state = $1 AND expires_at > NOW()
		RETURNING nonce, pkce_verifier, redirect_uri
	`, state).Scan(&nonce, &pkceVerifier, &storedRedirectURI)
	if err == sql.ErrNoRows {
		log.Printf("OIDC CALLBACK: state not found or expired for %.8s...", state)
		http.Redirect(w, r, "/login?error=oidc_invalid_state", http.StatusFound)
		return
	}
	if err != nil {
		log.Printf("OIDC CALLBACK: state query error: %v", err)
		http.Redirect(w, r, "/login?error=oidc_internal", http.StatusFound)
		return
	}

	cfg, err := h.loadConfig()
	if err != nil {
		log.Printf("OIDC CALLBACK: config load error: %v", err)
		http.Redirect(w, r, "/login?error=oidc_internal", http.StatusFound)
		return
	}
	if !cfg.Enabled {
		log.Printf("OIDC CALLBACK: received callback but OIDC is disabled - rejecting in-flight flow")
		http.Redirect(w, r, "/login?error=oidc_internal", http.StatusFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	rawIDToken, err := h.exchangeCode(ctx, cfg, code, storedRedirectURI, pkceVerifier)
	if err != nil {
		log.Printf("OIDC CALLBACK: token exchange error: %v", err)
		http.Redirect(w, r, "/login?error=oidc_token_exchange", http.StatusFound)
		return
	}

	provider, err := h.getProvider(cfg.Issuer)
	if err != nil {
		log.Printf("OIDC CALLBACK: provider error: %v", err)
		http.Redirect(w, r, "/login?error=oidc_provider", http.StatusFound)
		return
	}

	verifyCfg := oidc.Config{
		Issuer:       cfg.Issuer,
		ClientID:     cfg.ClientID,
		AllowedAlgs:  oidcSplitTrimmed(cfg.AllowedAlgs, ","),
		RequireNonce: true,
		GroupsClaim:  cfg.GroupClaim,
	}
	claims, err := oidc.VerifyIDToken(ctx, provider, verifyCfg, rawIDToken, nonce)
	if err != nil {
		log.Printf("OIDC CALLBACK: token verify error: %v", err)
		http.Redirect(w, r, "/login?error=oidc_invalid_token", http.StatusFound)
		return
	}

	userID, username, err := h.resolveUser(cfg, claims)
	if err != nil {
		log.Printf("OIDC CALLBACK: user resolve error sub=%s: %v", claims.Subject, err)
		http.Redirect(w, r, "/login?error=oidc_user_denied", http.StatusFound)
		return
	}

	// Non-fatal: log but proceed if group-to-role assignment partially fails.
	// Only when the token carries the groups claim (absent = unknown).
	if !claims.GroupsPresent {
		log.Printf("OIDC CALLBACK: no %q claim in the token: roles from the IdP left as they are", cfg.GroupClaim)
	} else if err := h.assignRoles(userID, claims.Groups); err != nil {
		log.Printf("OIDC CALLBACK: role assign warning user=%d: %v", userID, err)
	}

	sessionID, expiresAt, err := h.mintOIDCSession(userID, username, r)
	if err != nil {
		log.Printf("OIDC CALLBACK: session mint error: %v", err)
		http.Redirect(w, r, "/login?error=oidc_internal", http.StatusFound)
		return
	}

	// Store a one-time handoff code so the SPA can pick up the session via POST
	// /api/auth/oidc/exchange without exposing the token in the URL.
	handoffCode, err := oidcRandHex(32)
	if err != nil {
		log.Printf("OIDC CALLBACK: handoff gen error: %v", err)
		http.Redirect(w, r, "/login?error=oidc_internal", http.StatusFound)
		return
	}
	if _, err := h.db.Exec(`
		INSERT INTO oidc_handoff (code, session_id, expires_at)
		VALUES ($1, $2, NOW() + INTERVAL '2 minutes')
	`, handoffCode, sessionID); err != nil {
		log.Printf("OIDC CALLBACK: handoff insert error: %v", err)
		http.Redirect(w, r, "/login?error=oidc_internal", http.StatusFound)
		return
	}

	audit.LogAction("oidc", username, fmt.Sprintf("OIDC login via %s", cfg.Issuer), true, 0)
	log.Printf("OIDC AUTH OK: user=%s exp=%d", username, expiresAt)
	http.Redirect(w, r, "/login?oidc_handoff="+handoffCode, http.StatusFound)
}

// ─── POST /api/auth/oidc/exchange ─ public ───────────────────────────────────
// The SPA calls this after detecting oidc_handoff in the URL to exchange the
// one-time code for a real session token, identical in shape to a normal login
// response.

func (h *OIDCHandler) Exchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HandoffCode string `json:"handoff_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.HandoffCode == "" {
		respondError(w, http.StatusBadRequest, "handoff_code required", nil)
		return
	}

	// Delete the handoff row atomically; prevents double-use.
	var sessionID string
	err := h.db.QueryRow(`
		DELETE FROM oidc_handoff
		WHERE code = $1 AND expires_at > NOW()
		RETURNING session_id
	`, req.HandoffCode).Scan(&sessionID)
	if err == sql.ErrNoRows {
		respondError(w, http.StatusUnauthorized, "Invalid or expired handoff code", nil)
		return
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Internal error", err)
		return
	}

	var username string
	var expiresAt int64
	err = h.db.QueryRow(`
		SELECT username, COALESCE(expires_at, 0)
		FROM sessions
		WHERE session_id = $1 AND (expires_at IS NULL OR expires_at > $2)
	`, sessionID, time.Now().Unix()).Scan(&username, &expiresAt)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "Session not found", nil)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"session_id": sessionID,
		"username":   username,
		"expires_at": expiresAt,
	})
}

// StartOIDCCleanup launches a background goroutine that removes expired
// oidc_state and oidc_handoff rows hourly. Call once from main() at startup.
func StartOIDCCleanup(db *sql.DB) {
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			db.Exec(`DELETE FROM oidc_state WHERE expires_at < NOW()`)
			db.Exec(`DELETE FROM oidc_handoff WHERE expires_at < NOW()`)
		}
	}()
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// exchangeCode posts the authorization code to the IdP's token endpoint using
// PKCE and returns the raw id_token string.
func (h *OIDCHandler) exchangeCode(ctx context.Context, cfg *oidcConfigRow, code, redirectURI, pkceVerifier string) (string, error) {
	provider, err := h.getProvider(cfg.Issuer)
	if err != nil {
		return "", err
	}
	disco, err := provider.Discovery(ctx)
	if err != nil {
		return "", err
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {cfg.ClientID},
		"code_verifier": {pkceVerifier},
	}
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, disco.TokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("token response read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token endpoint %d: %s", resp.StatusCode, body)
	}

	var tokenResp struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("token response decode: %w", err)
	}
	if tokenResp.Error != "" {
		return "", fmt.Errorf("IdP token error: %s", tokenResp.Error)
	}
	if tokenResp.IDToken == "" {
		return "", fmt.Errorf("missing id_token in token response")
	}
	return tokenResp.IDToken, nil
}

// resolveUser finds or creates the local user for the given OIDC claims.
// Priority: (1) existing oidc_identity link by issuer+subject, (2) email-based
// link to an existing local account, (3) auto-provision a new account.
func (h *OIDCHandler) resolveUser(cfg *oidcConfigRow, claims *oidc.Claims) (int64, string, error) {
	// 1. Exact identity match: issuer + subject is the stable, authoritative key.
	var userID int64
	var username string
	err := h.db.QueryRow(`
		SELECT u.id, u.username
		FROM oidc_identities oi
		JOIN users u ON u.id = oi.user_id
		WHERE oi.issuer = $1 AND oi.subject = $2 AND u.active = 1
	`, claims.Issuer, claims.Subject).Scan(&userID, &username)
	if err == nil {
		h.db.Exec(`UPDATE oidc_identities SET last_login = NOW(), email = $1
			WHERE issuer = $2 AND subject = $3`,
			claims.Email, claims.Issuer, claims.Subject)
		return userID, username, nil
	}
	if err != sql.ErrNoRows {
		return 0, "", fmt.Errorf("identity lookup: %w", err)
	}

	// 2. Email-based linking: link the IdP identity to an existing account that
	// shares the same email, only when the IdP says the email is verified.
	// Without that check anyone who could set an unverified email at the IdP
	// (self-registration) took over the local account with that email.
	// Email is informational after this point; future logins match via the
	// identity row created here.
	if claims.Email != "" && claims.EmailVerified {
		var existID int64
		var existName string
		err := h.db.QueryRow(`SELECT id, username FROM users WHERE email = $1 AND active = 1 LIMIT 1`,
			claims.Email).Scan(&existID, &existName)
		if err == nil {
			if _, err := h.db.Exec(`
				INSERT INTO oidc_identities (issuer, subject, user_id, email, last_login)
				VALUES ($1, $2, $3, $4, NOW())
				ON CONFLICT (issuer, subject) DO UPDATE
				    SET user_id = $3, email = $4, last_login = NOW()
			`, claims.Issuer, claims.Subject, existID, claims.Email); err != nil {
				return 0, "", fmt.Errorf("identity link: %w", err)
			}
			return existID, existName, nil
		}
		if err != sql.ErrNoRows {
			return 0, "", fmt.Errorf("email lookup: %w", err)
		}
	}

	// 3. Auto-provision a new local account.
	if !cfg.AutoProvision {
		return 0, "", fmt.Errorf("no matching account and auto-provision is disabled")
	}

	newName := h.uniquifyUsername(deriveOIDCUsername(claims))
	displayName := claims.Name
	if displayName == "" {
		displayName = newName
	}

	var newID int64
	if err := h.db.QueryRow(`
		INSERT INTO users (username, password_hash, display_name, email, source, active)
		VALUES ($1, '', $2, $3, 'oidc', 1)
		RETURNING id
	`, newName, displayName, claims.Email).Scan(&newID); err != nil {
		return 0, "", fmt.Errorf("user create: %w", err)
	}

	if _, err := h.db.Exec(`
		INSERT INTO oidc_identities (issuer, subject, user_id, email, last_login)
		VALUES ($1, $2, $3, $4, NOW())
	`, claims.Issuer, claims.Subject, newID, claims.Email); err != nil {
		return 0, "", fmt.Errorf("identity insert: %w", err)
	}

	// Assign the configured default role to the new account.
	if cfg.DefaultRoleID.Valid {
		h.db.Exec(`
			INSERT INTO user_roles (user_id, role_id, granted_by)
			VALUES ($1, $2, 'oidc-default')
			ON CONFLICT (user_id, role_id) DO NOTHING
		`, newID, cfg.DefaultRoleID.Int64)
	}

	log.Printf("OIDC: auto-provisioned user %q (id=%d) sub=%s iss=%s",
		newName, newID, claims.Subject, claims.Issuer)
	return newID, newName, nil
}

// assignRoles makes the user's IdP-granted roles match the IdP groups: a
// group named like a role grants it, and a role the IdP granted earlier is
// revoked when the group is gone (before, roles were only ever added, so
// removing someone from the admin group at the IdP changed nothing).
// Roles granted locally or as the default role are left alone.
func (h *OIDCHandler) assignRoles(userID int64, groups []string) error {
	want := []int64{}
	for _, group := range groups {
		var roleID int64
		err := h.db.QueryRow(`SELECT id FROM roles WHERE name = $1`, group).Scan(&roleID)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		want = append(want, roleID)
		if _, err := h.db.Exec(`
			INSERT INTO user_roles (user_id, role_id, granted_by)
			VALUES ($1, $2, 'oidc-provider')
			ON CONFLICT (user_id, role_id) DO NOTHING
		`, userID, roleID); err != nil {
			log.Printf("OIDC: role assign warn user=%d role=%d: %v", userID, roleID, err)
		}
	}
	res, err := h.db.Exec(`DELETE FROM user_roles WHERE user_id = $1 AND granted_by = 'oidc-provider' AND NOT (role_id = ANY($2::bigint[]))`,
		userID, pgInt64Array(want))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("OIDC: revoked %d role(s) of user %d no longer granted by the IdP's groups", n, userID)
	}
	return nil
}

// mintOIDCSession inserts a new active session row and returns (sessionID,
// expiresAt, error). The shape exactly mirrors the regular Login path.
func (h *OIDCHandler) mintOIDCSession(userID int64, username string, r *http.Request) (string, int64, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", 0, err
	}
	sessionID := fmt.Sprintf("%x", raw)
	now := time.Now().Unix()
	expiresAt := time.Now().Add(24 * time.Hour).Unix()

	_, err := h.db.Exec(`
		INSERT INTO sessions
		    (session_id, user_id, username, ip_address, user_agent, created_at, expires_at, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'active')
	`, sessionID, userID, username, security.RealIP(r), r.UserAgent(), now, expiresAt)
	if err != nil {
		return "", 0, err
	}
	h.db.Exec(`UPDATE users SET last_login = NOW() WHERE id = $1`, userID)
	return sessionID, expiresAt, nil
}

// uniquifyUsername appends an incrementing counter to base until a name that
// does not already exist in the users table is found.
func (h *OIDCHandler) uniquifyUsername(base string) string {
	name := base
	for i := 2; i <= 99; i++ {
		var count int
		h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE username = $1`, name).Scan(&count)
		if count == 0 {
			return name
		}
		name = fmt.Sprintf("%s%d", base, i)
	}
	return name
}

// ─── Package-level pure helpers ───────────────────────────────────────────────

var oidcUnsafeChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// deriveOIDCUsername picks a safe login name from OIDC claims, preferring
// preferred_username, then the local part of email, then subject.
func deriveOIDCUsername(claims *oidc.Claims) string {
	s := claims.PreferredUsername
	if s == "" && claims.Email != "" {
		s = strings.SplitN(claims.Email, "@", 2)[0]
	}
	if s == "" {
		s = claims.Subject
	}
	s = oidcUnsafeChars.ReplaceAllString(s, "_")
	if len(s) > 64 {
		s = s[:64]
	}
	if s == "" {
		return "oidc_user"
	}
	return s
}

func oidcRandHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

func oidcRandBase64URL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// oidcPKCEChallenge computes the S256 code challenge from a PKCE verifier.
func oidcPKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// oidcBuildRedirectURI constructs the absolute callback URL from the current
// request, using X-Forwarded-Proto when the daemon sits behind a reverse proxy.
func oidcBuildRedirectURI(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/api/auth/oidc/callback"
}

// oidcSplitTrimmed splits s by sep and trims whitespace from each part.
func oidcSplitTrimmed(s, sep string) []string {
	parts := strings.Split(s, sep)
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}
