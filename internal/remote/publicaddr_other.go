//go:build !darwin && !linux

package remote

import (
	"errors"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// localScopedAddresses 在未实现的平台上**失败**，而不是退回
// `net.Interfaces()`。
//
// 理由：Go 的 `net` 包不给出地址的来历（临时 / 稳定），于是我们只能
// 在所有全局地址里随便挑一个 —— 而"随便挑"在开了隐私扩展的机器上
// 有一半以上的概率挑中一条几小时后就不存在的临时地址。
//
// 那条记录的症状是"域名昨天还能用，今天连不上了"，而用户什么都没改；
// 并且它**偶尔能连上**（轮换前），于是看起来像网络抖动。
//
// 报"这个平台不支持"会让用户手填一个地址 —— 他会填对，因为他能从
// 系统界面里看到哪个是哪个。
func localScopedAddresses() ([]ScopedAddress, error) {
	return nil, errors.New(i18n.T("remote.public.err.unsupported_platform"))
}
