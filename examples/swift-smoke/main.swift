// Swift 侧冒烟测试：链接内核库（libisc.dylib），在本进程内启动内核并读状态。
//
// 运行：examples/swift-smoke/run.sh
//
// 这个文件是**证据**也是**用法示例**：它证明了"Go 内核 → C ABI → Swift"
// 这条链路在本机真的通，同时示范了两件容易做错的事：
//
//   1. 每个返回的 char* 都必须用 isc_free_string 释放（内存由 C 侧分配）；
//   2. 请求的 Host 必须是回环地址 —— 内核有 DNS rebinding 防护，
//      用别的 Host 会被它挡下（那是它该做的事，不要去放宽内核）。
import Foundation

/// 取走 C 返回的字符串并释放它。
func take(_ p: UnsafeMutablePointer<CChar>?) -> String {
    guard let p else { return "(null)" }
    defer { isc_free_string(p) }
    return String(cString: p)
}

func pretty(_ json: String, keys: [String]) {
    guard let data = json.data(using: .utf8),
          let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
        print("  (无法解析) \(json.prefix(300))")
        return
    }
    if obj["ok"] as? Bool == false {
        print("  ❌ \(obj["error"] ?? "未知错误")")
        return
    }
    for k in keys {
        print("  \(k) = \(obj[k] ?? "?")")
    }
}

print("接口版本:", take(isc_api_version()))
print("内核版本:", take(isc_version_json()))

// 数据目录用临时目录，避免动到真实安装的数据（那里有主密钥）。
let dataDir = NSTemporaryDirectory() + "isc-swift-smoke"
print("\n启动内核（数据目录 \(dataDir)）…")
pretty(take(isc_start(strdup(dataDir))), keys: ["endpoint", "data_dir"])

let status = take(isc_status_json())
if let data = status.data(using: .utf8),
   let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
   let health = obj["health"] as? [String: Any],
   let meta = obj["meta"] as? [String: Any] {
    print("\n状态：")
    print("  health  = \(health["status"] ?? "?")")
    print("  version = \(meta["version"] ?? "?")")
    if let caps = meta["capabilities"] as? [String: Any] {
        for (name, v) in caps.sorted(by: { $0.key < $1.key }) {
            let c = v as? [String: Any]
            print("  \(name): available=\(c?["available"] ?? "?") backend=\(c?["backend"] ?? "?")")
        }
    }
} else {
    print("\n状态读取失败：\(status.prefix(300))")
}

print("\n停止：\(take(isc_stop()))")
print("再停一次（应当幂等）：\(take(isc_stop()))")
