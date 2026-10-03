# ISC-Core 构建入口
#
# 工具版本统一从 scripts/tool-versions.env 读取，避免与 scripts/generate.ps1 漂移。

SHELL := /bin/sh

TOOL_VERSIONS := scripts/tool-versions.env
GO_VERSION := $(shell sed -n 's/^GO_VERSION=//p' $(TOOL_VERSIONS))
OAPI_CODEGEN_VERSION := $(shell sed -n 's/^OAPI_CODEGEN_VERSION=//p' $(TOOL_VERSIONS))
OPENAPI_TYPESCRIPT_VERSION := $(shell sed -n 's/^OPENAPI_TYPESCRIPT_VERSION=//p' $(TOOL_VERSIONS))

OAPI_CODEGEN := go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)

# 本项目硬约束：禁止 cgo（见 docs/PLAN.md §0）。
export CGO_ENABLED := 0

.PHONY: help
help:
	@echo "make generate    生成 OpenAPI 服务端代码与前端客户端"
	@echo "make generate-go 仅生成 Go 服务端代码"
	@echo "make check-gen   检查生成物是否与 spec 同步（CI 用）"
	@echo "make build       构建 isc 二进制"
	@echo "make test        运行测试"
	@echo "make vet         静态检查"
	@echo "make fmt         格式化（会先核对 Go 版本）"
	@echo "make check-fmt   只检查格式（与 CI 同一条判据，$(GO_VERSION)）"
	@echo "make matrix      交叉编译矩阵验证（7 个目标）"
	@echo "make all         vet + test + build + matrix"

.PHONY: generate
generate: generate-go

.PHONY: generate-go
generate-go:
	$(OAPI_CODEGEN) --config api/oapi-codegen.yaml api/openapi.yaml
	@echo "已生成 internal/api/gen/api.gen.go"

.PHONY: check-gen
check-gen:
	@$(MAKE) --no-print-directory generate-go
	@if ! git diff --quiet -- internal/api/gen; then \
		echo "❌ 生成物与 api/openapi.yaml 不同步。请运行 'make generate' 并提交结果。"; \
		git --no-pager diff --stat -- internal/api/gen; \
		exit 1; \
	fi
	@echo "✅ 生成物与 spec 同步"

.PHONY: build
build:
	go build -o bin/isc ./cmd/isc

.PHONY: test
test:
	go test ./...

.PHONY: vet
vet:
	go vet ./...

# gofmt 的输出**随 Go 版本变化**：1.26 与 1.27 对含 CJK 的 map 字面量用
# 不同的对齐宽度算法（1.27 按显示宽度、1.26 按字符数）。因此"用本机的
# gofmt"会产出被 CI 拒绝的格式 —— 而 CI 的报错只会说"以下文件未格式化"，
# 指不到"你的 gofmt 版本不对"。
#
# 所以这两个目标都先核对版本，并且**只处理受版本控制的文件**
#（`gofmt -w .` 会顺手改动 ddns-go-master/ 那份上游参考源码）。
# `go env GOVERSION` 的形状是 `go1.27.1`（前缀 go，不是 v）。
GO_VERSION_TAG := go$(GO_VERSION)
GO_VERSION_HINT = 项目固定 $(GO_VERSION_TAG)（$(TOOL_VERSIONS)）；gofmt 的输出随版本变化

.PHONY: fmt
fmt:
	@test "$$(go env GOVERSION)" = "$(GO_VERSION_TAG)" || { \
		echo "❌ Go 版本不符：本机 $$(go env GOVERSION)，$(GO_VERSION_HINT)。"; \
		echo "   用别的版本格式化会让 CI 的格式检查失败。"; exit 1; }
	@gofmt -l -w $$(git ls-files '*.go') && echo "✅ 已格式化（$$(go env GOVERSION)）"

.PHONY: check-fmt
check-fmt:
	@test "$$(go env GOVERSION)" = "$(GO_VERSION_TAG)" || { \
		echo "❌ Go 版本不符：本机 $$(go env GOVERSION)，$(GO_VERSION_HINT)。"; \
		echo "   用别的版本格式化会让 CI 的格式检查失败。"; exit 1; }
	@unformatted=$$(gofmt -l $$(git ls-files '*.go')); \
	if [ -n "$$unformatted" ]; then \
		echo "❌ 以下文件未格式化："; echo "$$unformatted"; \
		echo "   请运行 make fmt。"; exit 1; \
	fi; \
	echo "✅ 格式干净（$$(go env GOVERSION)）"

.PHONY: matrix
matrix:
	@./scripts/check-matrix.sh

.PHONY: all
all: check-fmt vet test matrix build
