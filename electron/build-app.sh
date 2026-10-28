#!/bin/bash
# Vecgrep Studio — manual macOS .app build (no electron-packager: it exits
# silently mid-extraction on this toolchain, so we assemble the bundle with
# ditto/PlistBuddy/codesign instead). Run from electron/: ./build-app.sh
set -euo pipefail
cd "$(dirname "$0")"

APP_NAME="Vecgrep Studio"
BUNDLE_ID="com.vecgrep.studio"
VERSION="$(node -p "require('./package.json').version")"
OUT="dist/Vecgrep Studio-darwin-arm64"
APP="$OUT/$APP_NAME.app"
TEMPLATE="node_modules/electron/dist/Electron.app"

[ -d "$TEMPLATE" ] || { echo "missing $TEMPLATE (run: npm install)"; exit 1; }
[ -x "../bin/vecgrep" ] || { echo "missing ../bin/vecgrep (run: task build)"; exit 1; }

echo "-> cleaning $OUT"
rm -rf "$OUT"
mkdir -p "$OUT"

echo "-> copying Electron template"
ditto "$TEMPLATE" "$APP"

echo "-> renaming executable"
mv "$APP/Contents/MacOS/Electron" "$APP/Contents/MacOS/$APP_NAME"

echo "-> patching Info.plist"
PLIST="$APP/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleDisplayName $APP_NAME" "$PLIST"
/usr/libexec/PlistBuddy -c "Set :CFBundleName $APP_NAME" "$PLIST"
/usr/libexec/PlistBuddy -c "Set :CFBundleIdentifier $BUNDLE_ID" "$PLIST"
/usr/libexec/PlistBuddy -c "Set :CFBundleExecutable $APP_NAME" "$PLIST"
/usr/libexec/PlistBuddy -c "Set :CFBundleIconFile $APP_NAME" "$PLIST"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $VERSION" "$PLIST"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $VERSION" "$PLIST"
/usr/libexec/PlistBuddy -c "Delete :CFBundleURLTypes" "$PLIST" 2>/dev/null || true
rm -f "$APP/Contents/Resources/electron.icns"

echo "-> installing icon"
cp assets/icon.icns "$APP/Contents/Resources/$APP_NAME.icns"

echo "-> copying app code (no node_modules: zero runtime deps)"
mkdir -p "$APP/Contents/Resources/app"
cp main.js preload.js bridge.js package.json "$APP/Contents/Resources/app/"
ditto renderer "$APP/Contents/Resources/app/renderer"
mkdir -p "$APP/Contents/Resources/app/assets"
cp assets/icon.png "$APP/Contents/Resources/app/assets/"

echo "-> bundling vecgrep binary"
cp ../bin/vecgrep "$APP/Contents/Resources/vecgrep"
chmod +x "$APP/Contents/Resources/vecgrep"

echo "-> ad-hoc signing"
codesign --force --deep -s - "$APP" 2>/dev/null || echo "(codesign skipped)"

echo "built: $APP"
