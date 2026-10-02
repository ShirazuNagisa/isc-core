package settings

import (
	"context"
	"testing"
)

// TestOnChangeFiresWithNewSettings 验证更新回调。
//
// # 它为什么存在
//
// 有些设置项的生效方式不在本包能力范围内 —— 例如语言要调
// i18n.SetDefault。真机上确认过那个缺口的后果：把语言改成 English 之后，
// 命令行立刻变了（CLI 每次都重新读设置），而**服务端生成的内容仍是中文**
// （例如 /v1/providers 里的凭据字段标签），用户唯一的办法是重启内核。
func TestOnChangeFiresWithNewSettings(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)

	var got []Settings
	svc.SetOnChange(func(s Settings) { got = append(got, s) })

	en := LangEn
	if _, err := svc.Update(context.Background(), Patch{Lang: &en}); err != nil {
		t.Fatalf("更新失败: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("回调应当被调用 1 次，实际 %d 次", len(got))
	}
	if got[0].Lang != LangEn {
		t.Errorf("回调收到的是旧值：%q", got[0].Lang)
	}
}

// TestOnChangeNotFiredOnValidationError 钉住顺序。
//
// 回调放在校验之后：一次**失败**的保存不该留下已经生效的副作用。
func TestOnChangeNotFiredOnValidationError(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)

	fired := 0
	svc.SetOnChange(func(Settings) { fired++ })

	// 一个非法的语言值。
	bad := "klingon"
	if _, err := svc.Update(context.Background(), Patch{Lang: &bad}); err == nil {
		t.Fatal("非法语言应当被拒绝")
	}

	if fired != 0 {
		t.Errorf("校验失败时不该触发回调，实际 %d 次", fired)
	}
}

// TestOnChangeIsSynchronous 钉住"同步调用"。
//
// 异步化会让"改完设置立刻发一个请求"看到旧值 —— 而那正是用户会做的事
// （改语言，然后跑一条命令）。
func TestOnChangeIsSynchronous(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)

	seen := ""
	svc.SetOnChange(func(s Settings) { seen = s.Lang })

	en := LangEn
	if _, err := svc.Update(context.Background(), Patch{Lang: &en}); err != nil {
		t.Fatal(err)
	}

	// Update 返回时回调必须**已经跑完**。
	if seen != LangEn {
		t.Errorf("Update 返回时回调还没生效（%q）—— 它被异步化了", seen)
	}
}

func TestNoOnChangeIsFine(t *testing.T) {
	t.Parallel()

	// 没设回调时更新照常工作 —— 那是 CLI 侧的用法（它不装配守护进程）。
	svc := newTestService(t)
	en := LangEn
	if _, err := svc.Update(context.Background(), Patch{Lang: &en}); err != nil {
		t.Fatalf("未设回调时更新不该失败: %v", err)
	}
}

// TestOnChangeCanBeReplaced 验证回调可以被替换。
//
// 装配顺序上可能出现"先设一个，再换成完整的"。
func TestOnChangeCanBeReplaced(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)

	first, second := 0, 0
	svc.SetOnChange(func(Settings) { first++ })
	svc.SetOnChange(func(Settings) { second++ })

	en := LangEn
	if _, err := svc.Update(context.Background(), Patch{Lang: &en}); err != nil {
		t.Fatal(err)
	}

	if first != 0 {
		t.Errorf("被替换掉的回调不该再被调用（%d 次）", first)
	}
	if second != 1 {
		t.Errorf("新回调应当被调用 1 次，实际 %d 次", second)
	}
}

// newTestService 构造一个只在内存里的设置服务。
//
// 传 nil 存储：Load 会退回到默认值，而 Update 不落库 —— 这些测试
// 关心的只是"回调什么时候被调用"，与持久化无关。
func newTestService(t *testing.T) *Service {
	t.Helper()

	svc, err := Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("构造设置服务失败: %v", err)
	}
	return svc
}
