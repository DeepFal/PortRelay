#!/bin/bash
# 离屏渲染 App 各界面（模拟数据、浅色/深色），用于检查布局。
# 用法: tools/ui-snapshots/render.sh [只渲染的名称前缀]   输出: tools/ui-snapshots/out/*.png
set -e
DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
BUILD="$DIR/.build"
mkdir -p "$BUILD" "$DIR/out"
# 去掉 @main，与渲染入口一起编译
sed 's/^@main$//' "$ROOT/mac-native/PortRelayApp.swift" > "$BUILD/app.swift"
xcrun swiftc -O -target "$(uname -m)-apple-macos13.0" "$BUILD/app.swift" "$DIR/main.swift" -o "$BUILD/render"
rm -f "$DIR/out/${1:-}"*.png
"$BUILD/render" "$DIR/out" "$1"
