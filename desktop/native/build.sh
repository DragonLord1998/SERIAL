#!/bin/sh
set -eu
cd "$(dirname "$0")/../.."
out="${1:-.tools/native/GoAnimeMetalFX}"
mkdir -p "$(dirname "$out")" .tools/native/module-cache
xcrun swiftc -module-cache-path .tools/native/module-cache -O -swift-version 5 -target arm64-apple-macos13.0 desktop/native/main.swift -o "$out" -framework Cocoa -framework AVFoundation -framework Metal -framework MetalKit -framework MetalFX -framework CoreVideo
bundle="$(dirname "$out")/GoAnimeMetalFX.app"
mkdir -p "$bundle/Contents/MacOS"
cp "$out" "$bundle/Contents/MacOS/GoAnimeMetalFX"
cat > "$bundle/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>io.goanime.metalfx.player</string>
<key>CFBundleName</key><string>SERIAL MetalFX</string>
<key>CFBundleDisplayName</key><string>SERIAL MetalFX Player</string>
<key>CFBundleExecutable</key><string>GoAnimeMetalFX</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleVersion</key><string>1</string>
<key>CFBundleShortVersionString</key><string>1.0</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
codesign --force --sign - "$bundle"
codesign --verify --strict "$bundle"
