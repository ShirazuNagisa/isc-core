# ISC-Core 构建入口
#
# 工具版本统一从 scripts/tool-versions.env 读取，避免与 scripts/generate.ps1 漂移。

SHELL := /bin/sh

TOOL_VERSIONS := scripts/tool-versions.env
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

.PHONY: fmt
fmt:
	gofmt -l -w .

.PHONY: matrix
matrix:
	@./scripts/check-matrix.sh

.PHONY: all
all: vet test matrix build
