package ddnsgo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const desecEndpoint = "https://desec.io/api/v1"

// deSEC TTL限制: 最小3600秒(域名默认最小TTL), 最大86400秒
const (
	desecMinTTL = 3600
	desecMaxTTL = 86400
)

// DeSEC deSEC DNS实现
type DeSEC struct {
	DNS        DNS
	Domains    Domains
	TTL        int
	httpClient *http.Client
	lastStatus int
}

// DeSECRRSet RRSet记录实体
type DeSECRRSet struct {
	SubDomain string   `json:"subname"`
	Type      string   `json:"type"`
	Records   []string `json:"records"`
	TTL       int      `json:"ttl"`
}

// Init 初始化
func (desec *DeSEC) Init(dnsConf *DnsConfig, ipv4cache *IpCache, ipv6cache *IpCache) {
	desec.Domains.Ipv4Cache = ipv4cache
	desec.Domains.Ipv6Cache = ipv6cache
	desec.DNS = dnsConf.DNS
	desec.Domains.GetNewIp(dnsConf)
	desec.TTL = desecMinTTL
	if ttl, err := strconv.Atoi(dnsConf.TTL); err == nil {
		if ttl > desecMinTTL {
			desec.TTL = ttl
		}
		if ttl > desecMaxTTL {
			desec.TTL = desecMaxTTL
		}
	}
	desec.httpClient = dnsConf.GetHTTPClient()
}

// AddUpdateDomainRecords 添加或更新IPv4/IPv6记录
func (desec *DeSEC) AddUpdateDomainRecords() Domains {
	desec.addUpdateDomainRecords("A")
	desec.addUpdateDomainRecords("AAAA")
	return desec.Domains
}

func (desec *DeSEC) addUpdateDomainRecords(recordType string) {
	ipAddr, domains := desec.Domains.GetNewIpResult(recordType)

	if ipAddr == "" {
		return
	}

	for _, domain := range domains {
		// 按subname+type过滤查询，域名或记录不存在返回空数组，域名不存在返回404
		rrsets, status, err := desec.getRRSets(domain.DomainName, domain.SubDomain, recordType)
		if err != nil {
			if status == http.StatusNotFound {
				Log("在DNS服务商中未找到根域名: %s", domain.DomainName)
			} else {
				Log("查询域名信息发生异常! %s", err)
			}
			domain.UpdateStatus = UpdatedFailed
			continue
		}

		if len(rrsets) > 0 && len(rrsets[0].Records) > 0 && rrsets[0].Records[0] == ipAddr {
			// ip与dns服务器一致，不执行更新
			Log("你的IP %s 没有变化, 域名 %s", ipAddr, domain)
			continue
		}

		if len(rrsets) > 0 {
			// 更新记录
			desec.modify(domain, recordType, ipAddr)
		} else {
			// 创建记录
			desec.create(domain, recordType, ipAddr)
		}
	}
}

// getRRSets 按subname和type过滤查询rrsets
func (desec *DeSEC) getRRSets(zone string, subDomain string, recordType string) (rrsets []DeSECRRSet, status int, err error) {
	params := url.Values{}
	params.Set("subname", subDomain)
	params.Set("type", recordType)

	_, err = desec.request(
		"GET",
		fmt.Sprintf("%s/domains/%s/rrsets/?%s", desecEndpoint, zone, params.Encode()),
		nil,
		&rrsets,
	)

	return rrsets, desec.lastStatus, err
}

// create 创建新的解析
func (desec *DeSEC) create(domain *Domain, recordType string, ipAddr string) {
	rrset := DeSECRRSet{
		SubDomain: domain.SubDomain,
		Type:      recordType,
		Records:   []string{ipAddr},
		TTL:       desec.TTL,
	}

	var result DeSECRRSet
	_, err := desec.request(
		"POST",
		fmt.Sprintf("%s/domains/%s/rrsets/", desecEndpoint, domain.DomainName),
		rrset,
		&result,
	)

	if err != nil {
		Log("新增域名解析 %s 失败! 异常信息: %s", domain, err)
		domain.UpdateStatus = UpdatedFailed
	} else {
		Log("新增域名解析 %s 成功! IP: %s", domain, ipAddr)
		domain.UpdateStatus = UpdatedSuccess
	}
}

// modify 更新解析
func (desec *DeSEC) modify(domain *Domain, recordType string, ipAddr string) {
	rrset := DeSECRRSet{
		SubDomain: domain.SubDomain,
		Type:      recordType,
		Records:   []string{ipAddr},
		TTL:       desec.TTL,
	}

	// 批量PATCH接口，可更新单个rrset，主域名的subname为空字符串
	var result []DeSECRRSet
	_, err := desec.request(
		"PATCH",
		fmt.Sprintf("%s/domains/%s/rrsets/", desecEndpoint, domain.DomainName),
		[]DeSECRRSet{rrset},
		&result,
	)

	if err != nil {
		Log("更新域名解析 %s 失败! 异常信息: %s", domain, err)
		domain.UpdateStatus = UpdatedFailed
	} else {
		Log("更新域名解析 %s 成功! IP: %s", domain, ipAddr)
		domain.UpdateStatus = UpdatedSuccess
	}
}

// request 统一请求接口，返回响应状态码
func (desec *DeSEC) request(method string, url string, data interface{}, result interface{}) (status int, err error) {
	jsonStr := make([]byte, 0)
	if data != nil {
		jsonStr, _ = json.Marshal(data)
	}

	req, err := http.NewRequest(
		method,
		url,
		bytes.NewBuffer(jsonStr),
	)

	if err != nil {
		return
	}

	req.Header.Set("Authorization", "Token "+desec.DNS.Secret)
	req.Header.Set("Content-Type", "application/json")

	client := desec.httpClient
	resp, err := client.Do(req)
	if resp != nil {
		status = resp.StatusCode
		desec.lastStatus = resp.StatusCode
	}
	err = GetHTTPResponse(resp, err, result)
	return
}
