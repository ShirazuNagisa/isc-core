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
| `internal/dns/providers/tier2/` | `dns/*.go`（约 30 家服务商） |
| `internal/dns/signers/` | `util/aliyun_signer.go`、`huawei_signer.go`、`tencent_cloud_signer.go`、`baidu_signer.go`、`traffic_route_signer.go` |
| `internal/dns/ipcache/` | `util/ip_cache.go` |
| `internal/netx/` | `util/net.go`、`net_resolver.go`、`http_client_util.go` |
| `internal/ipmon/source/` | `config/config.go` 中的 IP 获取逻辑 |
| `internal/notify/channels/webhook.go` | `config/webhook.go` |

**已做的修改**（详见各文件头部注释）：

- 替换包路径与导入路径；
- **剥离包级全局可变状态**（`util.ForceCompareGlobal`、`dns.Ipcache`、`config` 包级缓存单例），改为依赖注入
  —— 这是必须的，否则并发场景下会产生难以复现的数据竞争（见 `docs/PLAN.md` R11）；
- 日志输出改为 ISC 的结构化日志接口（`log/slog`）；
- 用户可见字符串改走 ISC 的 i18n 消息目录。

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

审计时间：M0 阶段。审计命令见 §3。

| 模块 | 版本 | 许可证 | 用途 |
|---|---|---|---|
| `github.com/coder/websocket` | v1.8.15 | ISC | 事件流 WebSocket |
| `github.com/inconshreveable/mousetrap` | v1.1.0 | Apache-2.0 | cobra 在 Windows 上的间接依赖 |
| `github.com/oapi-codegen/runtime` | v1.7.0 | Apache-2.0 | 生成的 server 代码的运行时支撑（参数绑定等） |
| `github.com/spf13/cobra` | v1.10.2 | Apache-2.0 | CLI 子命令框架 |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | oapi-codegen/runtime 的间接依赖 |
| `github.com/spf13/pflag` | v1.0.9 | BSD-3-Clause | cobra 的间接依赖 |
| `golang.org/x/sys` | v0.48.0 | BSD-3-Clause | 平台系统调用（命名管道、DPAPI、netlink 等） |
| `github.com/apapsch/go-jsonmerge/v2` | v2.0.0 | MIT | oapi-codegen/runtime 的间接依赖 |
| `github.com/Microsoft/go-winio` | v0.6.2 | MIT | Windows 命名管道 |

### 兼容性结论

| 许可证 | 与 GPL-3.0 兼容 | 说明 |
|---|---|---|
| Apache-2.0 | ✅ | 与 GPL-3.0 兼容（**不**兼容 GPL-2.0，本项目不受影响） |
| BSD-3-Clause | ✅ | 宽松许可证，可再许可 |
| MIT | ✅ | 宽松许可证，可再许可 |
| ISC | ✅ | 功能上等价于 MIT |

**结论：全部依赖与 GPL-3.0 兼容，无许可证冲突。**

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
