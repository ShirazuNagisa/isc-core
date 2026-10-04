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

func (unsupportedSource) Host(context.Context) (HostSample, error) {
	return HostSample{}, errUnsupported
}

func (unsupportedSource) Processes(context.Context, []int) (map[int]ProcessSample, error) {
	return nil, errUnsupported
}
