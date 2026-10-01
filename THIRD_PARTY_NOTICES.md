# 第三方软件声明 / Third-Party Notices

ISC-Core 以 **GPL-3.0** 发布。本项目包含或派生自以下第三方软件，其许可证与版权声明在此保留。

> 本文件由依赖许可证审计维护。**新增任何依赖前，必须确认其许可证与 GPL-3.0 兼容，并在本文件登记。**
>
> 审计方式见文末 §3。审计范围是**真正参与编译的模块**
> （`go list -deps`），而不是 `go list -m all` —— 后者会把依赖自身
> 的测试依赖也算进来，产生大量与实际产物无关的条目。

---

## 1. 直接派生（源码级移植）

### ddns-go

本项目以下模块的源码**派生自 ddns-go**，以源码级复制的方式纳入，并做了修改：

| 本项目路径 | 来源 |
|---|---|
| `internal/ddnsgo/` | `dns/*.go`（30 家服务商）、`util/*_signer.go`（5 套签名）、`util/ip_cache.go`、`util/http_util.go`、`util/http_client_util.go`、`util/string.go`、`util/escape.go`、`util/copy_url_params.go`、`util/socket_bind_*.go`、`config/domains.go`、`config/netInterface.go` |
| `internal/ipmon/`（并入 `internal/platform/ipmon.go`） | `config/config.go` 中的 IP 获取逻辑 |
| `internal/i18n/messages_ddnsgo.go` | `util/messages.go` 中的英文译文（83 条） |
| `internal/notify/channels/webhook.go`（M5） | `config/webhook.go` |

**移植方式**：由 `scripts/port-ddnsgo.ps1` **机械变换**生成，只做三件事 ——
统一包名、去掉 `config.` 与 `util.` 前缀、删除指向 ddns-go 自身包的 import。
**逻辑一行未改**：签名算法、URL、请求体、错误处理、比较条件全部原样保留。

其余修改：

- **剥离包级全局可变状态**（`util.ForceCompareGlobal` 被删除；
  `util.IpCache` 的防抖次数从环境变量 `DDNS_IP_CACHE_TIMES` 改为显式设置）。
  这是必须的：那两个全局被运行期反复改写并参与逻辑判断，
  在 ISC 的"定时触发 + 事件触发"并发模型下会产生难以复现的竞争
  （见 `docs/PLAN.md` R11）。
- `util.Log` / `util.LogStr` 改为桥接到 ISC 的结构化日志与 i18n；
  233 处调用点的签名保持不变。
- **新增 `DnsConfig.Ipv4/Ipv6.ForceAddr` 字段**（ISC 对上游结构唯一的字段新增）：
  由调用方注入已知地址，而不是让每个 provider 各查一次 —— ISC 有统一的
  IPMonitor 在跟踪地址与前缀变化。
- `TencentCloudSigner` 的两个服务名常量（`DnsPod` / `EdgeOne`）在移植后与
  两个 provider 的结构体同名，改名为 `svcDnsPod` / `svcEdgeOne`。

**验证**：`internal/provider/tier2_test.go` 用 callback 服务商对着本地假服务器
跑真实的动态更新，串起"具名凭据 → 位置化槽位 → 地址注入 → 域名解析 →
真实 HTTP → 结果翻译"整条链路。编译通过不足以证明移植成功。

