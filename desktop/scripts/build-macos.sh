#!/usr/bin/env bash
set -euo pipefail

taskWorkspace="$(cd "$(dirname "$0")/../.." && pwd)"
taskTools="$taskWorkspace/.tools"
export PATH="$taskTools/go/bin:$taskTools/bin:$PATH"
command -v go >/dev/null || { printf '%s\n' 'Go 1.27.1 or newer is required to build. The packaged app runs without Go.' >&2; exit 1; }
command -v npm >/dev/null || { printf '%s\n' 'Node.js and npm are required to build.' >&2; exit 1; }
mkdir -p "$taskTools/bin"
if ! command -v wails >/dev/null; then
 GOBIN="$taskTools/bin" go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0
fi

# Keep the official mpv bundle outside Wails' output directory across rebuilds.
taskPlayer="$taskTools/mpv.app"
if [[ ! -d "$taskPlayer" ]]; then
 taskExisting="$taskWorkspace/desktop/build/bin/SERIAL.app/Contents/Resources/tools/mpv.app"
 if [[ ! -d "$taskExisting" ]]; then
  taskExisting="$taskWorkspace/desktop/build/bin/GoAnime.app/Contents/Resources/tools/mpv.app"
 fi
 if [[ -d "$taskExisting" ]]; then
  ditto "$taskExisting" "$taskPlayer"
 else
  taskRelease='https://github.com/mpv-player/mpv/releases/download/git-release/mpv-v0.41.0-dev-ga1f50f2c3-36640285359-macos-14-arm.zip'
  curl --fail --location "$taskRelease" --output "$taskTools/mpv.zip"
  printf '%s  %s\n' 'b4bb538d7b765b49b9104b4fe2eff5e84efdc55e9aeb7810cb75f04ea7a05211' "$taskTools/mpv.zip" | shasum -a 256 -c -
  unzip -q -o "$taskTools/mpv.zip" -d "$taskTools/mpv-download"
  tar -xzf "$taskTools/mpv-download/mpv.tar.gz" -C "$taskTools"
 fi
fi

cd "$taskWorkspace/desktop"
wails build -platform darwin/arm64 -clean
"$taskWorkspace/desktop/native/build.sh"
mkdir -p build/bin/SERIAL.app/Contents/Resources/tools
ditto "$taskWorkspace/.tools/native/GoAnimeMetalFX.app" build/bin/SERIAL.app/Contents/Resources/tools/GoAnimeMetalFX.app
ditto "$taskPlayer" build/bin/SERIAL.app/Contents/Resources/tools/mpv.app
cp THIRD_PARTY.md build/bin/SERIAL.app/Contents/Resources/THIRD_PARTY.md
codesign --force --deep --sign - build/bin/SERIAL.app
codesign --verify --deep --strict build/bin/SERIAL.app
printf '%s\n' "$taskWorkspace/desktop/build/bin/SERIAL.app"
