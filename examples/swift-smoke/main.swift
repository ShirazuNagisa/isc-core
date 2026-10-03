// Swift 侧冒烟测试：链接内核库（libisc.dylib），在**本进程内**启动内核、
// 调契约接口、订阅事件、停止。
//
// 运行：examples/swift-smoke/run.sh
//
// 这个文件既是证据也是用法示例，示范了三件容易做错的事：
//
//   1. 每个返回的 char* 都必须用 isc_free_string 释放（内存由 C 侧分配）；
//   2. 失败时看的是 **code**（机器可判别），不是 error 文案；
//   3. 功能调用统一走 isc_call，路径与方法照 api/openapi.yaml 的契约写。
import Foundation

/// 取走 C 返回的字符串并释放它。
func take(_ p: UnsafeMutablePointer<CChar>?) -> String {
    guard let p else { return "(null)" }
    defer { isc_free_string(p) }
    return String(cString: p)
}

func json(_ s: String) -> [String: Any]? {
    guard let d = s.data(using: .utf8) else { return nil }
    return (try? JSONSerialization.jsonObject(with: d)) as? [String: Any]
}

/// 调一次契约接口并打印结果。
@discardableResult
func call(_ method: String, _ path: String, _ body: String? = nil) -> [String: Any]? {
    let res = take(isc_call(strdup(method), strdup(path), body.map { strdup($0) }))
    guard let obj = json(res) else {
        print("  \(method) \(path) → 无法解析: \(res.prefix(200))")
        return nil
    }
    if obj["ok"] as? Bool == false {
        // 关键：分支看 code，不看文案。
        print("  \(method) \(path) → ❌ code=\(obj["code"] ?? "?") (\(obj["error"] ?? ""))")
    } else {
        let bodyText = obj["body"].map { String(describing: $0) } ?? ""
        print("  \(method) \(path) → ✅ \(obj["status"] ?? "?") \(bodyText.prefix(90))")
    }
    return obj
}

print("接口版本:", take(isc_api_version()))
if let v = json(take(isc_version_json())) {
    print("内核版本: \(v["version"] ?? "?")  commit=\(v["commit"] ?? "?")")
}

let dataDir = NSTemporaryDirectory() + "isc-swift-smoke"
print("\n启动内核（数据目录 \(dataDir)）…")
if let started = json(take(isc_start(strdup(dataDir)))) {
    print("  endpoint = \(started["endpoint"] ?? "?")")
}

print("\n状态（便捷函数，health + meta 合成）：")
if let st = json(take(isc_status_json())),
   let health = st["health"] as? [String: Any],
   let meta = st["meta"] as? [String: Any] {
    print("  health  = \(health["status"] ?? "?")")
    if let caps = meta["capabilities"] as? [String: Any] {
        for (name, v) in caps.sorted(by: { $0.key < $1.key }) {
            let c = v as? [String: Any]
            print("  \(name): available=\(c?["available"] ?? "?") backend=\(c?["backend"] ?? "?")")
        }
    }
}

print("\n通用调用（isc_call）—— 路径与方法一律照契约：")
call("GET", "/v1/settings")
call("GET", "/v1/providers")
call("GET", "/v1/changes")
call("GET", "/v1/nope")            // 故意错：应当回 code=not_found

print("\n事件（游标式长轮询，300ms 超时）：")
if let ev = json(take(isc_events_json(0, 300))),
   let events = ev["events"] as? [Any] {
    print("  events=\(events.count) next=\(ev["next"] ?? "?") gap=\(ev["gap"] ?? "?")")
}

print("\n停止：\(take(isc_stop()))")
print("再停一次（应当幂等）：\(take(isc_stop()))")