```
MIT License

Copyright (c) 2020 jeessy

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

- 上游仓库：https://github.com/jeessy2/ddns-go
- 参考版本：v6（`go.mod` 声明 `go 1.25.0`）

**兼容性说明**：MIT 为宽松许可证，允许再许可为 GPL-3.0。本项目保留上述版权声明，
符合 MIT 第 1 条要求。

---

## 2. 编译进产物的第三方依赖

审计时间：M1 阶段（引入 SQLite 与 YAML 之后）。审计命令见 §3。

| 模块 | 版本 | 许可证 | 用途 |
|---|---|---|---|
| `github.com/coder/websocket` | v1.8.15 | ISC | 事件流 WebSocket |
| `github.com/inconshreveable/mousetrap` | v1.1.0 | Apache-2.0 | cobra 在 Windows 上的间接依赖 |
| `github.com/oapi-codegen/runtime` | v1.7.0 | Apache-2.0 | 生成的 server 代码的运行时支撑（参数绑定等） |
| `github.com/spf13/cobra` | v1.10.2 | Apache-2.0 | CLI 子命令框架 |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | oapi-codegen/runtime 的间接依赖 |
| `github.com/remyoudompheng/bigfft` | v0.0.0-20230129092748 | BSD-3-Clause | modernc.org/sqlite 的间接依赖 |
| `github.com/spf13/pflag` | v1.0.9 | BSD-3-Clause | cobra 的间接依赖 |
| `golang.org/x/sys` | v0.48.0 | BSD-3-Clause | 平台系统调用（命名管道、DPAPI、ACL 等） |
| `golang.org/x/net` | v0.59.0 | BSD-3-Clause | IDNA 与 publicsuffix（根域名识别） |
| `golang.org/x/text` | v0.42.0 | BSD-3-Clause | 国际化文本处理 |
| `modernc.org/libc` | v1.77.1 | BSD-3-Clause | modernc.org/sqlite 的间接依赖 |
| `modernc.org/mathutil` | v1.7.1 | BSD-3-Clause | modernc.org/sqlite 的间接依赖 |
| `modernc.org/memory` | v1.12.1 | BSD-3-Clause | modernc.org/sqlite 的间接依赖 |
| `modernc.org/sqlite` | v1.60.1 | BSD-3-Clause | **嵌入式数据库（纯 Go，无 cgo）** |
| `github.com/apapsch/go-jsonmerge/v2` | v2.0.0 | MIT | oapi-codegen/runtime 的间接依赖 |
| `github.com/dustin/go-humanize` | v1.0.1 | MIT | modernc.org/sqlite 的间接依赖 |
| `github.com/mattn/go-isatty` | v0.0.24 | MIT | modernc.org/sqlite 的间接依赖 |
| `github.com/Microsoft/go-winio` | v0.6.2 | MIT | Windows 命名管道 |
| `github.com/ncruces/go-strftime` | v1.0.0 | MIT | modernc.org/sqlite 的间接依赖 |
| `gopkg.in/yaml.v3` | v3.0.1 | MIT | 配置导入导出 |

### 关于 SQLite 驱动的重要说明

选 `modernc.org/sqlite` 而不是更常见的 `mattn/go-sqlite3`，是因为后者需要 cgo，
而本项目**禁止 cgo**（见 `docs/PLAN.md` §0）。代价是依赖链长了一些
（多了 libc / mathutil / memory / bigfft / humanize / isatty / strftime 六个
间接依赖），但换来的是三平台交叉编译完全不需要 C 工具链 —— 这个交换是值得的。

### 兼容性结论

| 许可证 | 与 GPL-3.0 兼容 | 说明 |
|---|---|---|
| Apache-2.0 | ✅ | 与 GPL-3.0 兼容（**不**兼容 GPL-2.0，本项目不受影响） |
| BSD-3-Clause | ✅ | 宽松许可证，可再许可 |
| MIT | ✅ | 宽松许可证，可再许可 |
| ISC | ✅ | 功能上等价于 MIT |

**结论：全部 18 个依赖与 GPL-3.0 兼容，无许可证冲突。**

### 关于"未参与编译"的模块

`go list -m all` 会列出约 90 个模块，其中包含 `gin`、`iris`、`echo` 等 web 框架。
它们**不是本项目的依赖**，而是 `github.com/oapi-codegen/runtime` 这个模块
自身的测试依赖，出现在模块图中但不会进入任何构建产物。

审计只针对 `go list -deps ./cmd/isc` 报告的模块。已用
`go list -deps github.com/oapi-codegen/runtime` 确认这些框架不在编译范围内。

### 构建期工具（不进入产物）

| 工具 | 版本 | 许可证 |
|---|---|---|
| `github.com/oapi-codegen/oapi-codegen/v2` | v2.8.0 | Apache-2.0 |

通过 `go run ...@${版本}` 调用（版本固定在 `scripts/tool-versions.env`），
**不写入 `go.mod`**，因此不进入本模块的依赖图，也不影响发布产物。

---

## 3. 审计方法

```bash
# 1. 列出真正参与编译的模块（而非整个模块图）
go list -deps -f '{{if .Module}}{{.Module.Path}} {{.Module.Version}} {{.Module.Dir}}{{end}}' ./cmd/isc \
  | sort -u

# 2. 逐个读取其模块目录下的 LICENSE / LICENCE / COPYING 文件
#    （模块缓存路径可由上面的 {{.Module.Dir}} 直接得到）

# 3. 确认许可证类型，并与 GPL-3.0 兼容性表比对
```

**CI 强制执行的检查**

1. `CGO_ENABLED=0 go build ./...` 在三平台 × 两架构全部通过 ——
   任何引入 cgo 的依赖都会直接失败（见 `docs/PLAN.md` §0）；
2. `gofmt` 检查、`go vet`、三平台 `go test`；
3. `internal/api/gen` 与 `api/openapi.yaml` 的同步检查。

**尚未自动化、需要人工把关的部分**

- 新增依赖时手工登记到本文件；
- 许可证兼容性的人工判断（目前依赖数量少，可行；若依赖增长到 30+ 应引入
  `go-licenses` 或 `lichen` 做自动检查）。
