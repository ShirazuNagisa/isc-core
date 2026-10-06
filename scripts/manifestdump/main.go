// manifestdump 按 TSV 列出运行时清单里某个平台的固定发行版。
//
// 存在的理由是**打包脚本不该去解析 Go 源码**：清单是代码常量（理由见
// internal/runtime/manifest.go），而用 grep/sed 从里面抠 URL 与摘要在格式
// 稍变时就会静默取错 —— 而取错的表现是"内置了但校验不过"，很难查。
//
// 用法：
//
//	go run ./Scripts/manifestdump darwin/arm64
//
// 输出每行六列：kind、version、archive、sizeBytes、digest、url
package main

import (
	"fmt"
	"os"
	goruntime "runtime"

	"github.com/ShirazuNagisa/isc-core/internal/runtime"
)

func main() {
	platform := goruntime.GOOS + "/" + goruntime.GOARCH
	if len(os.Args) > 1 {
		platform = os.Args[1]
	}
	for _, kind := range runtime.Kinds() {
		artifact, ok := runtime.ArtifactFor(kind, platform)
		if !ok {
			continue
		}
		fmt.Printf("%s\t%s\t%s\t%d\t%s\t%s\n",
			artifact.Kind, artifact.Version, artifact.Archive,
			artifact.SizeBytes, artifact.Digest, artifact.URL)
	}
}
