# D-PlaneOS Legacy (v1.x)

This directory contains documentation and files from the v1.x PHP-based release series.

D-PlaneOS v2.0.0 is a complete rewrite in Go. These files are preserved for historical reference.

## What was here

| File/Folder | Description | Status in v2.0.0 |
|-------------|-------------|-------------------|
| `backend/` | PHP API backend | Replaced by Go daemon (`dplaned`) |
| `frontend-built/` | PHP-rendered frontend | Replaced by static HTML + vanilla JS |
| `offline-packages/` | .deb packages for PHP/Apache | No longer needed |
| `install-offline.sh` | Offline installer | Replaced by `make install` |
| `FIX_SUMMARY.md` | v1.x bug fix documentation | Historical reference |
| `INSTALL-SAFETY.md` | PHP installation safety guide | Replaced by `RECOVERY.md` |
| `INTERNET-DEPLOYMENT.md` | PHP deployment guide | Replaced by `README.md` |
| `MANIFEST.txt` | v1.x file listing | Replaced by this repo structure |
| `RBAC_IMPLEMENTATION.md` | PHP RBAC documentation | Replaced by Go RBAC in `SECURITY.md` |
| `SECURITY-QUICK-REF.md` | v1.x security quick reference | Replaced by `SECURITY.md` |
| `SHA256SUMS` | v1.x checksums | New checksums in release artifacts |

## Upgrade Path

v2.0.0 is not an in-place upgrade from v1.x. See the main `README.md` for installation instructions.

Your ZFS pools, datasets, shares, and Docker containers remain on disk - only the management layer changes.
