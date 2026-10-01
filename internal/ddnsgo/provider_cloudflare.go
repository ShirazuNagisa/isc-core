package ddnsgo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const zonesAPI = "https://api.cloudflare.com/client/v4/zones"

// Cloudflare Cloudflare实现
type Cloudflare struct {
	DNS        DNS
	Domains    Domains
	TTL        int
	httpClient *http.Client
}

// CloudflareZonesResp cloudflare zones返回结果
type CloudflareZonesResp struct {
	CloudflareStatus
	Result []struct {
		ID     string
		Name   string
		Status string
		Paused bool
	}
}

// CloudflareRecordsResp records
type CloudflareRecordsResp struct {
	CloudflareStatus
	Result []CloudflareRecord
}

// CloudflareRecord 记录实体
type CloudflareRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
	Comment string `json:"comment"`
}

// CloudflareStatus 公共状态
type CloudflareStatus struct {
	Success  bool
	Messages []string
}

// Init 初始化
func (cf *Cloudflare) Init(dnsConf *DnsConfig, ipv4cache *IpCache, ipv6cache *IpCache) {
	cf.Domains.Ipv4Cache = ipv4cache
	cf.Domains.Ipv6Cache = ipv6cache
	cf.DNS = dnsConf.DNS
	cf.Domains.GetNewIp(dnsConf)
	if dnsConf.TTL == "" {
		// 默认1 auto ttl
		cf.TTL = 1
	} else {
		ttl, err := strconv.Atoi(dnsConf.TTL)
		if err != nil {
			cf.TTL = 1
		} else {
			cf.TTL = ttl
		}
	}
	cf.httpClient = dnsConf.GetHTTPClient()
}

// AddUpdateDomainRecords 添加或更新IPv4/IPv6记录
func (cf *Cloudflare) AddUpdateDomainRecords() Domains {
	cf.addUpdateDomainRecords("A")
	cf.addUpdateDomainRecords("AAAA")
	return cf.Domains
}

func (cf *Cloudflare) addUpdateDomainRecords(recordType string) {
	ipAddr, domains := cf.Domains.GetNewIpResult(recordType)

	if ipAddr == "" {
		return
	}

	for _, domain := range domains {
		// get zone
		result, err := cf.getZones(domain)

		if err != nil {
			Log("查询域名信息发生异常! %s", err)
			domain.UpdateStatus = UpdatedFailed
			return
		}

		if len(result.Result) == 0 {
			Log("在DNS服务商中未找到根域名: %s", domain.DomainName)
			domain.UpdateStatus = UpdatedFailed
			return
		}

		params := url.Values{}
		params.Set("type", recordType)
		// The name of DNS records in Cloudflare API expects Punycode.
		//
		// See: cloudflare/cloudflare-go#690
		params.Set("name", domain.ToASCII())
		params.Set("per_page", "50")
		// Add a comment only if it exists
		if c := domain.GetCustomParams().Get("comment"); c != "" {
			params.Set("comment", c)
		}

		zoneID := result.Result[0].ID

		var records CloudflareRecordsResp
		// getDomains 最多更新前50条
		err = cf.request(
			"GET",
			fmt.Sprintf(zonesAPI+"/%s/dns_records?%s", zoneID, params.Encode()),
			nil,
			&records,
		)

		if err != nil {
			Log("查询域名信息发生异常! %s", err)
			domain.UpdateStatus = UpdatedFailed
			return
		}

		if !records.Success {
			Log("查询域名信息发生异常! %s", strings.Join(records.Messages, ", "))
			domain.UpdateStatus = UpdatedFailed
			return
		}

		if len(records.Result) > 0 {
			// 更新
			cf.modify(records, zoneID, domain, ipAddr)
		} else {
			// 新增
			cf.create(zoneID, domain, recordType, ipAddr)
		}
	}
}

// 创建
func (cf *Cloudflare) create(zoneID string, domain *Domain, recordType string, ipAddr string) {
	record := &CloudflareRecord{
		Type:    recordType,
		Name:    domain.ToASCII(),
		Content: ipAddr,
		Proxied: false,
		TTL:     cf.TTL,
		Comment: domain.GetCustomParams().Get("comment"),
	}
	record.Proxied = domain.GetCustomParams().Get("proxied") == "true"
	var status CloudflareStatus
	err := cf.request(
		"POST",
		fmt.Sprintf(zonesAPI+"/%s/dns_records", zoneID),
		record,
		&status,
	)

	if err != nil {
		Log("新增域名解析 %s 失败! 异常信息: %s", domain, err)
		domain.UpdateStatus = UpdatedFailed
		return
	}

	if status.Success {
		Log("新增域名解析 %s 成功! IP: %s", domain, ipAddr)
		domain.UpdateStatus = UpdatedSuccess
	} else {
		Log("新增域名解析 %s 失败! 异常信息: %s", domain, strings.Join(status.Messages, ", "))
		domain.UpdateStatus = UpdatedFailed
	}
}

// 修改
func (cf *Cloudflare) modify(result CloudflareRecordsResp, zoneID string, domain *Domain, ipAddr string) {
	customParams := domain.GetCustomParams()
	manageProxied := customParams.Has("proxied")
	desiredProxied := customParams.Get("proxied") == "true"

	for _, record := range result.Result {
		ipChanged := record.Content != ipAddr
		proxiedChanged := manageProxied && record.Proxied != desiredProxied

		// 相同不修改
		if !ipChanged && !proxiedChanged {
			Log("DNS 记录没有变化, 域名 %s", domain)
			continue
		}

		if proxiedChanged {
			Log(
				"Cloudflare 代理状态发生变化: %v -> %v! 域名 %s",
				record.Proxied,
				desiredProxied,
				domain,
			)
		}

		var status CloudflareStatus

		record.Content = ipAddr
		record.TTL = cf.TTL
		if manageProxied {
			record.Proxied = desiredProxied
		}

		err := cf.request(
			"PUT",
			fmt.Sprintf(zonesAPI+"/%s/dns_records/%s", zoneID, record.ID),
			record,
			&status,
		)

		if err != nil {
			Log("更新域名解析 %s 失败! 异常信息: %s", domain, err)
			domain.UpdateStatus = UpdatedFailed
			return
		}

		if status.Success {
			Log("更新域名解析 %s 成功! IP: %s", domain, ipAddr)
			domain.UpdateStatus = UpdatedSuccess
		} else {
			Log("更新域名解析 %s 失败! 异常信息: %s", domain, strings.Join(status.Messages, ", "))
			domain.UpdateStatus = UpdatedFailed
		}
	}
}

// 获得域名记录列表
func (cf *Cloudflare) getZones(domain *Domain) (result CloudflareZonesResp, err error) {
	params := url.Values{}
	params.Set("name", domain.DomainName)
	params.Set("status", "active")
	params.Set("per_page", "50")

	err = cf.request(
		"GET",
		fmt.Sprintf(zonesAPI+"?%s", params.Encode()),
		nil,
		&result,
	)

	return
}

// request 统一请求接口
func (cf *Cloudflare) request(method string, url string, data interface{}, result interface{}) (err error) {
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
	req.Header.Set("Authorization", "Bearer "+cf.DNS.Secret)
	req.Header.Set("Content-Type", "application/json")

	client := cf.httpClient
	resp, err := client.Do(req)
	err = GetHTTPResponse(resp, err, result)

	return
}
