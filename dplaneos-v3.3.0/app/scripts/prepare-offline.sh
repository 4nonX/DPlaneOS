#!/bin/bash
# D-PlaneOS v2 - Offline Dependency Downloader
# This script downloads all external dependencies for offline installation

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
ASSETS_DIR="$PROJECT_ROOT/assets"
FONTS_DIR="$ASSETS_DIR/fonts"

echo "=== D-PlaneOS v2 Offline Preparation ==="
echo ""

# Create directories
mkdir -p "$FONTS_DIR"

# Download Material Symbols font
echo "📥 Downloading Material Symbols Rounded font..."
if [ ! -f "$FONTS_DIR/MaterialSymbolsRounded.woff2" ]; then
    curl -sL "https://github.com/google/material-design-icons/raw/master/variablefont/MaterialSymbolsRounded%5BFILL%2CGRAD%2Copsz%2Cwght%5D.woff2" \
        -o "$FONTS_DIR/MaterialSymbolsRounded.woff2"
    echo "✓ Material Symbols font downloaded"
else
    echo "✓ Material Symbols font already exists"
fi

# Download Inter Variable font (optional)
echo ""
echo "📥 Downloading Inter Variable font..."
if [ ! -f "$FONTS_DIR/Inter-Variable.ttf" ]; then
    TEMP_DIR=$(mktemp -d)
    curl -sL "https://github.com/rsms/inter/releases/download/v4.0/Inter-4.0.zip" -o "$TEMP_DIR/inter.zip"
    unzip -q "$TEMP_DIR/inter.zip" "InterVariable.ttf" -d "$TEMP_DIR"
    mv "$TEMP_DIR/InterVariable.ttf" "$FONTS_DIR/Inter-Variable.ttf"
    rm -rf "$TEMP_DIR"
    echo "✓ Inter font downloaded"
else
    echo "✓ Inter font already exists"
fi

# Verify files
echo ""
echo "=== Verification ==="
echo ""

check_file() {
    if [ -f "$1" ]; then
        SIZE=$(du -h "$1" | cut -f1)
        echo "✓ $2: $SIZE"
        return 0
    else
        echo "✗ $2: MISSING"
        return 1
    fi
}

ERRORS=0

check_file "$FONTS_DIR/MaterialSymbolsRounded.woff2" "Material Symbols" || ((ERRORS++))
check_file "$FONTS_DIR/Inter-Variable.ttf" "Inter Variable" || ((ERRORS++))

echo ""
if [ $ERRORS -eq 0 ]; then
    echo "✓ All dependencies downloaded successfully!"
    echo ""
    echo "Package is now ready for offline installation."
    echo "You can create the distribution package with:"
    echo "  cd $PROJECT_ROOT && tar -czf dplaneos-v2-offline.tar.gz ."
else
    echo "✗ Some dependencies failed to download ($ERRORS errors)"
    exit 1
fi
