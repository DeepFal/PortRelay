#!/bin/bash
# 重新生成 README 用到的全部截图和动图（模拟数据），写入 docs/images。
# 用法: tools/ui-snapshots/readme.sh（即 make readme-images）
# 需要 Xcode 命令行工具和 Google Chrome。
set -e
DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
IMG="$ROOT/docs/images"

# App 界面：readme-xxx.png → app-xxx.png
"$DIR/render.sh" readme
for f in "$DIR"/out/readme-*.png; do
	name="$(basename "$f")"
	cp "$f" "$IMG/app-${name#readme-}"
done

# 通道卡片动图：40 帧，每帧相当于 5 秒
FRAMES=40 "$DIR/render.sh" gif
(cd "$ROOT" && go run ./tools/ui-snapshots/mkgif -o "$IMG/live-traffic.gif" "$DIR"/out/gif-*.png)

# 授权页与网页面板
"$DIR/web.sh"
cp "$DIR"/out/web/*.png "$IMG/"
echo "✓ 已更新 docs/images"
