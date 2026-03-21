#!/usr/bin/env bash
# scripts/release-check.sh
# Run this before tagging a release to ensure the repository is in a valid state.

set -e

# 1. Check for uncommitted changes
if [ -n "$(git status --porcelain)" ]; then
    echo "❌ ERROR: Working directory is not clean. Commit or stash your changes first."
    git status --short
    exit 1
fi

# 2. Check current version
VERSION=$(cat VERSION | tr -d '[:space:]')
echo "🔍 Current version in file: $VERSION"

# 3. Check if version matches the latest tag
LATEST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "none")
if [ "v$VERSION" == "$LATEST_TAG" ]; then
    echo "⚠️ WARNING: VERSION file ($VERSION) matches the latest tag ($LATEST_TAG). Did you forget to bump the version?"
fi

# 4. Check for Syncthing conflict files
CONFLICTS=$(find . -name '.sync-conflict-*.go' 2>/dev/null | head -5 || true)
if [ -n "$CONFLICTS" ]; then
    echo "❌ ERROR: Syncthing conflict files found:"
    echo "$CONFLICTS"
    exit 1
fi

# 5. Verify CHANGELOG has an entry for this version
if ! grep -q "## v$VERSION" docs/reference/CHANGELOG.md; then
    echo "❌ ERROR: docs/reference/CHANGELOG.md is missing an entry for v$VERSION"
    exit 1
fi

# 6. Check for submodule drifts
SUBMODULE_STATUS=$(git submodule status)
if echo "$SUBMODULE_STATUS" | grep -q "^+"; then
    echo "⚠️ WARNING: Submodules have uncommitted changes/pointers."
    echo "$SUBMODULE_STATUS"
fi

echo "✅ All checks passed! You are safe to tag: git tag v$VERSION"
