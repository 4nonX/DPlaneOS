---
description: D-PlaneOS Standard Release Procedure
---

Follow these steps for EVERY release to ensure CI/CD consistency and prevent stale bundle errors.

// turbo-all
1. Update `VERSION` file in the root directory.
2. Update `version` in `app-react/package.json`.
3. Update `version` in `D-Plane-Compliance-Engine/app-react-pro/package.json`.
4. Run `npm run build` in `app-react/` to refresh the `app/` production assets.
5. Update `docs/reference/CHANGELOG.md` with the new version entry.
6. Commit all changes with `release: vX.Y.Z - [Title]`.
7. Delete local tag: `git tag -d vX.Y.Z` (if exists).
8. Delete remote tag: `git push origin :refs/tags/vX.Y.Z` (if exists).
9. Create new tag: `git tag vX.Y.Z`.
10. Push main: `git push -f origin main`.
11. Push tag: `git push origin vX.Y.Z`.
