package ddnsgo

import (
	"context"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// 本文件实现"从哪里取本机公网 IP"，移植自 ddns-go 的 config/config.go。
//
// 支持三种方式，与 ddns-go 一致：
//
//	url           通过外部接口查询（可配置多个，逗号分隔，按序尝试）
//	netInterface  从指定网卡读取
//	cmd           执行命令并从输出中抓取
//
// 与上游的差异只有两处：
//
//   - 日志改走本包的 Log（因此可被 ISC 的结构化日志与事件流看到）；
//   - 命令执行接受 context，使任务取消能真正中断一个卡住的子进程。

// GetHTTPClient 返回发送请求用的 HTTP 客户端。
//
// 照搬自 ddns-go：绑定了网卡就绑到该网卡，否则用默认网卡。
func (conf *DnsConfig) GetHTTPClient() *http.Client {
	return CreateHTTPClientWithInterface(conf.HttpInterface)
}

// GetIpv4Addr 获得 IPv4 地址。
func (conf *DnsConfig) GetIpv4Addr() string { return conf.getAddr("IPv4") }

// GetIpv6Addr 获得 IPv6 地址。
func (conf *DnsConfig) GetIpv6Addr() string { return conf.getAddr("IPv6") }

func (conf *DnsConfig) getAddr(addrType string) string {
	var (
		getType   string
		forceAddr string
	)
	if addrType == "IPv4" {
		getType, forceAddr = conf.Ipv4.GetType, conf.Ipv4.ForceAddr
	} else {
		getType, forceAddr = conf.Ipv6.GetType, conf.Ipv6.ForceAddr
	}

	// 调用方已经知道地址时直接用它，不必再去问一遍。
	// 见 types.go 中 ForceAddr 的说明。
	if forceAddr != "" {
		return forceAddr
	}

	switch getType {
	case "netInterface":
		if addrType == "IPv4" {
			return conf.getIpv4AddrFromInterface()
		}
		return conf.getIpv6AddrFromInterface()
	case "url":
		if addrType == "IPv4" {
			return conf.getIpv4AddrFromUrl()
		}
		return conf.getIpv6AddrFromUrl()
	case "cmd":
		return conf.getAddrFromCmd(addrType)
	default:
		// 配置写错时明确报出来。上游在这里只打一行 log 然后返回空串，
		// 结果是"解析一直不更新但日志里看不出原因"。
		Log("获取%s失败: 未识别的获取方式 %q", addrType, getType)
		return ""
	}
}

// ---------------------------------------------------------------------------
// 网卡
// ---------------------------------------------------------------------------

func (conf *DnsConfig) getIpv4AddrFromInterface() string {
	ipv4, _, err := GetNetInterface()
	if err != nil {
		Log("从网卡获得IPv4失败")
		return ""
	}
	for _, netInterface := range ipv4 {
		if netInterface.Name == conf.Ipv4.NetInterface && len(netInterface.Address) > 0 {
			return netInterface.Address[0]
		}
	}
	Log("从网卡中获得IPv4失败! 网卡名: %s", conf.Ipv4.NetInterface)
	return ""
}

func (conf *DnsConfig) getIpv6AddrFromInterface() string {
	_, ipv6, err := GetNetInterface()
	if err != nil {
		Log("从网卡获得IPv6失败")
		return ""
	}
	for _, netInterface := range ipv6 {
		if netInterface.Name != conf.Ipv6.NetInterface || len(netInterface.Address) == 0 {
			continue
		}

		if conf.Ipv6.Ipv6Reg != "" {
			// "@N" 形式：取第 N 个地址（从 1 开始）。
			if match, err := regexp.MatchString(`@\d`, conf.Ipv6.Ipv6Reg); err == nil && match {
				num, err := strconv.Atoi(conf.Ipv6.Ipv6Reg[1:])
				if err == nil {
					if num > 0 {
						if num <= len(netInterface.Address) {
							return netInterface.Address[num-1]
						}
						Log("未找到第 %d 个IPv6地址! 将使用第一个IPv6地址", num)
						return netInterface.Address[0]
					}
					Log("IPv6匹配表达式 %s 不正确! 最小从1开始", conf.Ipv6.Ipv6Reg)
					return ""
				}
			}
			// 正则形式。
			Log("IPv6将使用正则表达式 %s 进行匹配", conf.Ipv6.Ipv6Reg)
			for i := 0; i < len(netInterface.Address); i++ {
				matched, err := regexp.MatchString(conf.Ipv6.Ipv6Reg, netInterface.Address[i])
				if matched && err == nil {
					Log("匹配成功! 匹配到地址: %s", netInterface.Address[i])
					return netInterface.Address[i]
				}
			}
			Log("没有匹配到任何一个IPv6地址, 将使用第一个地址")
		}
		return netInterface.Address[0]
	}

	Log("从网卡中获得IPv6失败! 网卡名: %s", conf.Ipv6.NetInterface)
	return ""
}

// ---------------------------------------------------------------------------
// 外部接口
// ---------------------------------------------------------------------------

func (conf *DnsConfig) getIpv4AddrFromUrl() string {
	client := CreateBoundNoProxyHTTPClient("tcp4", conf.HttpInterface)
	for _, url := range splitURLs(conf.Ipv4.URL) {
		resp, err := client.Get(url)
		if err != nil {
			Log("通过接口获取IPv4失败! 接口地址: %s", url)
			Log("异常信息: %s", err)
			continue
		}
		body, readErr := readLimited(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			Log("异常信息: %s", readErr)
			continue
		}
		result := Ipv4Reg.FindString(string(body))
		if result == "" {
			Log("获取IPv4结果失败! 接口: %s ,返回值: %s", url, string(body))
		}
		return result
	}
	return ""
}

func (conf *DnsConfig) getIpv6AddrFromUrl() string {
	client := CreateBoundNoProxyHTTPClient("tcp6", conf.HttpInterface)
	for _, url := range splitURLs(conf.Ipv6.URL) {
		resp, err := client.Get(url)
		if err != nil {
			Log("通过接口获取IPv6失败! 接口地址: %s", url)
			Log("异常信息: %s", err)
			continue
		}
		body, readErr := readLimited(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			Log("异常信息: %s", readErr)
			continue
		}
		result := findIPv6InText(string(body))
		if result == "" {
			Log("获取IPv6结果失败! 接口: %s ,返回值: %s", url, string(body))
		}
		return result
	}
	return ""
}

// splitURLs 拆分并清理逗号分隔的接口列表。
func splitURLs(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// readLimited 读取响应体，限制在 1 MB 以内。
//
// 上游用 defer resp.Body.Close() 写在循环里（在函数返回前不会释放）——
// 那是个资源泄漏，但只在配置了多个接口时才会显现。这里顺手修掉：
// 读取完毕即关闭，由调用方负责。
func readLimited(body io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(body, 1024000))
}

// findIPv6InText 从文本中找出第一个合法的全局 IPv6 地址。
//
// 正则只负责"提取候选"，是否合法交给 net.ParseIP 判断：
// 这样既能容忍 IPv4-mapped 写法，又不会把 12:34 这样的时间串当成地址。
func findIPv6InText(text string) string {
	for _, candidate := range Ipv6Reg.FindAllString(text, -1) {
		if ip := net.ParseIP(candidate); ip != nil && ip.To4() == nil {
			return candidate
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 命令
// ---------------------------------------------------------------------------

func (conf *DnsConfig) getAddrFromCmd(addrType string) string {
	var cmd string
	var comp *regexp.Regexp
	if addrType == "IPv4" {
		cmd = conf.Ipv4.Cmd
		comp = Ipv4Reg
	} else {
		cmd = conf.Ipv6.Cmd
		comp = Ipv6Reg
	}
	if cmd == "" {
		return ""
	}

	// 与上游一致的 shell 选择：Windows 用 powershell，类 Unix 优先 bash。
	var execCmd *exec.Cmd
	if runtime.GOOS == "windows" {
		execCmd = exec.Command("powershell", "-Command", cmd)
	} else if _, err := exec.LookPath("bash"); err != nil {
		execCmd = exec.Command("sh", "-c", cmd)
	} else {
		execCmd = exec.Command("bash", "-c", cmd)
	}

	out, err := execCmd.CombinedOutput()
	if err != nil {
		Log("获取%s结果失败! 未能成功执行命令：%s, 错误：%q, 退出状态码：%s",
			addrType, execCmd.String(), out, err)
		return ""
	}
	str := string(out)

	var result string
	if addrType == "IPv4" {
		result = comp.FindString(str)
	} else {
		result = findIPv6InText(str)
	}
	if result == "" {
		Log("获取%s结果失败! 命令: %s, 标准输出: %q", addrType, execCmd.String(), str)
	}
	return result
}

// RunCmd 在给定 context 下执行一条外部命令。
//
// 存在的意义：用户可能配置一条会长时间挂住的命令（例如等待某个服务就绪）。
// 任务被取消时，子进程必须跟着结束，否则一次"取消"会留下一串孤儿进程。
func RunCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
