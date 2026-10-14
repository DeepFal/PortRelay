#!/bin/bash
# 用无头 Chrome 截取 README 里的授权页和网页面板（模拟数据）。
# 用法: tools/ui-snapshots/web.sh   输出: tools/ui-snapshots/out/web/*.png
# 需要 Google Chrome，路径可用 CHROME 环境变量指定。
set -e
DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
BUILD="$DIR/.build/web"
OUT="$DIR/out/web"
CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
[ -x "$CHROME" ] || { echo "找不到 Chrome：$CHROME（可用 CHROME=... 指定）" >&2; exit 1; }
rm -rf "$BUILD" "$OUT"
mkdir -p "$BUILD" "$OUT"

# shot URL 宽 高 输出：按 2 倍像素截图，使用临时的浏览器配置
shot() {
	"$CHROME" --headless=new --disable-gpu --hide-scrollbars --no-first-run --no-default-browser-check \
		--blink-settings=preferredColorScheme=1 --force-device-scale-factor=2 --window-size="$2,$3" --timeout=3000 \
		--user-data-dir="$BUILD/chrome" --screenshot="$4" "$1" >/dev/null 2>&1
}

# 授权页：服务端用模拟状态生成 HTML。无头 Chrome 的窗口最窄约 500pt，
# 所以把页面放进居中的 390pt 宽 iframe（手机宽度），截图后从中间裁出 iframe 部分
(cd "$ROOT" && PORTRELAY_README_PAGES="$BUILD" go test ./server -run '^TestReadmePages$' -count=1 >/dev/null)
for page in "$BUILD"/auth-*.html; do
	name="$(basename "$page" .html)"
	printf '<!DOCTYPE html><body style="margin:0;background:#F5F5F7"><iframe src="%s" width="390" height="610" style="border:0;display:block;margin:0 auto"></iframe>' \
		"$(basename "$page")" >"$BUILD/frame-$name.html"
	shot "file://$BUILD/frame-$name.html" 520 610 "$OUT/$name.png"
	sips -c 1220 780 "$OUT/$name.png" >/dev/null
	echo "captured $name"
done

# 网页面板：本机模拟 Agent API，只监听回环地址
(cd "$ROOT" && go build -o "$BUILD/webpanel" ./tools/ui-snapshots/webpanel)
"$BUILD/webpanel" "$ROOT/cmd/portrelay-agent/web/index.html" >"$BUILD/webpanel.url" &
pid=$!
trap 'kill $pid 2>/dev/null' EXIT
for _ in $(seq 50); do [ -s "$BUILD/webpanel.url" ] && break; sleep 0.1; done
shot "$(head -1 "$BUILD/webpanel.url")" 1280 1124 "$OUT/web-panel.png"
echo "captured web-panel"
