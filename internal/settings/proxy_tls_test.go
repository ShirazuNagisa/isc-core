package settings

import (
	"context"
	"testing"
)

// TestProxyTLSIsOnByDefault 钉住"证书自动申请是产品默认"。
//
// 它同时是反代用不用 HTTPS 的开关：默认关着的话，新装的实例既不会去
// 申请证书，用户也没有任何地方能把它打开 —— 界面上那个开关已经被
// 去掉了（见 Default 的注释）。
func TestProxyTLSIsOnByDefault(t *testing.T) {
	t.Parallel()

	svc, err := Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("加载设置失败: %v", err)
	}
	if !svc.Get().ProxyTLS {
		t.Fatal("ProxyTLS 默认应当是 true：证书自动申请已经是产品默认")
	}
}

// TestDefaultTLSDoesNotBlockUnrelatedSettings 钉住默认翻转之后仍然成立
// 的那条底线。
//
// ProxyTLS 默认开着，而邮箱在首次配置之前是空的。若校验只看 ProxyTLS，
// 新装的实例就**连语言都改不了**（同一条路径还挡着改日志级别、从备份
// 恢复设置）—— 那些动作与证书毫无关系。真机上这一条是先从设置接口的
// 400 上看到的。
func TestDefaultTLSDoesNotBlockUnrelatedSettings(t *testing.T) {
	t.Parallel()

	svc, err := Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("加载设置失败: %v", err)
	}

	en := LangEn
	if _, err := svc.Update(context.Background(), Patch{Lang: &en}); err != nil {
		t.Fatalf("新装实例改语言不该被证书相关的校验挡住: %v", err)
	}
	if got := svc.Get().Lang; got != LangEn {
		t.Errorf("语言没有被写入：%q", got)
	}
}

// TestEnablingProxyWithoutACMEEmailIsRejected 钉住守卫**依然在**。
//
// 放宽的只是"反代关着时不必先填邮箱"；真正会去签证书的动作（打开反代）
// 仍然必须能拿到账号邮箱，否则症状是"浏览器报证书错误"。
func TestEnablingProxyWithoutACMEEmailIsRejected(t *testing.T) {
	t.Parallel()

	svc, err := Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("加载设置失败: %v", err)
	}

	on := true
	if _, err := svc.Update(context.Background(), Patch{ProxyEnabled: &on}); err == nil {
		t.Fatal("打开反代（HTTPS）而没有 ACME 邮箱应当被拒绝")
	}

	// 同一个 patch 里给出邮箱是正常路径（界面一次提交整页）。
	email := "ops@example.com"
	if _, err := svc.Update(context.Background(), Patch{
		ProxyEnabled: &on,
		ACMEEmail:    &email,
	}); err != nil {
		t.Fatalf("同一个 patch 里给出邮箱之后应当通过: %v", err)
	}
}

// TestStoredTLSChoiceWinsOverDefault 钉住"默认值翻不掉旧部署的显式选择"。
//
// merge 是"存储里的值覆盖默认值"，因此存过 proxy_tls=false 的部署在
// 升级之后仍然用明文 HTTP —— 退回明文的那个口子留在命令行
// （`isc settings set --no-proxy-tls`）上，而不是被这次默认值改动收走。
func TestStoredTLSChoiceWinsOverDefault(t *testing.T) {
	t.Parallel()

	svc, err := Load(context.Background(), mapStore{
		KeyProxyTLS:  "false",
		KeyACMEEmail: "ops@example.com",
	})
	if err != nil {
		t.Fatalf("加载设置失败: %v", err)
	}
	if svc.Get().ProxyTLS {
		t.Fatal("存储里显式存过 false 的部署不该被新的默认值改成 true")
	}
}

// mapStore 是一份只读的内存设置存储，用来钉住"存储值覆盖默认值"。
//
// SaveSettings 是空实现：这些测试只关心 Load 之后的取值。
type mapStore map[string]string

func (m mapStore) LoadSettings(context.Context) (map[string]string, error) {
	return m, nil
}

func (mapStore) SaveSettings(context.Context, map[string]string) error { return nil }
