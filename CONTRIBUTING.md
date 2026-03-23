# Contributing to D-PlaneOS

Thank you for helping make D-PlaneOS better. This document explains how to contribute code, report bugs, and suggest features.

## Quick Start

```bash
# Prerequisites: Go 1.25+, gcc (for ZFS interop), Node.js 20+, make, PostgreSQL
git clone https://github.com/4nonX/D-PlaneOS
cd D-PlaneOS

# Build the daemon
cd daemon && go build -o dplaned ./cmd/dplaned && cd ..

# Run locally (uses local PostgreSQL)
sudo ./daemon/dplaned -backup-path ""

# Frontend dev server (hot reload, proxies API to daemon)
cd app-react
npm install
npm run dev
```

## Project Structure

```
D-PlaneOS/
├── daemon/                     # Go backend
│   ├── cmd/dplaned/            # Entry point (main.go, schema.go, routes)
│   └── internal/
│       ├── handlers/           # HTTP handlers (one file per feature area)
│       ├── jobs/               # Async job store (in-memory, ephemeral)
│       ├── audit/              # Audit logging (buffered, HMAC chain)
│       ├── cmdutil/            # Safe exec.Command wrappers (timeout-aware)
│       ├── netlinkx/           # Netlink syscalls (no CGO, no ip(8))
│       └── security/           # CSRF, session validation, command whitelist
├── app/                        # Built frontend (output of `npm run build`)
│   ├── index.html              # SPA entry point
│   └── assets/                 # Vite-built JS/CSS bundles + self-hosted fonts
├── app-react/                  # Frontend source (React 19 + TypeScript + Vite)
│   └── src/
│       ├── routes/             # TanStack Router pages (one file per page)
│       ├── components/         # Shared components (layout, ui/)
│       ├── stores/             # Zustand stores (auth, websocket)
│       ├── hooks/              # Shared hooks (useJob, useToast, etc.)
│       └── lib/api.ts          # Typed API client (CSRF, session, 401 redirect)
├── docs/                       # All documentation
├── install/                    # Install-time system files (copied to /opt/dplaneos/install/)
│   ├── config/                 # Default configuration templates
│   ├── scripts/                # Operational and install-time scripts
│   ├── systemd/                # systemd unit files
│   ├── udev/                   # udev rules (hot-swap, removable media)
│   ├── zed/                    # ZED hook for real-time ZFS event notification
│   └── nginx-dplaneos.conf     # Reference nginx configuration
└── nixos/                      # NixOS module and setup scripts
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
- The use case, not just the feature
- How it fits with D-PlaneOS's focus (NAS appliance, not general-purpose Linux)
- API or UI mockups if applicable

### Submitting Code

1. Fork the repository and create a feature branch: `git checkout -b feat/my-feature`
2. Follow the conventions below
3. Test your change: `cd daemon && go test ./...`
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

By contributing, you agree your contributions are licensed under the project's [GNU Affero General Public License v3.0 (AGPLv3)](https://www.gnu.org/licenses/agpl-3.0.html). See `CLA-INDIVIDUAL.md` for details.

