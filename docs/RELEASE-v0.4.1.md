# ISC-Core v0.4.1

**发布日期**：2026-10-04
**许可证**：GPL-3.0（同一内核被 GUI 以**库**的形式链接；GUI 同样以 GPLv3 开源）

---

## 这一版是什么

一次**口径**的修订，不是新功能。它把"这套东西自己占了多少"变成内核
能回答的问题，并修掉两处会让数字骗人的地方。

`APIVersion` 保持 `v2`：新增字段，没有破坏性变更。

---

## 新增：footprint

`GET /v1/metrics` 的响应里多了一个 `footprint`：**内核自身 + 它托管的
站点**（含进程树）的 CPU、内存、网络。它与原有的 `host`（整台机器）并列。

界面拿它当主数字之后，"机器很卡"与"Phecda 吃了多少"才成为两个能被
分别回答的问题。三条边界写在 `api/openapi.yaml` 的 `FootprintMetrics`
上，理由见 `docs/DECISIONS.md` D39：

- 含**后代**进程 —— 预设里的 `npm start` 会再 fork 出 node；
- 内存是各进程 RSS 之和，是上界不是精确值；
- CPU 是各进程之和，多核机器上**可能超过 100**（180% 读作 1.8 个核）。

**GPU 不在 footprint 里。** macOS 没有按进程归因 GPU 的途径，把设备数字
放进去就是冒充。GPU 仍由 `host.gpu` 提供，由界面标注"整机 GPU"。

## 新增：按进程的网络归因

`footprint.net_rx_bytes_per_sec` / `net_tx_bytes_per_sec` 来自 macOS 的
`nettop`，按 PID 求和后差分出速率。

`net_backend` 有三种取值，界面必须分开处理：

| 取值 | 含义 |
|---|---|
| `darwin-nettop` | 数字有效 |
| `unsupported` | 这个平台没有进程级网络采样 |
| `unavailable` | 实现了，但这一次没读到 |

速率字段只在有效时才发出 —— 把"没读到"显示成 0，和把"不支持"显示成 0
是同一类错误。

## 修掉的两处

1. **`nettop` 不带 `-n` 会永远卡住。** 它会反向解析每个远端地址，在有
   活动连接时根本不返回（实测 >30 秒）。症状是静默的：命令被杀、输出为
   空、网络永远显示"没读到"。现在固定带 `-n`，并有一条断言"够快"的
   真机测试守着。
2. **计数回退会被算成负速率。** 站点进程重启后累计字节归零，直接差分
   会得到一条巨大的负速率。现在钳到 0。

## 兼容性

- `APIVersion` 仍是 `v2`；旧客户端（Phecda 0.4.0 / Mizar 0.2.0）不受影响 ——
  新增字段对 `Decodable` 透明。
- C ABI **没有变化**：9 个导出函数一个都没动，新能力全部走 REST（D24）。
- 采样成本每轮多约 0.5 秒（一次 `ps -A` + 一次 `nettop`）。

## 验证

```
go test ./... -timeout 600s
make check-fmt && make vet && make check-gen
CGO_ENABLED=1 go test -race ./internal/metrics/ ./internal/api/
```

真机测试（`internal/metrics/source_darwin_test.go`）额外守住两件事：
进程树必须包含内核自身，nettop 必须明显快于采样间隔。
