package remote

import (
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"testing"
)

// 按 SNI 选证书。
//
// # 这里最需要钉住的是**回落**
//
// 两条路必须并存，而它们的失败方式完全不同：
//
//   - 局域网用自签证书，公钥指纹是手机配对时固定的。换成受信任证书
//     会让所有已配对的手机立刻连不上 —— 那条路必须一个字都不变。
//   - 公网用 Let's Encrypt 证书。它**不能**沿用固定指纹，因为续期会
//     换密钥，固定之后每次续期都连不上。
//
// 而最容易写错的是"公网那张还没有/取不到"的时候：那时必须安静地
// 回落到自签，而不是报错 —— 报错会让"公网还没配好"变成"手机完全
// 连不上"，而后者严重得多。

func newSNIService(t *testing.T, public PublicCertificateFunc) *Service {
	t.Helper()
	svc, err := New(Options{Dir: t.TempDir(), PublicCert: public,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	return svc
}

func TestGetCertificatePrefersThePublicCertificate(t *testing.T) {
	t.Parallel()

	public := &tls.Certificate{Certificate: [][]byte{[]byte("public")}}
	svc := newSNIService(t, func(host string) (*tls.Certificate, error) {
		if host == "mizar-abc.example.com" {
			return public, nil
		}
		return nil, nil
	})

	got, err := svc.getCertificate(&tls.ClientHelloInfo{ServerName: "mizar-abc.example.com"})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if len(got.Certificate) == 0 || string(got.Certificate[0]) != "public" {
		t.Fatal("域名请求应当拿到受信任的那张")
	}
}

// **这条是整组里最重要的。**
//
// 没有 SNI 名字（手机用 IP 连）时必须是自签那张，而且不能报错。
func TestGetCertificateFallsBackToSelfSignedWithoutServerName(t *testing.T) {
	t.Parallel()

	called := false
	svc := newSNIService(t, func(string) (*tls.Certificate, error) {
		called = true
		return nil, nil
	})

	got, err := svc.getCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("没有 SNI 名字时不该报错: %v", err)
	}
	if got != &svc.cert.TLS {
		t.Fatal("应当回落到自签那张")
	}
	if called {
		t.Fatal("没有名字时不该去问证书来源 —— 那是一次无谓的磁盘查找")
	}
}

// 公网那张取不到时**安静回落**，不报错。
//
// 报错会把"公网还没配好"变成"手机完全连不上"，而后者严重得多：
// 用户会以为是自己弄坏了什么。
func TestGetCertificateFallsBackWhenPublicLookupFails(t *testing.T) {
	t.Parallel()

	for _, lookup := range []PublicCertificateFunc{
		func(string) (*tls.Certificate, error) { return nil, errors.New("证书库读不了") },
		func(string) (*tls.Certificate, error) { return nil, nil },
		func(string) (*tls.Certificate, error) { return &tls.Certificate{}, nil },
	} {
		svc := newSNIService(t, lookup)
		got, err := svc.getCertificate(&tls.ClientHelloInfo{ServerName: "mizar-abc.example.com"})
		if err != nil {
			t.Fatalf("取不到公网证书时不该报错: %v", err)
		}
		if got != &svc.cert.TLS {
			t.Fatal("应当回落到自签那张")
		}
	}
}

// 没有配置公网证书来源时，一切与加这个功能之前一样。
func TestGetCertificateWithoutPublicSourceIsUnchanged(t *testing.T) {
	t.Parallel()

	svc := newSNIService(t, nil)
	got, err := svc.getCertificate(&tls.ClientHelloInfo{ServerName: "anything.example.com"})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if got != &svc.cert.TLS {
		t.Fatal("没有公网来源时必须用自签那张")
	}
}

// 自签那张的 SPKI 不能因为这个功能而变。
//
// 它一旦变了，所有已配对的手机都会在下次连接时报"服务器身份已变化"，
// 而用户需要重新配对每一台设备。
func TestSelfSignedSPKIIsStableAcrossRestarts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := newSNIServiceAt(t, dir, nil)
	fingerprint := first.cert.SPKIBase64()

	second := newSNIServiceAt(t, dir, func(string) (*tls.Certificate, error) { return nil, nil })
	if second.cert.SPKIBase64() != fingerprint {
		t.Fatal("重启之后自签证书的公钥指纹变了 —— 所有已配对的手机都会要求重新配对")
	}
}

func newSNIServiceAt(t *testing.T, dir string, public PublicCertificateFunc) *Service {
	t.Helper()
	svc, err := New(Options{Dir: dir, PublicCert: public,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	return svc
}
