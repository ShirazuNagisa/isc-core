package sysproxy

import (
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// 这几个地址是内核真正要下载运行时的地方。它们各自"该走哪条路"完全不同：
// 实测同一时刻，dl.static-php.dev 只能直连（走代理时隧道刚建立就被关掉），
// 而 api.cloudflare.com 在另一个时段只能走代理。任何一个被写死都会让
// 某类运行时永远装不上。
var liveRuntimeURLs = []string{
	"https://dl.static-php.dev/static-php-cli/common/php-8.5.8-cli-macos-aarch64.tar.gz",
	"https://api.adoptium.net/v3/info/available_releases",
	"https://builds.dotnet.microsoft.com/dotnet/Sdk/8.0.425/dotnet-sdk-8.0.425-osx-arm64.tar.gz",
}

// TestLiveFailoverReachesRuntimeSources 需要联网，默认跳过。
//
//	ISC_LIVE_CHECK=1 go test ./internal/sysproxy/ -run Live -v
//
// 只取前 1 MiB：要验证的是"能不能拿到数据"，不是把整个运行时下下来。
func TestLiveFailoverReachesRuntimeSources(t *testing.T) {
	if os.Getenv("ISC_LIVE_CHECK") == "" {
		t.Skip("需要联网：设置 ISC_LIVE_CHECK=1 才运行")
	}
	client := &http.Client{
		Transport: Transport(&http.Transport{}),
		Timeout:   4 * time.Minute,
	}
	t.Logf("系统代理：%s", Describe())

	for _, u := range liveRuntimeURLs {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Range", "bytes=0-1048575")
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("%s 取不到：%v", u, err)
			continue
		}
		n, _ := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			t.Errorf("%s 返回 %d", u, resp.StatusCode)
			continue
		}
		t.Logf("%s → %d，%d 字节", u, resp.StatusCode, n)
	}
}
