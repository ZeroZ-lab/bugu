# 决策记录 0002:全程不使用 cgo。
export CGO_ENABLED = 0

VERSION ?= dev

.PHONY: build build-headless app test fmt check fmt-check vet cross web web-dev web-check

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/bugu .

# 无界面构建:不包含桌面框架,用于服务器和容器。
build-headless:
	go build -tags headless -ldflags "-X main.version=$(VERSION)" -o bin/bugu-headless .

# 打包桌面应用到 dist/。打包工具的版本跟随 go.mod 里的桌面框架版本,配置在 mygo.json。
# 没有配置 Developer ID 时只做本机签名:自己用可以,发给别人会被系统拦截。
MYGO_VERSION = $(shell go list -m -f '{{.Version}}' github.com/egoist/mygo)
app:
	MYGO_LDFLAGS="-X main.version=$(VERSION)" go run github.com/egoist/mygo/cmd/mygo@$(MYGO_VERSION) build -skip-dmg

test:
	go test ./...

fmt:
	gofmt -w .

# 合并前的唯一关卡。
check: fmt-check vet test cross web-check

# ---- 网页前端(决策记录 0010)----
# 源码在 web/,构建产物写进 internal/server/static 并提交进仓库,
# 所以只改 Go 代码、构建程序时不需要 Node;改前端或运行 make check 需要。

web/node_modules: web/package.json web/package-lock.json
	cd web && npm ci --no-audit --no-fund
	@touch $@

# 构建前端,改完 web/ 下的源码后运行,连同产物一起提交。
web: web/node_modules
	cd web && npm run build

# 开发:Vite 提供页面并热更新,接口转给本机 7377 端口上的后台。
web-dev: web/node_modules
	cd web && npm run dev

# 类型检查、测试,并确认已提交的产物与源码一致。
web-check: web/node_modules
	cd web && npm run typecheck && npm test
	@tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	(cd web && npx vite build --logLevel warn --outDir "$$tmp" --emptyOutDir) && \
	diff -r "$$tmp" internal/server/static >/dev/null || \
	{ echo "internal/server/static 与 web/ 的源码不一致,请运行 make web 后一起提交。"; exit 1; }

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "以下文件未格式化,请运行 make fmt:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...
	GOOS=linux go vet ./...
	go vet -tags headless ./...

# 确认 macOS 和 Linux 都能在不使用 cgo 的情况下编译。
cross:
	GOOS=darwin GOARCH=arm64 go build -o /dev/null .
	GOOS=linux GOARCH=amd64 go build -o /dev/null .
	GOOS=linux GOARCH=arm64 go build -o /dev/null .
	GOOS=linux GOARCH=amd64 go build -tags headless -o /dev/null .
	@if go list -deps -tags headless . | grep -q egoist/mygo; then echo "无界面构建不应包含桌面框架"; exit 1; fi
