# 内核库接口（libisc）

> 内核的交付形态是**可调用库**：GUI（macOS 用 Swift、Windows 用 C#）链接它，
> 通过函数调用使用内核功能。`isc daemon` 与 `isc` CLI 是同一个内核的另一个
> 消费者。为什么是 C ABI、接口为什么用 JSON，见 [`DECISIONS.md`](./DECISIONS.md) D24。

## 构建产物

| 平台 | 命令 | 产物 |
|---|---|---|
| macOS | `scripts/build-libisc.sh` | `dist/libisc.dylib` + `dist/libisc.h` |
| Windows | `set CGO_ENABLED=1 && go build -buildmode=c-shared -o isc.dll ./cmd/libisc` | `isc.dll` + `isc.h`（需要 MinGW-w64 或 MSVC） |

库需要 cgo（这是 `c-shared` 的硬要求），但**只有这一个目标需要**：内核本体、
CLI 与守护进程仍然零 cgo，`CGO_ENABLED=0` 下的构建与测试完全不受影响。

## 代码分层

| 位置 | 内容 | 依赖 cgo |
|---|---|---|
| `internal/libisc` | 全部行为（生命周期、派发、事件、错误码） | ❌ 不依赖 —— 于是在**默认 CI**（`CGO_ENABLED=0 go test ./...`）里就有测试覆盖 |
| `cmd/libisc` | cgo 薄包装：C 字符串 ↔ Go 字符串，`//export` | ✅ 唯一需要 cgo 的地方 |

之所以这样分：Go 不允许在 `_test.go` 里 `import "C"`，所以逻辑必须待在普通包里
才测得到。

## 快速开始

Swift（完整可跑的示例见 [`examples/swift-smoke`](../examples/swift-smoke/main.swift)）：

```bash
swiftc -import-objc-header libisc.h main.swift -L . -lisc \
  -Xlinker -rpath -Xlinker "$(pwd)" -o gui
```

```swift
func take(_ p: UnsafeMutablePointer<CChar>?) -> String {   // 每个返回值都要释放
    guard let p else { return "{}" }
    defer { isc_free_string(p) }
    return String(cString: p)
}

let started = take(isc_start(strdup(dataDir)))      // 就绪后才返回
let status  = take(isc_status_json())               // health + meta
let creds   = take(isc_call(strdup("GET"), strdup("/v1/credentials"), nil))
```

C#：

```csharp
using System.Runtime.InteropServices;

static class Isc {
    [DllImport("isc", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr isc_start(string dataDir);

    [DllImport("isc", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr isc_call(string method, string path, string body);

    [DllImport("isc", CallingConvention = CallingConvention.Cdecl)]
    public static extern void isc_free_string(IntPtr p);
}

var started = Marshal.PtrToStringUTF8(Isc.isc_start(dataDir));   // 用完必须 free
```

## 接口

| 函数 | 说明 |
|---|---|
| `isc_api_version() → char*` | 接口版本（形如 `v1`）。**GUI 启动时应先比对**，不一致要提示 |
| `isc_version_json() → char*` | 内核版本信息 |
| `isc_start(dataDir) → char*` | 在本进程内启动内核；**完全就绪后才返回**。`dataDir` 传 NULL/空串表示用默认目录 |
| `isc_stop() → char*` | 停止并等待退出；**幂等**（没在跑时也返回 ok，`stopped=false`） |
| `isc_restart(dataDir) → char*` | 停止后再启动 |
| `isc_status_json() → char*` | 便捷函数：`health` + `meta` 合成一份 |
| `isc_call(method, path, body) → char*` | **核心**：调用契约里的任意路径（见下） |
| `isc_events_json(sinceSeq, timeoutMs) → char*` | 事件订阅（游标式长轮询，无 C 回调） |
| `isc_free_string(p) → void` | 释放上面每个返回值 |

### `isc_call`：功能面就是契约

路径与方法一律照 [`api/openapi.yaml`](../api/openapi.yaml) 写 —— 契约有
spec-drift 检查守着，因此**新增功能不需要改库**：内核加了接口，GUI 直接调即可。

```
isc_call("GET",  "/v1/health", NULL)
isc_call("GET",  "/v1/settings", NULL)
isc_call("PATCH","/v1/settings", "{\"lang\":\"en\"}")
isc_call("POST", "/v1/credentials", body)
isc_call("GET",  "/v1/credentials/<id>/zones", NULL)
isc_call("POST", "/v1/changes/<planId>/apply", NULL)
isc_call("POST", "/v1/certs/renew", NULL)
```

