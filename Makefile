# PortRelay —— 常用命令
#   make test        运行 gofmt 检查、go vet 与全部测试（-race）
#   make server      交叉编译 Linux 服务端到 dist/
#   make agent       编译当前平台的 Agent 到 bin/
#   make app         构建 macOS 应用 PortRelay.app（需要 Xcode 命令行工具）
#   make install-app 构建并替换 /Applications/PortRelay.app（正在运行的会先退出，装好后重新打开）
#   make dmg         打包 macOS 安装镜像 dist/PortRelay-<版本>.dmg（UNIVERSAL=1 构建通用版本）
#   make release     打包服务端（Linux）与命令行 Agent（Linux / Windows）到 dist/
#   make snapshots   离屏渲染 App 界面截图（tools/ui-snapshots/out）

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"

# 服务端只发布 Linux 版本；macOS 用户用 App，其余平台用命令行 Agent
SERVER_PLATFORMS := linux/amd64 linux/arm64
AGENT_PLATFORMS := linux/amd64 linux/arm64 windows/amd64

.PHONY: all test fmt server agent app install-app dmg release snapshots clean

all: test agent

test:
	@test -z "$$(gofmt -l .)" || (echo "需要 gofmt:" && gofmt -l . && exit 1)
	go vet ./...
	go test -race -count=1 ./...

fmt:
	gofmt -w .

server:
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o dist/portrelay-server-linux-amd64 ./cmd/portrelay-server
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o dist/portrelay-server-linux-arm64 ./cmd/portrelay-server

agent:
	$(GOBUILD) -o bin/portrelay-agent ./cmd/portrelay-agent

app:
	VERSION=$(VERSION) ./build-app.sh

# 正常退出正在运行的 App（会一并停止后台引擎）后替换，装好后按原状态重新打开
install-app: app
	@running=; \
	if pgrep -f /Applications/PortRelay.app/ >/dev/null; then \
		running=1; osascript -e 'quit app "PortRelay"' >/dev/null; \
		for i in $$(seq 1 30); do pgrep -f /Applications/PortRelay.app/ >/dev/null || break; sleep 0.5; done; \
		if pgrep -f /Applications/PortRelay.app/ >/dev/null; then echo "PortRelay 未能退出，已放弃替换"; exit 1; fi; \
	fi; \
	rm -rf /Applications/PortRelay.app && ditto PortRelay.app /Applications/PortRelay.app && rm -rf PortRelay.app; \
	echo "✓ 已安装 /Applications/PortRelay.app ($(VERSION))"; \
	if [ -n "$$running" ]; then open /Applications/PortRelay.app; fi

# 安装镜像：PortRelay.app 和「应用程序」的快捷方式，打开后拖进去即可
dmg: app
	@rm -rf build/dmg && mkdir -p build/dmg dist
	ditto PortRelay.app build/dmg/PortRelay.app && rm -rf PortRelay.app
	ln -s /Applications build/dmg/Applications
	hdiutil create -volname PortRelay -srcfolder build/dmg -fs HFS+ -format UDZO -ov dist/PortRelay-$(VERSION).dmg
	@rm -rf build/dmg

# 服务端：portrelay-server-<版本>-linux-<架构>.tar.gz（含 deploy/ 安装脚本）
# 客户端：portrelay-agent-<版本>-<系统>-<架构>.tar.gz / .zip
release:
	@rm -rf dist/portrelay-* dist/SHA256SUMS && mkdir -p dist
	@pack() { \
		cmd=$$1; os=$$2; arch=$$3; ext=; [ $$os = windows ] && ext=.exe; \
		name=$$cmd-$(VERSION)-$$os-$$arch; mkdir -p dist/$$name; \
		echo "==> $$name"; \
		GOOS=$$os GOARCH=$$arch $(GOBUILD) -o dist/$$name/$$cmd$$ext ./cmd/$$cmd || return 1; \
		cp LICENSE README.md README_EN.md dist/$$name/; \
		if [ $$cmd = portrelay-server ]; then cp -R deploy dist/$$name/; fi; \
		if [ $$os = windows ]; then (cd dist && zip -qr $$name.zip $$name); \
		else tar -C dist -czf dist/$$name.tar.gz $$name; fi; \
		rm -rf dist/$$name; \
	}; \
	for p in $(SERVER_PLATFORMS); do pack portrelay-server $${p%/*} $${p#*/} || exit 1; done; \
	for p in $(AGENT_PLATFORMS); do pack portrelay-agent $${p%/*} $${p#*/} || exit 1; done
	@cd dist && shasum -a 256 * > SHA256SUMS && cat SHA256SUMS

snapshots:
	tools/ui-snapshots/render.sh

clean:
	rm -rf bin build dist PortRelay.app mac-native/PortRelay tools/ui-snapshots/.build tools/ui-snapshots/out
