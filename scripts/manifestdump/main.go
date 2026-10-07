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
// 输出每行十列，制表符分隔：
//
//	kind、version、archive、sizeBytes、digest、url、executable、stripRoot、license、source
//
// 前六列的历史顺序**不能改**：ISC-Phecda 的打包脚本按列号取值。后四列是
// 构建期解压运行时需要的（去哪找可执行文件、要不要剥顶层目录、许可与来源
// 要登记进包内说明）。
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
		fmt.Printf("%s\t%s\t%s\t%d\t%s\t%s\t%s\t%t\t%s\t%s\n",
			artifact.Kind, artifact.Version, artifact.Archive,
			artifact.SizeBytes, artifact.Digest, artifact.URL,
			artifact.Executable, artifact.StripRoot, artifact.License, artifact.Source)
	}
}
