# 把 ddns-go 的 DNS provider 源码机械地移植到 internal/ddnsgo/。
#
# 为什么用脚本而不是手抄：这是 30 个文件、约 5000 行的纯机械变换
# （改包名、去掉 config./util. 前缀、删掉对应 import）。手抄的出错概率
# 远高于脚本，而且一旦出错往往是"某个签名少写一个字符"这种极难发现的
# 问题。脚本变换是幂等的、可复查的、可重跑的。
#
# 变换规则：
#   1. package dns|util|config  →  package ddnsgo
#   2. 删除对 github.com/jeessy2/ddns-go/v6/{config,util} 的 import
#   3. 去掉 config. 与 util. 前缀（移植后它们都在同一个包里）
#
# 不做任何逻辑改动 —— 签名、URL、字段名、错误处理全部原样保留。
# 唯一"改动"是包内符号不再带包名前缀。

$ErrorActionPreference = 'Stop'

$src = 'ddns-go-master'
$dst = 'internal/ddnsgo'
New-Item -ItemType Directory -Force -Path $dst | Out-Null

# 需要跳过的文件：ISC 有自己的调度器与配置层，不用 ddns-go 的。
$skip = @('index.go')

function Convert-DdnsGoFile {
    param(
        [string]$InPath,
        [string]$OutName
    )

    $lines = Get-Content -LiteralPath $InPath
    $out = New-Object System.Collections.Generic.List[string]
    $importLines = New-Object System.Collections.Generic.List[string]
    $inImport = $false
    $droppedImports = 0

    foreach ($line in $lines) {
        # 包声明
        if ($line -match '^package\s+\w+\s*$') {
            $out.Add('package ddnsgo')
            continue
        }

        # import 块
        if ($line -match '^import\s*\($') {
            $inImport = $true
            $importLines.Add($line)
            continue
        }
        if ($inImport -and $line -match '^\)\s*$') {
            $inImport = $false
            # 过滤掉 import 起始行与指向 ddns-go 自身包的 import。
            # 注意 import 块本身也进了 $importLines，重建时必须显式补回括号 ——
            # 否则生成的文件会缺右括号，报 "missing import path" 这种
            # 指向行号却看不出原因的错。
            $real = $importLines | Where-Object {
                $_ -notmatch '^import\s*\(' -and
                $_ -notmatch '"github\.com/jeessy2/ddns-go/v6/(config|util)"'
            }
            $droppedImports += ($importLines.Count - $real.Count - 1)
            if ($real.Count -gt 0) {
                $out.Add('import (')
                foreach ($k in $real) { $out.Add($k) }
                $out.Add(')')
            }
            $importLines.Clear()
            continue
        }
        if ($inImport) {
            $importLines.Add($line)
            continue
        }

        # 单行 import
        if ($line -match '^import\s+"github\.com/jeessy2/ddns-go/v6/(config|util)"\s*$') {
            $droppedImports++
            continue
        }

        # 去掉包名前缀
        $l = $line -replace '\butil\.', '' -replace '\bconfig\.', ''
        $out.Add($l)
    }

    $text = ($out -join "`n") + "`n"
    $path = Join-Path $dst $OutName
    Set-Content -LiteralPath $path -Value $text -NoNewline -Encoding utf8
    return $droppedImports
}

$totalDropped = 0

# --- 30 个 provider ---
Get-ChildItem -Path (Join-Path $src 'dns') -Filter '*.go' |
    Where-Object { $_.Name -notlike '*_test.go' -and $skip -notcontains $_.Name } |
    ForEach-Object {
        $name = 'provider_' + $_.Name
        $d = Convert-DdnsGoFile -InPath $_.FullName -OutName $name
        $totalDropped += $d
        "  provider  {0,-24} -> {1}" -f $_.Name, $name
    }

# --- 签名与工具 ---
$utilFiles = @(
    'aliyun_signer.go', 'aliyun_signer_util.go', 'baidu_signer.go',
    'tencent_cloud_signer.go', 'traffic_route_signer.go', 'huawei_signer.go'
)
foreach ($f in $utilFiles) {
    $p = Join-Path $src "util\$f"
    if (Test-Path $p) {
        $name = 'signer_' + $f
        $d = Convert-DdnsGoFile -InPath $p -OutName $name
        $totalDropped += $d
        "  signer    {0,-24} -> {1}" -f $f, $name
    }
}

$miscFiles = @{
    'util\ip_cache.go'            = 'ipcache.go'
    'util\http_util.go'           = 'http.go'
    'util\http_client_util.go'    = 'httpclient.go'
    'util\string.go'              = 'strings.go'
    'util\escape.go'              = 'escape.go'
    'util\copy_url_params.go'     = 'copy_url_params.go'
    'util\socket_bind_linux.go'   = 'socket_bind_linux.go'
    'util\socket_bind_nonlinux.go' = 'socket_bind_nonlinux.go'
    'config\domains.go'           = 'domains.go'
    'config\netInterface.go'      = 'netinterface.go'
}
foreach ($k in $miscFiles.Keys) {
    $p = Join-Path $src $k
    if (Test-Path $p) {
        $d = Convert-DdnsGoFile -InPath $p -OutName $miscFiles[$k]
        $totalDropped += $d
        "  misc      {0,-32} -> {1}" -f $k, $miscFiles[$k]
    }
}

""
"共删除 $totalDropped 处 ddns-go 内部包 import"
"生成文件数: $((Get-ChildItem $dst -Filter '*.go' | Measure-Object).Count)"
