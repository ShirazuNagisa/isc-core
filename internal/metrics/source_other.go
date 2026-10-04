//go:build !darwin && !linux

package metrics

import "context"

// unsupportedSource 用于尚未实现采样的平台。
//
// 它**总是返回错误**而不是零值：零看起来像"什么都没占用"，
// 而上层据此显示的应该是"此平台不支持"。
type unsupportedSource struct{}

func newPlatformSource() Source { return unsupportedSource{} }

func (unsupportedSource) Describe() string { return "unsupported" }

// Host 在未实现的平台上总是失败，因此磁盘与 GPU 也一并缺席：
// Disks 为空、GPU 为 nil。这与上层"整块指标不支持"的显示一致 ——
// 这里刻意不去拼一份"CPU 有值、磁盘没有"的半个样本，
// 那种样本会让界面上一半数字有、一半数字没有，而原因只是平台不支持。
func (unsupportedSource) Host(context.Context) (HostSample, error) {
	return HostSample{}, errUnsupported
}

func (unsupportedSource) Processes(context.Context, []int) (map[int]ProcessSample, error) {
	return nil, errUnsupported
}
