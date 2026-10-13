# PortRelay —— 常用命令
#   make test        运行 gofmt 检查、go vet 与全部测试（-race）
#   make server      交叉编译 Linux 服务端到 dist/
#   make agent       编译当前平台的 Agent 到 bin/
#   make app         构建 macOS 应用 PortRelay.app（需要 Xcode 命令行工具）
#   make release     打包所有平台的服务端 / Agent 到 dist/
#   make snapshots   离屏渲染 App 界面截图（tools/ui-snapshots/out）

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"

PLATFORMS := linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64

.PHONY: all test fmt server agent app release snapshots clean

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

release:
	@rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		name=portrelay-$(VERSION)-$$os-$$arch; mkdir -p dist/$$name; \
		echo "==> $$name"; \
		GOOS=$$os GOARCH=$$arch $(GOBUILD) -o dist/$$name/portrelay-server$$ext ./cmd/portrelay-server || exit 1; \
		GOOS=$$os GOARCH=$$arch $(GOBUILD) -o dist/$$name/portrelay-agent$$ext ./cmd/portrelay-agent || exit 1; \
		cp LICENSE README.md README_EN.md dist/$$name/; \
		if [ $$os = linux ]; then cp -R deploy dist/$$name/; fi; \
		if [ $$os = windows ]; then (cd dist && zip -qr $$name.zip $$name); \
		else tar -C dist -czf dist/$$name.tar.gz $$name; fi; \
		rm -rf dist/$$name; \
	done
	@cd dist && shasum -a 256 * > SHA256SUMS && cat SHA256SUMS

snapshots:
	tools/ui-snapshots/render.sh

clean:
	rm -rf bin build dist PortRelay.app mac-native/PortRelay tools/ui-snapshots/.build tools/ui-snapshots/out
