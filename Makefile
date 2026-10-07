BINARY  := bin/volgate
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := help
.PHONY: help build install test vet fmt cover clean check serve

help:
	@echo "volgate"
	@echo ""
	@echo "  make build    建置 $(BINARY)"
	@echo "  make test     go vet + go test ./..."
	@echo "  make cover    測試並產生 coverage.txt + 摘要"
	@echo "  make fmt      gofmt 全部檔案"
	@echo "  make check    fmt 檢查 + vet + test(CI 用)"
	@echo "  make serve    在本機 127.0.0.1:8791 起 HTTP 服務"
	@echo "  make clean    刪除建置產物"

build:
	@mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/volgate
	@echo "▸ $(BINARY) ($(VERSION))"

install:
	go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/volgate

test:
	go vet ./...
	go test ./...

cover:
	go test -coverprofile=coverage.txt -covermode=atomic ./...
	go tool cover -func=coverage.txt | tail -1

fmt:
	gofmt -w .

check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	go test ./...

serve: build
	$(BINARY) serve -addr 127.0.0.1:8791

clean:
	@rm -rf bin coverage.txt
