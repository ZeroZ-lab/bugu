# 决策记录 0002:全程不使用 cgo。
export CGO_ENABLED = 0

VERSION ?= dev

.PHONY: build build-headless test fmt check fmt-check vet cross

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/bugu .

# 无界面构建:不包含桌面框架,用于服务器和容器。
build-headless:
	go build -tags headless -ldflags "-X main.version=$(VERSION)" -o bin/bugu-headless .

test:
	go test ./...

fmt:
	gofmt -w .

# 合并前的唯一关卡。
check: fmt-check vet test cross

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
