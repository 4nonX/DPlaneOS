# Contributing to DPlaneOS

Thank you for helping make DPlaneOS better. This document explains how to contribute code, report bugs, and suggest features.

## Quick Start

```bash
# Prerequisites: Go (version in daemon/go.mod), Node.js 20+, PostgreSQL 15+;
# or `nix develop`, which provides all of them
git clone https://github.com/4nonX/DPlaneOS
cd DPlaneOS

# Build the daemon (static; see docs/reference/DEPENDENCIES.md for the libzfs build)
cd daemon && CGO_ENABLED=0 go build -mod=vendor -o dplaned ./cmd/dplaned && cd ..

# Run locally on TCP port 9000 against a local PostgreSQL
sudo ./daemon/dplaned -listen 127.0.0.1:9000   -db-dsn "postgres://dplaneos:dplaneos@localhost/dplaneos?sslmode=disable"

# Frontend dev server (hot reload, proxies /api and /ws to 127.0.0.1:9000)
cd app-react
npm install
npm run dev
```

## Project Structure

```
DPlaneOS/
├── daemon/                     # Go backend
│   ├── cmd/dplaned/            # Entry point (main.go, schema.go seed data, routes)
│   └── internal/
│       ├── handlers/           # HTTP handlers (one file per feature area)
│       ├── jobs/               # Async job store (in-memory, ephemeral)
│       ├── audit/              # Audit logging (buffered, HMAC chain)
│       ├── cmdutil/            # Safe exec.Command wrappers (timeout-aware)
│       ├── database/           # Schema migrations (goose) + SQL helpers
│       │   └── migrations/     # Versioned .sql migration files (goose format)
│       ├── netlinkx/           # Netlink syscalls (no CGO, no ip(8))
│       └── security/           # CSRF, session validation, command whitelist
├── app/                        # Built frontend (output of `npm run build`)
│   ├── index.html              # SPA entry point
│   └── assets/                 # Vite-built JS/CSS bundles + self-hosted fonts
├── app-react/                  # Frontend source (React 19 + TypeScript + Vite)
│   └── src/
│       ├── pages/              # One file per page
│       ├── routes/             # TanStack Router route tree (index.tsx)
│       ├── components/         # Shared components (layout, ui/)
│       ├── stores/             # Zustand stores (auth, websocket)
│       ├── hooks/              # Shared hooks (useJob, useToast, etc.)
│       └── lib/api.ts          # Typed API client (CSRF, session, 401 redirect)
├── docs/                       # All documentation
├── install/                    # System files for installs outside NixOS (install.sh copies them to /opt/dplaneos)
│   ├── config/                 # Default configuration templates
│   ├── scripts/                # Operational and install-time scripts
│   ├── systemd/                # systemd unit files
│   ├── udev/                   # udev rules (hot-swap, removable media)
│   ├── zed/                    # ZED hook for real-time ZFS event notification
│   └── nginx-dplaneos.conf     # Reference nginx configuration
└── nixos/                      # The NixOS appliance: module, configurations, installer, VM tests (nixos/tests)
```

## How to Contribute

### Reporting Bugs

1. Search [existing issues](../../issues) first
2. Open a new issue with:
   - DPlaneOS version (`/api/system/status` → `version`)
   - OS and ZFS version
   - Steps to reproduce
   - What you expected vs what happened
   - Relevant logs from `journalctl -u dplaned -n 100`

### Suggesting Features

Open an issue tagged `enhancement`. Describe:
- The use case, not just the feature
- How it fits with DPlaneOS's focus (NAS appliance, not general-purpose Linux)
- API or UI mockups if applicable

### Submitting Code

1. Fork the repository and create a feature branch: `git checkout -b feat/my-feature`
2. Follow the conventions below
3. Test your change:
   - Go: `cd daemon && go test ./...` (tests that need a database skip without `DATABASE_DSN`, e.g. `postgres://dplaneos:dplaneos@localhost/dplaneos?sslmode=disable`)
   - Frontend: `cd app-react && npm run typecheck && npm run lint && npx playwright test`; commit the rebuilt `app/` (`npm run build`), CI checks it is up to date
   - NixOS: the VM tests, `nix build .#checks.x86_64-linux.<name> -L` (list: `nix flake show`)
