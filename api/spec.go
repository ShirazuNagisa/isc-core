// Package api 内嵌 OpenAPI 契约原文。
//
// 契约文件 `openapi.yaml` 是接口的唯一真理（见 docs/DECISIONS.md D07）：
// Go 服务端代码由它生成，前端类型也由它生成。这里把原文一并嵌入二进制，
// 使内核可以把它直接提供给验证控制台与 Scalar / Swagger UI，
// 保证"文档"与"实现"永远是同一份东西。
//
// 之所以不开启 oapi-codegen 的 embedded-spec 选项：那会把
// github.com/getkin/kin-openapi 拖进生产依赖树，仅为解析 spec 成对象，
// 代价与收益不成比例。
package api

import _ "embed"

// OpenAPISpec 是 openapi.yaml 的原始内容。
//
//go:embed openapi.yaml
var OpenAPISpec []byte
