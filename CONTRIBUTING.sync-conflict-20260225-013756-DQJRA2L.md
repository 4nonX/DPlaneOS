# Contributing to D-PlaneOS

Thank you for helping make D-PlaneOS better. This document explains how to contribute code, report bugs, and suggest features.

## Quick Start

```bash
# Prerequisites: Go 1.22+, gcc (for SQLite CGO), make
git clone https://github.com/YOUR_ORG/dplaneos
cd dplaneos

# Build the daemon
cd daemon && go build -o dplaned ./cmd/dplaned && cd ..

# Run locally (will use /tmp/dplaneos-dev.db)
sudo ./daemon/dplaned -db /tmp/dplaneos-dev.db -backup-path "" 

# Frontend: just open app/ in a browser pointed at the daemon
# (serve with any static server: python3 -m http.server 8080)
```

## Project Structure

```
dplaneos/
├── daemon/                     # Go backend
│   ├── cmd/dplaned/            # Entry point (main.go, schema.go, routes)
│   └── internal/
│       ├── handlers/           # HTTP handlers (one file per feature)
│       ├── audit/              # Audit logging
│       ├── cmdutil/            # Safe command execution (timeout-aware)
│       ├── netlinkx/           # Netlink syscalls (no CGO, no ip(8))
│       └── security/           # CSRF, session validation, command whitelist
├── app/                        # Frontend (vanilla HTML/JS/CSS, no build step)
│   ├── pages/                  # One HTML file per page / feature area
│   └── assets/
│       ├── js/core.js          # Shared UI utilities (toast, confirm, csrfFetch)
│       └── css/design-tokens.css
├── nixos/                      # NixOS module + setup scripts
└── nginx-dplaneos.conf         # Reference nginx config
```

## How to Contribute

### Reporting Bugs

1. Search [existing issues](../../issues) first
2. Open a new issue with:
   - D-PlaneOS version (`/api/system/status` → `version`)
   - OS and ZFS version
   - Steps to reproduce
   - What you expected vs what happened
   - Relevant logs from `journalctl -u dplaned -n 100`

### Suggesting Features

Open an issue tagged `enhancement`. Describe:
- The use case (not just the feature)
- How it fits with D-PlaneOS's focus (NAS appliance, not general-purpose Linux)
- Any API/UI mockups if applicable

### Submitting Code

1. **Fork** the repository and create a feature branch: `git checkout -b feat/my-feature`
2. **Follow the conventions below**
3. **Test your change** — run `cd daemon && go test ./...`
4. **Open a PR** against `main` with a clear description

## Coding Conventions

### Backend (Go)

- **One handler file per feature area** — don't add unrelated code to existing files
- **Validate before executing** — all user input must be validated with allowlist patterns before any exec/syscall
- **No shell=true, no fmt.Sprintf for commands** — use `cmdutil.RunFast()` / `cmdutil.RunSlow()`
- **New exec commands** require an entry in `internal/security/whitelist.go` — no exceptions
- **Error handling** — always return a JSON error via `respondErrorSimple()`, never `http.Error()` on API routes
- **Audit logging** — use `audit.LogAction()` for any state-changing operation
- **Tests** — add tests in `_test.go` files; table-driven tests preferred

```go
// Good: validated, audited, error-handled
func (h *MyHandler) DoThing(w http.ResponseWriter, r *http.Request) {
    user := r.Header.Get("X-User")
    var req struct { Name string `json:"name"` }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
        return
    }
    if !isValidName(req.Name) {
        respondErrorSimple(w, "Invalid name", http.StatusBadRequest)
        return
    }
    // ... do the thing ...
    audit.LogAction("mything", user, "Did thing: "+req.Name, true, 0)
    respondJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
```

### Frontend (HTML/JS)

- **No frameworks, no bundlers** — vanilla JS only, runs from static files
- **Always use `csrfFetch()`** for API calls — never raw `fetch()` on mutating requests
- **Always handle errors** — every API call needs a `.catch()` or `try/catch` with a user-facing `showToast()`
- **Refresh UI after mutations** — after create/update/delete, reload the relevant list
- **Use `ui.confirm()`** before destructive actions (delete, reset, disable)
- **Escape user-supplied HTML** — use the `escHtml()` helper, never `innerHTML = userString`
- **Design tokens** — use CSS variables from `design-tokens.css` — no hardcoded colours

```js
// Good: validated, CSRF-protected, error-handled, UI refreshed
async function deleteItem(id, name) {
    if (!await ui.confirm('Delete', `Delete "${name}"? This cannot be undone.`)) return;
    try {
        const r = await csrfFetch('/api/items/' + id, { method: 'DELETE' });
        const d = await r.json();
        if (d.success) { showToast('Deleted', 'success'); loadItems(); }
        else showToast(d.error || 'Failed to delete', 'error');
    } catch(e) {
        showToast('Network error', 'error');
    }
}
```

## Security Requirements for PRs

All PRs touching the backend must:
- [ ] Validate all input with allowlist patterns before use
- [ ] Use parameterized SQL queries (never string concatenation)
- [ ] Not introduce new exec calls outside `cmdutil` + `whitelist.go`
- [ ] Not weaken authentication or authorization checks
- [ ] Include audit logging for state changes

PRs that introduce security issues will be closed without merge regardless of other quality.

## License

By contributing, you agree your contributions are licensed under the project's [PolyForm Shield License 1.0.0](LICENSE).
