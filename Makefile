# 决策记录 0002:全程不使用 cgo。
export CGO_ENABLED = 0

VERSION ?= dev

.PHONY: build test fmt check fmt-check vet cross

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/bugu .

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

# 确认 macOS 和 Linux 都能在不使用 cgo 的情况下编译。
cross:
	GOOS=darwin GOARCH=arm64 go build -o /dev/null .
	GOOS=linux GOARCH=amd64 go build -o /dev/null .
	GOOS=linux GOARCH=arm64 go build -o /dev/null .
