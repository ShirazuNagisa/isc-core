package reach

import (
	"sort"
	"sync"
)

// Registry 是可达方式的注册表。
//
// # 为什么需要注册表而不是一个固定列表
//
// 可达方式会持续增加（IPv6 直连 → frp → cloudflared → Tailscale……），
// 而每一种都有自己的配置、外部依赖与失败模式。把它们硬编码进
// 调用点会让"新增一种方式"变成一次跨文件的改动。
//
// 注册表还承担一件更重要的事：**让界面能列出全部选项**。
// 用户在 IPv6 走不通时需要看到"还有别的路"，而不是一个
// "无法访问"的死胡同。
type Registry struct {
	mu    sync.RWMutex
	byKey map[string]Provider
	order []string
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{byKey: make(map[string]Provider)}
}

// Register 登记一种可达方式。
//
// 重复登记同名插件会覆盖并在顺序里保持首次出现的位置 ——
// 覆盖而不是报错，是为了让测试能够替换真实实现。
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := p.Meta().Name
	if _, exists := r.byKey[name]; !exists {
		r.order = append(r.order, name)
	}
	r.byKey[name] = p
}

// Get 按名称取出一种可达方式。
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byKey[name]
	return p, ok
}

// List 返回全部已登记的方式。
//
// 排序规则：先按 Tier 升序（成熟实现在前），再按名称。
//
// **不按"推荐顺序"排**是刻意的：推荐哪一种是产品决策，会随运营商策略
// 与用户处境变化；把它硬编码在这里会让界面无法表达"当前这台机器上
// 哪种更合适"。界面应当结合 Probe 的结果自己决定怎么呈现。
func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Provider, 0, len(r.order))
	for _, name := range r.order {
		if p, ok := r.byKey[name]; ok {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		mi, mj := out[i].Meta(), out[j].Meta()
		if mi.Tier != mj.Tier {
			return mi.Tier < mj.Tier
		}
		return mi.Name < mj.Name
	})
	return out
}

// Len 返回已登记的数量。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byKey)
}
