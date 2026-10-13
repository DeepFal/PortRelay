#!/bin/bash
# 构建 macOS 菜单栏应用 PortRelay.app（内含 Go Agent）。
#
#   ./build-app.sh                 # 当前架构
#   UNIVERSAL=1 ./build-app.sh     # Apple Silicon + Intel 通用二进制
#   VERSION=1.2.0 ./build-app.sh   # 写入版本号（默认 0.0.0-dev）
set -euo pipefail

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$DIR"

VERSION="${VERSION:-0.0.0-dev}"
BUNDLE_VERSION="${VERSION%%-*}"
[[ "$BUNDLE_VERSION" =~ ^[0-9]+(\.[0-9]+)*$ ]] || BUNDLE_VERSION="0.0.0"
MIN_MACOS="13.0"
APP="PortRelay.app"
BUILD="build/app"
mkdir -p "$BUILD" bin

if [ "${UNIVERSAL:-0}" = "1" ]; then
    ARCHS="arm64 x86_64"
else
    ARCHS="$(uname -m)"
fi

goarch() { [ "$1" = "x86_64" ] && echo amd64 || echo "$1"; }

echo "==> 1. Building Go agent ($ARCHS)..."
agents=()
for a in $ARCHS; do
    CGO_ENABLED=0 GOOS=darwin GOARCH="$(goarch "$a")" \
        go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$BUILD/portrelay-agent-$a" ./cmd/portrelay-agent
    agents+=("$BUILD/portrelay-agent-$a")
done
lipo -create -output bin/portrelay-agent "${agents[@]}"

echo "==> 2. Generating AppIcon if needed..."
if [ ! -f mac-native/AppIcon.icns ]; then
    xcrun swift mac-native/generate_icon.swift
    iconutil -c icns mac-native/AppIcon.iconset -o mac-native/AppIcon.icns
fi

echo "==> 3. Compiling native Swift app ($ARCHS)..."
apps=()
for a in $ARCHS; do
    xcrun swiftc -parse-as-library -O -target "$a-apple-macos$MIN_MACOS" \
        mac-native/PortRelayApp.swift -o "$BUILD/PortRelay-$a"
    apps+=("$BUILD/PortRelay-$a")
done
lipo -create -output "$BUILD/PortRelay" "${apps[@]}"

echo "==> 4. Assembling $APP..."
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BUILD/PortRelay" "$APP/Contents/MacOS/PortRelay"
cp bin/portrelay-agent "$APP/Contents/Resources/portrelay-agent"
cp mac-native/AppIcon.icns "$APP/Contents/Resources/AppIcon.icns"
chmod +x "$APP/Contents/MacOS/PortRelay" "$APP/Contents/Resources/portrelay-agent"

cat << PLIST > "$APP/Contents/Info.plist"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>PortRelay</string>
    <key>CFBundleIdentifier</key>
    <string>com.deepfal.portrelay</string>
    <key>CFBundleName</key>
    <string>PortRelay</string>
    <key>CFBundleDisplayName</key>
    <string>PortRelay</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon.icns</string>
    <key>CFBundleIconName</key>
    <string>AppIcon</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundleVersion</key>
    <string>${BUNDLE_VERSION}</string>
    <key>LSMinimumSystemVersion</key>
    <string>${MIN_MACOS}</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSLocalNetworkUsageDescription</key>
    <string>PortRelay 需要访问本地网络，以连接边缘网关并把流量转发到内网目标服务。</string>
</dict>
</plist>
PLIST

echo "==> 5. Ad-hoc signing..."
xattr -cr "$APP"
codesign --force --deep --sign - "$APP"
touch "$APP"
if [ -z "${CI:-}" ]; then
    /System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$APP" || true
    xcrun swift mac-native/set_icon.swift "$APP" || true
fi

echo "✓ Build complete: $DIR/$APP ($VERSION, $ARCHS)"
