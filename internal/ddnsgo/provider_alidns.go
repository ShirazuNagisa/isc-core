package ddnsgo

import (
	"bytes"
	"net/http"
	"net/url"
)

const (
	alidnsEndpoint string = "https://alidns.aliyuncs.com/"
)

// https://help.aliyun.com/document_detail/29776.html?spm=a2c4g.11186623.6.672.715a45caji9dMA
// Alidns Alidns
type Alidns struct {
	DNS        DNS
	Domains    Domains
	TTL        string
	httpClient *http.Client
}

// AlidnsRecord record
type AlidnsRecord struct {
	DomainName string
	RecordID   string
	Value      string
}

// AlidnsSubDomainRecords 记录
type AlidnsSubDomainRecords struct {
	TotalCount    int
	DomainRecords struct {
		Record []AlidnsRecord
	}
}

// AlidnsResp 修改/添加返回结果
type AlidnsResp struct {
	RecordID  string
	RequestID string
}

// Init 初始化
func (ali *Alidns) Init(dnsConf *DnsConfig, ipv4cache *IpCache, ipv6cache *IpCache) {
	ali.Domains.Ipv4Cache = ipv4cache
	ali.Domains.Ipv6Cache = ipv6cache
	ali.DNS = dnsConf.DNS
	ali.Domains.GetNewIp(dnsConf)
	if dnsConf.TTL == "" {
		// 默认600s
		ali.TTL = "600"
	} else {
		ali.TTL = dnsConf.TTL
	}
	ali.httpClient = dnsConf.GetHTTPClient()
}

// AddUpdateDomainRecords 添加或更新IPv4/IPv6记录
func (ali *Alidns) AddUpdateDomainRecords() Domains {
	ali.addUpdateDomainRecords("A")
	ali.addUpdateDomainRecords("AAAA")
	return ali.Domains
}

func (ali *Alidns) addUpdateDomainRecords(recordType string) {
	ipAddr, domains := ali.Domains.GetNewIpResult(recordType)

	if ipAddr == "" {
		return
	}

	for _, domain := range domains {
		var records AlidnsSubDomainRecords
		// 获取当前域名信息
		params := domain.GetCustomParams()
		params.Set("Action", "DescribeSubDomainRecords")
		params.Set("DomainName", domain.DomainName)
		params.Set("SubDomain", domain.GetFullDomain())
		params.Set("Type", recordType)
		err := ali.request(params, &records)

		if err != nil {
			Log("查询域名信息发生异常! %s", err)
			domain.UpdateStatus = UpdatedFailed
			return
		}

		if records.TotalCount > 0 {
			// 默认第一个
			recordSelected := records.DomainRecords.Record[0]
			if params.Has("RecordId") {
				for i := 0; i < len(records.DomainRecords.Record); i++ {
					if records.DomainRecords.Record[i].RecordID == params.Get("RecordId") {
						recordSelected = records.DomainRecords.Record[i]
					}
				}
			}
			// 存在，更新
			ali.modify(recordSelected, domain, recordType, ipAddr)
		} else {
			// 不存在，创建
			ali.create(domain, recordType, ipAddr)
		}

	}
}

// 创建
func (ali *Alidns) create(domain *Domain, recordType string, ipAddr string) {
	params := domain.GetCustomParams()
	params.Set("Action", "AddDomainRecord")
	params.Set("DomainName", domain.DomainName)
	params.Set("RR", domain.GetSubDomain())
	params.Set("Type", recordType)
	params.Set("Value", ipAddr)
	params.Set("TTL", ali.TTL)

	var result AlidnsResp
	err := ali.request(params, &result)

	if err != nil {
		Log("新增域名解析 %s 失败! 异常信息: %s", domain, err)
		domain.UpdateStatus = UpdatedFailed
		return
	}

	if result.RecordID != "" {
		Log("新增域名解析 %s 成功! IP: %s", domain, ipAddr)
		domain.UpdateStatus = UpdatedSuccess
	} else {
		Log("新增域名解析 %s 失败! 异常信息: %s", domain, "返回RecordId为空")
		domain.UpdateStatus = UpdatedFailed
	}
}

// 修改
func (ali *Alidns) modify(recordSelected AlidnsRecord, domain *Domain, recordType string, ipAddr string) {

	// 相同不修改
	if recordSelected.Value == ipAddr {
		Log("你的IP %s 没有变化, 域名 %s", ipAddr, domain)
		return
	}

	params := domain.GetCustomParams()
	params.Set("Action", "UpdateDomainRecord")
	params.Set("RR", domain.GetSubDomain())
	params.Set("RecordId", recordSelected.RecordID)
	params.Set("Type", recordType)
	params.Set("Value", ipAddr)
	params.Set("TTL", ali.TTL)

	var result AlidnsResp
	err := ali.request(params, &result)

	if err != nil {
		Log("更新域名解析 %s 失败! 异常信息: %s", domain, err)
		domain.UpdateStatus = UpdatedFailed
		return
	}

	if result.RecordID != "" {
		Log("更新域名解析 %s 成功! IP: %s", domain, ipAddr)
		domain.UpdateStatus = UpdatedSuccess
	} else {
		Log("更新域名解析 %s 失败! 异常信息: %s", domain, "返回RecordId为空")
		domain.UpdateStatus = UpdatedFailed
	}
}

// request 统一请求接口
func (ali *Alidns) request(params url.Values, result interface{}) (err error) {
	method := http.MethodGet
	AliyunSigner(ali.DNS.ID, ali.DNS.Secret, &params, method, "2015-01-09")

	req, err := http.NewRequest(
		method,
		alidnsEndpoint,
		bytes.NewBuffer(nil),
	)
	req.URL.RawQuery = params.Encode()

	if err != nil {
		return
	}

	client := ali.httpClient
	resp, err := client.Do(req)
	err = GetHTTPResponse(resp, err, result)

	return
}
