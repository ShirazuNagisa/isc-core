package ddnsgo

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func sha256hex(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

func tencentCloudHmacsha256(s, key string) string {
	hashed := hmac.New(sha256.New, []byte(key))
	hashed.Write([]byte(s))
	return string(hashed.Sum(nil))
}

// 腾讯云签名时的服务名。
//
// 上游把这两个常量命名为 DnsPod 与 EdgeOne，与两个 provider 的结构体
// 同名 —— 在 ddns-go 里它们分属 util 与 dns 两个包，不会冲突；
// 移植到同一个包后必须改名。加 svc 前缀是为了让"这是服务名、不是类型"
// 在调用处一眼可辨。
const (
	svcDnsPod  = "dnspod"
	svcEdgeOne = "teo"
)

// TencentCloudSigner 腾讯云签名方法 v3 https://cloud.tencent.com/document/api/1427/56189#Golang
func TencentCloudSigner(secretId string, secretKey string, r *http.Request, action string, payload string, service string) {
	algorithm := "TC3-HMAC-SHA256"
	host := WriteString(service, ".tencentcloudapi.com")
	timestamp := time.Now().Unix()
	timestampStr := strconv.FormatInt(timestamp, 10)

	// step 1: build canonical request string
	canonicalHeaders := WriteString("content-type:application/json\nhost:", host, "\nx-tc-action:", strings.ToLower(action), "\n")
	signedHeaders := "content-type;host;x-tc-action"
	hashedRequestPayload := sha256hex(payload)
	canonicalRequest := WriteString("POST\n/\n\n", canonicalHeaders, "\n", signedHeaders, "\n", hashedRequestPayload)

	// step 2: build string to sign
	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	credentialScope := WriteString(date, "/", service, "/tc3_request")
	hashedCanonicalRequest := sha256hex(canonicalRequest)
	string2sign := WriteString(algorithm, "\n", timestampStr, "\n", credentialScope, "\n", hashedCanonicalRequest)

	// step 3: sign string
	secretDate := tencentCloudHmacsha256(date, WriteString("TC3", secretKey))
	secretService := tencentCloudHmacsha256(service, secretDate)
	secretSigning := tencentCloudHmacsha256("tc3_request", secretService)
	signature := hex.EncodeToString([]byte(tencentCloudHmacsha256(string2sign, secretSigning)))

	// step 4: build authorization
	authorization := WriteString(algorithm, " Credential=", secretId, "/", credentialScope, ", SignedHeaders=", signedHeaders, ", Signature=", signature)

	r.Header.Add("Authorization", authorization)
	r.Header.Set("Host", host)
	r.Header.Set("X-TC-Action", action)
	r.Header.Add("X-TC-Timestamp", timestampStr)
}