4. Open a PR against `main` with a clear description

## Coding Conventions

### Backend (Go)

- **One handler file per feature area** - do not add unrelated code to existing files
- **Validate before executing** - all user input must pass allowlist validation before any exec or syscall
- **No `shell=true`, no `fmt.Sprintf` for commands** - use `cmdutil.RunFast()` / `cmdutil.RunSlow()`
- **New exec commands** require an entry in `internal/security/whitelist.go` - no exceptions
- **Error handling** - return JSON errors via `respondErrorSimple()`, never `http.Error()` on API routes
- **Audit logging** - use `audit.LogAction()` for any state-changing operation
- **Long-running operations** - use `jobs.Start()` and return the job ID immediately; never block the HTTP connection
- **Tests** - add tests in `_test.go` files; table-driven tests preferred

```go
// Correct: validated, audited, error-handled
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

// Correct: long-running operation returned as async job
func (h *MyHandler) DoSlowThing(w http.ResponseWriter, r *http.Request) {
    jobID := jobs.Start("slow_thing", func(j *jobs.Job) {
        output, err := cmdutil.RunSlow("zfs", "send", "-R", snapshot)
        if err != nil { j.Fail(err.Error()); return }
        j.Done(map[string]interface{}{"output": string(output)})
    })
    respondJSON(w, http.StatusAccepted, map[string]interface{}{"job_id": jobID})
}
```

### Frontend (TypeScript / React)

- **TanStack Query for all data fetching** - use `useQuery` / `useMutation`, not raw `fetch`
- **Always use `api.get/post/put/delete`** from `src/lib/api.ts` - handles CSRF, session headers, and 401 redirect
- **Long-running mutations** - use the `useJob` hook to poll `GET /api/jobs/{id}` after receiving a `job_id`
- **Error handling** - every mutation needs an `onError` handler with a `toast.error()` call
- **User feedback** - use `toast` from `useToast()`; never use `alert()`
- **Icons** - use `<Icon name="..." />` from `src/components/ui/Icon.tsx` (Material Symbols Rounded)
- **Styling** - use CSS variables; no hardcoded colours

## Building for Release

```bash
# Build frontend into app/
cd app-react && npm run build && cd ..

# Build daemon binary
cd daemon
go build -mod=vendor \
  -ldflags "-s -w -X main.Version=$(cat ../VERSION)" \
  -o ../build/dplaned ./cmd/dplaned/
```

### Release procedure

Changelog entries are written during development, under `## Unreleased` in [docs/reference/CHANGELOG.md](docs/reference/CHANGELOG.md).

1. On the development branch, commit `release: vX.Y.Z "Name"`: set `VERSION` to `X.Y.Z` and rename `## Unreleased` to `## vX.Y.Z (YYYY-MM-DD) - "Name"`.
2. Merge into `main`. CI builds and signs every asset (tarballs, installer and live ISOs, witness images, SBOMs) and creates a **draft** release named after the changelog heading, with that section as its notes.
3. Check the draft: CI runs every test before it drafts a release; download the draft's ISOs, check their `.sha256`, boot the live ISO and install from the installer ISO. Then publish it on the commit CI built:
   ```bash
   gh release edit vX.Y.Z --target <main commit> --draft=false --latest
   ```
   Publishing creates the tag.

A published release is never rebuilt: CI skips a version that is already published, so a fix needs a new version.

## Security Requirements for PRs

All PRs touching the backend must:

- [ ] Validate all input with allowlist patterns before use
- [ ] Use parameterized SQL queries (never string concatenation)
- [ ] Not introduce new exec calls outside `cmdutil` + `whitelist.go`
- [ ] Not weaken authentication or authorization checks
- [ ] Include audit logging for state changes
- [ ] Use `jobs.Start()` for any operation that may take more than a few seconds

PRs that introduce security regressions will be closed without merge.

## License

By contributing, you agree your contributions are licensed under the project's [GNU Affero General Public License v3.0 (AGPLv3)](https://www.gnu.org/licenses/agpl-3.0.html). See `docs/legal/CLA-INDIVIDUAL.md` for details.

