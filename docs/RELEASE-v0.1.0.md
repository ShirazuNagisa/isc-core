# ISC-Core v0.1.0

**发布日期**：2026-10-03
**许可证**：GPL-3.0（同一内核被 GUI 以**库**的形式链接；GUI 同样以 GPLv3 开源）

---

## 这一版是什么

内核的第一个可用版本。它把"无公网 IPv4 的家用电脑也能从公网访问"这件事
做成一套可用的内核能力，并且**同时以两种形态交付**：

| 形态 | 用途 |
|---|---|
| **可调用库**（`libisc.dylib` / 未来 `isc.dll`） | GUI 直接链接调用：Swift（macOS）、C#（Windows）。接口文档见 [`LIBRARY-API.md`](./LIBRARY-API.md) |
| **守护进程 + CLI**（`isc`） | 库的第一个消费者，也是目前唯一被完整验证过的消费者；也用于把内核装成系统服务 |

## 验证状态（v0.1.0 的验收口径：**这台 Mac**）

| 项 | 结果 |
|---|---|
| CI（20 个 job） | ✅ 全绿：三平台测试、Linux race、7 目标编译矩阵、**真实安装包核对**（`dpkg -i` / `rpm -i` + 查询 + 卸载 / `installer -pkg`）、可复现构建、i18n、契约同步、**内核库 + Swift 冒烟**、**systemd 与 launchd 真实服务安装** |
| 本机功能验收（不需 root） | ✅ 内核起停、六个平台后端全部可用（pf / polling / darwin-native / macos-keychain / launchd / unix-socket）、数据目录 0700、套接字与 runtime.json 0600、doctor 认出 IPv6 直连条件 |
| 本机功能验收（需 root） | ✅ 16/16：launchd 服务真装真起（`running=true`、`health=ok`）、pf 真的放行（规则在内核里可见）、撤销后规则消失 |
| 内核库 | ✅ Swift 6.4 链接 `libisc.dylib`，**进程内**启停内核、调契约接口、拉事件、拿到机器可判别的错误码 |

复跑方式：

```sh
# 库 + Swift 端到端
examples/swift-smoke/run.sh

# 需要 root 的本机验收（会自动复原）
CGO_ENABLED=0 go build -o /tmp/isc-acc/isc ./cmd/isc
sudo scripts/acceptance-macos-sudo.sh /tmp/isc-acc/isc
```

## 产物：只有可链接的库

**2026-10-03 项目主决定：安装包不再是发布产物。** 发布里只保留 GUI 要链接的库：

| 产物 | 说明 |
|---|---|
| `libisc.dylib` | 内核库本体（macOS；Windows 上是 `isc.dll`） |
| `libisc.h` | 头文件：Swift 用 bridging header，C# 用 `DllImport` |
| `SHA256SUMS` | 上面两个文件的 SHA-256 |

构建（也是发布流程）：

```sh
scripts/build-libisc.sh            # → dist/libisc.dylib + libisc.h + SHA256SUMS
```

版本由 `git describe --tags` 注入，因此发布产物会自报版本号
（`isc_version_json` 里能看到 `v0.1.0` 与对应 commit）。

**被打包代码没有被删掉**：`.deb` / `.rpm` / `.pkg` / `.msi` 与各平台归档仍可由
`go run ./scripts/release -out dist -version X` 构建（有人要自己分发时可以用），
只是**不再进发布**。

## 已知限制（刻意记录，不掩盖）

1. **`.pkg` 不是逐字节可复现**（xar TOC 的 creation-time / inode / uid 等），
   但载荷与 Bom 逐字节一致。
2. **撤销不恢复 pf 的启用状态**：验收前是 Disabled，撤销规则后规则确实清干净，
   但 pf 仍处于 Enabled。影响很小（只有 Apple 默认规则时几乎无副作用），
   经评估**暂不修改**。
3. **代码签名与公证**：`.pkg` / `.dylib` 未签名（需要证书）。用户首次打开
   需要右键"打开"或在"隐私与安全性"里放行。
4. **Windows 的库未验证**：`isc.dll` 的构建与 C# `DllImport` 需要在 Windows
   机器上验证（本机没有 .NET，也没有 MinGW/MSVC）。
5. **Tier-1/Tier-2 服务商的真实 API 调用、通知通道的真实端点、ACME 对真实
   域名的签发**都需要凭据/域名，未在 v0.1.0 的验收范围内。

## 快速开始

```sh
# 起内核
isc daemon run &

# 看状态（含六个平台后端的可用性）
isc status

# 生成一份"放行端口"的变更计划（确认后才应用；--json 适合脚本）
isc expose --port 8080
isc expose --port 8080 --yes        # 应用（需要 root：pf 要写 /etc）

# 装成系统服务（需要 root）
sudo isc service install
```

GUI 侧：见 [`LIBRARY-API.md`](./LIBRARY-API.md)（`isc_start` / `isc_status_json` /
`isc_call` / `isc_events_json` / `isc_stop`）。
