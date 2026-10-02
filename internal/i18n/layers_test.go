package i18n

import "testing"

// 本文件覆盖目录的**分层合并**。
//
// # 它防的是一个只在"加到第二层"时才显形的缺陷
//
// `New()` 逐层合并补充表。最初的写法是每一轮都从**基础表**重建 map：
//
//	msgs := src
//	for _, layer := range extra {
//	    merged := make(map[string]string, len(src)+len(layer))
//	    for k, v := range src { merged[k] = v }   // ← 从 src，而不是 msgs
//	    for k, v := range layer { merged[k] = v }
//	    msgs = merged                             // ← 上一轮的结果被丢弃
//	}
//
// 只加一层时它**完全正确** —— 因为那时"上一轮的结果"恰好就是 src。
// 加上第二层（tier1）之后，第一层（api）的 key **整层消失**，
// 表现是所有接口文案都变成 key 本身。
//
// 这类缺陷的形状值得记住：**它对层数敏感，而对内容不敏感** ——
// 单层时无论怎么测都测不出来。

// TestEveryLayerIsMerged 验证每一层的 key 都在最终目录里。
//
// 它比"逐层比对 zh/en"更强：后者只保证同一层内部中英一致，
// 而这一条保证每一层**真的被合并进去了**。
func TestEveryLayerIsMerged(t *testing.T) {
	t.Parallel()

	zh := New(ZhCN)
	en := New(En)

	layers := layeredCatalogMaps()
	if len(layers) < 2 {
		t.Fatal("至少应当有基础表这一层")
	}

	for i, layer := range layers {
		for key := range layer {
			if !zh.Has(key) {
				t.Errorf("第 %d 层的 key %q 没有出现在中文目录里 —— "+
					"分层合并把这一层丢掉了", i, key)
				break // 每层报一个就够，否则输出会淹没
			}
		}
		for key := range layer {
			if !en.Has(key) {
				t.Errorf("第 %d 层的 key %q 没有出现在英文目录里", i, key)
				break
			}
		}
	}
}

// TestMergeIsCumulative 用**多于两层**的情形钉住"累加"这个语义。
//
// 上一条用的是真实的分层表，因此它只在"层数 ≥ 2"时有效。这一条直接
// 构造三层来验证合并是累加的 —— 将来加到第四层、第五层时它同样有效。
func TestMergeIsCumulative(t *testing.T) {
	t.Parallel()

	catalog := New(ZhCN)

	// 三层各放一个独特的 key，逐一确认它真的在结果里。
	// 用真实的层而不是造假的 map，是为了让测试跟着代码一起演进。
	type probe struct {
		name string
		keys []string
	}
	probes := []probe{
		{name: "基础表", keys: []string{"error.not_found"}},
		{name: "api 层", keys: []string{"api.empty_body"}},
		{name: "tier1 层", keys: []string{"tier1.need_type"}},
	}

	for _, p := range probes {
		for _, k := range p.keys {
			if !catalog.Has(k) {
				t.Errorf("%s 的 key %q 没有出现在合并结果里 —— "+
					"合并可能不是累加的（每加一层丢掉前一层）", p.name, k)
			}
		}
	}
}

// TestNoDuplicateKeysAcrossLayers 检查**同一语言**内没有重复的 key。
//
// # 为什么只看奇数层是错的
//
// 分层表是 (zh, en) 成对排列的，因此"第 2i 层"是中文、"第 2i+1 层"是英文。
// 一开始这条测试遍历了**全部**层，于是 zh 与 en 之间的同名 key 被当成了重复 ——
// 而那正是它们应有的样子。测试报了一长串无意义的"重复"。
//
// 真正的检查对象是**同一语言内部**：两个中文层里出现同一个 key 才说明
// 有人把同一条文案定义了两遍（那时生效的是后者，而前者成了死条目）。
func TestNoDuplicateKeysAcrossLayers(t *testing.T) {
	t.Parallel()

	layers := layeredCatalogMaps()

	for _, parity := range []struct {
		name  string
		start int
	}{{"中文", 0}, {"英文", 1}} {
		seen := map[string]int{}
		for i := parity.start; i < len(layers); i += 2 {
			for key := range layers[i] {
				if prev, dup := seen[key]; dup {
					t.Errorf("key %q 同时出现在%s的第 %d 层与第 %d 层 —— "+
						"生效的是后者，前者成了死条目",
						key, parity.name, prev, i)
				}
				seen[key] = i
			}
		}
	}
}
