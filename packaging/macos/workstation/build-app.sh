#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 3 || "$(uname -s)" != Darwin ]]; then
    echo 'Usage (on macOS): build-app.sh <Paseo.app> <cc-remote> <output.app>' >&2
    exit 2
fi
root="$(cd "$(dirname "$0")/../../.." && pwd)"
paseo="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
adapter="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
mkdir -p "$(dirname "$3")"
output="$(cd "$(dirname "$3")" && pwd)/$(basename "$3")"
if [[ "$output" != *.app || -e "$output" || -L "$output" ]]; then
    echo "Choose a new .app output path: $output" >&2
    exit 2
fi

arch="$(uname -m)"
xcrun lipo "$paseo/Contents/MacOS/Paseo" -verify_arch "$arch"
xcrun lipo "$adapter" -verify_arch "$arch"
stage="$(mktemp -d "$(dirname "$output")/.cage-workstation.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
contents="$stage/Cage Workstation.app/Contents"
mkdir -p "$contents/MacOS" "$contents/Helpers" "$contents/Resources"
workbench="$contents/Helpers/PaseoLAN.app"
ditto --noqtn "$paseo" "$workbench"
install -m 755 "$adapter" "$contents/Helpers/cc-remote"

cd "$root"
"${GO:-go}" build -trimpath -ldflags '-s -w' -o "$contents/MacOS/CageWorkstationLAN" ./cmd/cage-workstation
xcrun swiftc -O -target "$arch-apple-macosx13.0" -parse-as-library \
    packaging/macos/workstation/Launcher.swift -o "$contents/MacOS/CageWorkstation"
resources="$workbench/Contents/Resources"
node packaging/windows/workstation/paseo-lan/build.mjs --macos-resources "$resources"
asar_hash="$(node -p 'const a=require("fs").readFileSync(process.argv[1]);require("crypto").createHash("sha256").update(a.subarray(16,16+a.readUInt32LE(12))).digest("hex")' "$resources/app.asar")"
/usr/libexec/PlistBuddy -c "Set :ElectronAsarIntegrity:Resources/app.asar:hash $asar_hash" "$workbench/Contents/Info.plist"
# Updating the upstream desktop in place would replace the Cage integration.
rm -f "$resources/app-update.yml"
cp "$resources/icon.icns" "$contents/Resources/Workstation.icns"
cat > "$contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Cage Workstation</string>
  <key>CFBundleIdentifier</key><string>io.github.lee-xeo.claude-cage.workstation</string>
  <key>CFBundleExecutable</key><string>CageWorkstation</string>
  <key>CFBundleIconFile</key><string>Workstation</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>0.1.8</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>LSUIElement</key><true/>
  <key>NSLocalNetworkUsageDescription</key><string>发现并连接同一局域网中的 Claude Cage 服务器。</string>
  <key>NSBonjourServices</key><array><string>_claude-cage._tcp</string></array>
</dict>
</plist>
PLIST

identity="${CAGE_SIGNING_IDENTITY:--}"
codesign --force --sign "$identity" --preserve-metadata=entitlements "$workbench"
codesign --force --sign "$identity" "$contents/Helpers/cc-remote"
codesign --force --sign "$identity" "$contents/MacOS/CageWorkstationLAN"
bundle="$(dirname "$contents")"
codesign --force --sign "$identity" "$bundle"
codesign --verify --deep --strict "$bundle"
mv "$bundle" "$output"
printf 'Built: %s\n' "$output"
