# Contributing to DPlaneOS

How to build, test and change DPlaneOS, and what a change has to bring with it.

## The rule that comes first

**What the interface and the documentation say must be true.** A feature is done when it works end to end on an installed system and a test shows it. An endpoint without a way to reach it in the interface, a setting that is saved but not applied, a status that is not read from the system, a "best effort" that swallows its error: these are defects, not partial features. If something cannot be finished, it is left out and said to be missing.

## Layout

```
DPlaneOS/
├── daemon/                  the Go daemon
│   ├── cmd/dplaned/         entry point, routes, background loops
│   └── internal/            handlers/ (the API, one file per feature) and the
│                            packages behind them: see docs/development/CODEBASE-DIAGRAM.md
├── app-react/               the web interface (React 19, TypeScript, Vite)
├── app/                     the built interface, committed; this is what images contain
├── nixos/                   the system: modules, installer, update, images, VM tests
├── flake.nix, flake.lock    how everything is built, and from what
├── scripts/                 release tooling
└── docs/                    documentation
```

## Building

With Nix (the same way CI and releases build):

```bash
nix develop                          # a shell with the pinned Go and Node.js
nix build .#iso                      # the installer image
nix build .#checks.x86_64-linux.appliance -L    # a VM test; see nixos/README.md for all of them
```

By hand:

```bash
cd daemon && CGO_ENABLED=0 go build -mod=vendor ./cmd/dplaned/
cd app-react && npm ci && npm run build      # writes ../app/
```

`app/` is committed. After changing `app-react/`, rebuild and commit `app/` with the change: CI fails when they differ.

## Testing

```bash
# Go: tests that need a database are skipped without DATABASE_DSN
cd daemon
DATABASE_DSN="postgres://user@localhost:5432/postgres?sslmode=disable" go test -race ./...

# Interface
cd app-react && npx tsc --noEmit -p . && npm run lint
```

What the layers are for:

- **Unit tests** for logic and for the handlers. Handler tests run through the command allowlist: a command the handler builds that the allowlist would refuse fails the test.
- **A SQL test** prepares every SQL statement in the code against the migrated schema.
- **A contract test** checks that the interface calls routes that exist, with the fields they take.
- **VM tests** (`nixos/tests/`) start real systems and use them through the API, from a second machine where a client matters. A feature that touches the system (a service, the kernel, the network, another node) needs one: that is where it is shown to work.

## Conventions

### Daemon

- **Validate, then act.** Input from a request is checked before it reaches a command, a path or a query.
- **No shell.** Programs are run with their arguments as a list. Anything whose arguments come from a request goes through `cmdutil` with an allowlist entry (`internal/security/whitelist.go`): a new program or a new form of arguments needs an entry, with a test. Nothing from a request goes into a unit file, a configuration file or a command line without being validated for that place.
- **Every route has a permission** (`permRoute`); routes without one are the few that must be public, each with its own check.
- **A change that fails is taken back.** If a setting is saved and then applied, a failed apply restores what was there, and the error says what the system said.
- **Errors are reported, not logged and forgotten.** If part of an operation fails, the response says so.
- **Long operations are jobs** (`jobs.Start`), and run as long as they take: no fixed timeout on a transfer or a rebuild.
- **Changes are audited** (`audit.LogActivity`), without secrets in the entry.
- **SQL is parameterised**, and a schema change is a new migration in `internal/database/migrations/`.

### Interface

- Data through TanStack Query and `api.get/post/put/delete` from `src/lib/api.ts`.
- Every mutation reports its failure (`toast.error`) and reads the answer before reporting success.
- Destructive actions say what will be lost and ask.
- No requests to other hosts: fonts, icons and anything else are part of the build.
- New functions go where they belong in the existing pages and menus.

### Documentation

- Describe what the interface does, in its words. A statement about behaviour should be one a test checks, or one you verified.
- Say what is not tested and what is not supported.
- New alert events go into `handlers/alert_catalog.go` and `docs/admin/ALERTS.md` together.

## Submitting

1. Branch from `main`.
2. Make the change with its tests, and its documentation.
3. Open a pull request that says what changed, why, and how it was tested.

For security issues, see [SECURITY.md](SECURITY.md).

## License

Contributions are licensed under the project's [GNU Affero General Public License v3.0](LICENSE). See `docs/legal/CLA-INDIVIDUAL.md`.
