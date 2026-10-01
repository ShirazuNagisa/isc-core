# 从 ddns-go 的 util/messages.go 提取英文译文，生成 ISC 的 i18n 数据文件。
#
# ddns-go 的做法是把**中文原文当作消息 key**，再用 golang.org/x/text/message
# 注册英文译文。移植过来的 provider 有 233 处 Log 调用沿用这个约定。
#
# 与其把那 233 处调用逐行改写（移植中最容易出错的操作），不如沿用约定：
# 本脚本把译文抽出来并入 ISC 的 i18n 目录，于是 ddnsgo.Log 走一遍
# i18n.T 就既能输出中文也能输出英文。
#
# 脚本是幂等的，可随时重跑（上游更新译文后重新生成即可）。

$ErrorActionPreference = 'Stop'

$src = 'ddns-go-master/util/messages.go'
$dst = 'internal/i18n/messages_ddnsgo.go'

if (-not (Test-Path $src)) { throw "找不到 $src" }

$text = Get-Content -LiteralPath $src -Raw

# 匹配 message.SetString(language.English, "原文", "译文")
$pattern = 'message\.SetString\(\s*language\.English,\s*"((?:[^"\\]|\\.)*)"\s*,\s*"((?:[^"\\]|\\.)*)"\s*\)'
$matches = [regex]::Matches($text, $pattern)

$pairs = [ordered]@{}
foreach ($m in $matches) {
    $zh = $m.Groups[1].Value
    $en = $m.Groups[2].Value
    # 同一 key 重复注册时保留第一次 —— 与 ddns-go 的注册语义一致
    # （后面的会覆盖前面的，但实践中没有重复项；保留首次更可预测）。
    if (-not $pairs.Contains($zh)) { $pairs[$zh] = $en }
}

if ($pairs.Count -eq 0) { throw "没有提取到任何译文，请检查 messages.go 的格式是否变化" }

$sb = New-Object System.Text.StringBuilder
[void]$sb.AppendLine('package i18n')
[void]$sb.AppendLine()
[void]$sb.AppendLine('// 本文件由 scripts/extract-ddnsgo-messages.ps1 生成，请勿手工编辑。')
[void]$sb.AppendLine('//')
[void]$sb.AppendLine('// 内容是从 ddns-go（MIT，Copyright (c) 2020 jeessy）的 util/messages.go')
[void]$sb.AppendLine('// 提取的英文译文。移植过来的 provider 沿用"中文原文即消息 key"的约定，')
[void]$sb.AppendLine('// 因此这里的 key 是中文句子而不是点分标识符 —— 这是刻意的：')
[void]$sb.AppendLine('// 改写 233 处调用点的风险远高于让两类 key 并存。')
[void]$sb.AppendLine('//')
[void]$sb.AppendLine('// 中文侧不需要对应条目：T() 在找不到 key 时返回 key 本身，')
[void]$sb.AppendLine('// 而 key 已经是中文。')
[void]$sb.AppendLine()
[void]$sb.AppendLine('// messagesEnDdnsGo 是移植代码使用的英文译文。')
[void]$sb.AppendLine('var messagesEnDdnsGo = map[string]string{')

foreach ($k in $pairs.Keys) {
    # 原样写出，**不做任何转义**：正则捕获到的已经是 Go 字符串字面量内部
    # 的形式（例如源文件里的 \\ 表示一个真实反斜杠），再转义一次会变成
    # 两个反斜杠。踩过一次，所以这里留个记号。
    [void]$sb.AppendLine("`t`"$k`": `"$($pairs[$k])`",")
}

[void]$sb.AppendLine('}')
[void]$sb.AppendLine()
[void]$sb.AppendLine('// ddnsGoKeys 返回移植代码使用的全部消息 key，供 i18n 完整性测试使用。')
[void]$sb.AppendLine('func ddnsGoKeys() []string {')
[void]$sb.AppendLine('	out := make([]string, 0, len(messagesEnDdnsGo))')
[void]$sb.AppendLine('	for k := range messagesEnDdnsGo {')
[void]$sb.AppendLine('		out = append(out, k)')
[void]$sb.AppendLine('	}')
[void]$sb.AppendLine('	return out')
[void]$sb.AppendLine('}')

Set-Content -LiteralPath $dst -Value $sb.ToString() -NoNewline -Encoding utf8

"提取译文 $($pairs.Count) 条 -> $dst"