契约里现有 38 条路径（`/v1/health`、`/v1/meta`、`/v1/settings`、`/v1/providers`、
`/v1/credentials…`、`/v1/ddns-tasks…`、`/v1/reach/providers…`、`/v1/changes…`
（含 `apply` / `rollback`）、`/v1/proxy/…`、`/v1/certs…`、`/v1/notify/…`、
`/v1/config/export|import`、`/v1/ip/current`、`/v1/jobs…`、`/v1/audit`、
`/v1/verify/sessions…`）。

**注意方法不要凭印象写**：设置是 `PATCH`（不是 `PUT`），字段是 `lang`
（不是 `language`）。本仓库在这两处各错过一次。

## 返回的 JSON 形状

成功：

```json
{"ok": true, "status": 200, "body": { /* 契约里定义的响应体 */ }}
```

失败（**分支看 `code`，不要匹配 `error` 文案** —— 文案会随语言变）：

```json
{"ok": false, "code": "not_found", "status": 404, "error": "内核返回 404：..."}
```

| 错误码 | 含义 |
|---|---|
| `bad_request` | 路径/参数不合法（含方法不存在时的 4xx） |
| `unauthorized` / `forbidden` | 令牌或权限问题（正常使用不应出现） |
| `not_found` | 契约里没有这个路径，或对象不存在 |
| `conflict` | 状态冲突（例如重复启动） |
| `not_running` | 内核没在跑（还没 `isc_start`，或已经 `isc_stop`） |
| `not_ready` | 内核在跑但接口处理器/事件总线还没就绪（罕见） |
| `already_running` | 本进程里已经有一个内核实例 |
| `timeout` | 启动/停止超时 |
| `internal` | 内核内部错误（含 5xx） |

便捷函数与 `isc_call` 的成功形状不同：`isc_start`/`isc_stop` 直接返回
`{"ok":true,"endpoint":...}`；`isc_status_json` 返回 `{"ok":true,"health":{...},"meta":{...}}`；
`isc_events_json` 返回 `{"ok":true,"events":[...],"next":N,"gap":bool}`。

## 事件订阅

```c
char* r = isc_events_json(0, 300);   // 首次：since=0 表示"从现在开始"
// → {"ok":true,"events":[…],"next":42,"gap":false}
r = isc_events_json(42, 300);        // 之后用上次的 next 接着拉
```

- `timeoutMs` 是"没有新事件时最多等多久"，`0` 表示立刻返回。**请在后台线程调用**
  ——它会在没有事件时阻塞住当前线程。
- `gap=true` 表示事件因积压被丢弃（或内核重启过）：这时候**不要试图补齐**，
  重新全量拉一次状态（`isc_status_json` 或相关列表接口）即可。
- 事件里带单调递增的 `seq`；把它当游标保存。

## 内存与线程约定

1. **每个返回的 `char*` 都必须用 `isc_free_string` 释放**，包括出错时返回的。
   返回值由 C 侧分配（`malloc`），语言运行时的 GC 管不到它。
2. 传进去的字符串由**调用方**负责（Swift 的 `strdup` 结果、C# 的临时字符串
   各按本语言惯例处理）；库不会保存它们的引用。
3. **一个进程只能有一个内核实例**：第二次 `isc_start` 返回
   `already_running`。要换数据目录就先 `isc_stop`。
4. 可以从任意线程调用这些函数：库内部有互斥，Go 运行时也支持外部线程进入。
   唯一要避免的是在同一线程里阻塞等待自己触发的回调 —— 本库没有回调，
   因此不存在这个问题（这也是选长轮询而不是 C 回调的原因之一）。

## 打包注意（GUI 侧）

- **macOS**：把 `libisc.dylib` 放进 `App.app/Contents/Frameworks/`，并在可执行
  文件上加 `-rpath @executable_path/../Frameworks`。库的 install name 已经设成
  `@rpath/libisc.dylib`（`scripts/build-libisc.sh` 里设的），因此这一步就够。
  之后 dylib 与 app 都要**一起签名**（`codesign`），否则公证会失败。
- **Windows**：`isc.dll` 放到可执行文件同目录，或系统 PATH 上的目录；
  `DllImport("isc", CallingConvention = CallingConvention.Cdecl)`。**架构必须
  匹配**（x64 的 GUI 只能用 x64 的 DLL）。
- 库是 **GPL-3.0**：链接它的 GUI 构成衍生作品。发布闭源 GUI 前必须先解决
  许可证问题（见 D24 的待定项）。

## 还没做（后续）

- C 回调式的事件推送（现在是长轮询）；
- 事件类型的稳定枚举（目前 `type` 是字符串，取自内核内部事件名）；
- 完整错误码覆盖（现在按 HTTP 状态映射，个别接口的特殊错误会是 `bad_request`）；
- Windows 端的构建与 C# 调用验证（本机没有 .NET，也没有 MinGW/MSVC）。
