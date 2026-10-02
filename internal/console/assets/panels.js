// 本文件里的文案都经 window.iscI18n 取。
//
// 这两个别名是为了让调用处短一些 —— 它们是拼接生成 HTML 的那部分，
// 取值点多达几十处，`window.iscI18n.t(...)` 会把真正的结构淹掉。
//
// 第二个参数是**兜底**：消息表没到或 key 不存在时显示它。动态渲染的内容
// 必须保留兜底（不像 index.html 那样写死在标记上），因为这些字符串只在
// 这里出现一次，没有第二个来源。
const t = (key, fallback) => window.iscI18n.t(key, fallback);
const tf = (key, fallback, params) => window.iscI18n.tf(key, fallback, params);
// ---------------------------------------------------------------------------
// 反向代理
// ---------------------------------------------------------------------------

// 代理路由是**整体保存**的，因此这里在本地维护一份完整列表，
// 编辑后整份提交。
//
// 用增量接口的话，「检查同一个域名是否出现在两条规则里」就得跨多次
// 调用完成，而中间任何一个时刻的状态都可能有冲突。
async function loadProxy() {
  const [status, routes] = await Promise.all([
    api('GET', '/v1/proxy/status'),
    api('GET', '/v1/proxy/routes'),
  ]);

  const s = status.ok ? await status.json() : null;
  const r = routes.ok ? await routes.json() : { items: [] };

  state.proxyRoutes = r.items || [];
  renderProxyStatus(s);
  renderProxyRoutes();
}

function renderProxyStatus(s) {
  const box = $('pxState');
  if (!s) {
    box.innerHTML = '<span class="err">' + t('web.px.err_state', '无法读取代理状态') + '</span>';
    return;
  }

  if (s.running) {
    const scheme = s.tls ? 'HTTPS' : 'HTTP';
    box.innerHTML = '运行中：' + scheme + '，监听端口 ' + esc(s.port) +
      '，' + esc(s.routes) + ' 条规则';
  } else {
    box.textContent = t('web.px.not_running', '未运行');
  }

  // 失败原因必须显示出来 —— 只写日志的话，用户在界面上
  // 只看到「代理没开」而不知道为什么。
  if (s.error) {
    box.innerHTML += '<br><span class="err">' + esc(s.error) + '</span>';
  }
}

function renderProxyRoutes() {
  const box = $('pxList');
  const routes = state.proxyRoutes || [];

  if (routes.length === 0) {
    box.innerHTML = '<p class="hint">' + t('web.px.empty', '还没有转发规则。') + '</p>';
    return;
  }

  let html = '<table><thead><tr>' +
    '<th>' + t('web.th.domain', '域名') + '</th><th>' + t('web.th.upstream', '上游') + '</th><th>HTTPS</th><th></th>' +
    '</tr></thead><tbody>';

  for (const r of routes) {
    html += '<tr>' +
      '<td>' + esc((r.hosts || []).join('<br>')) + '</td>' +
      '<td><code>' + esc(r.upstream) + '</code></td>' +
      '<td>' + (r.tls ? '✅' : '—') + '</td>' +
      '<td class="actions">' +
      '<button data-editpx="' + esc(r.id) + '">' + t('web.common.edit', '编辑') + '</button>' +
      '<button data-delpx="' + esc(r.id) + '" class="danger">' + t('web.common.delete', '删除') + '</button>' +
      '</td></tr>';
  }

  box.innerHTML = html + '</tbody></table>';
}

function renderProxyForm(existing) {
  const form = $('pxForm');
  const r = existing || { id: '', hosts: [], upstream: '', tls: false };

  form.hidden = false;
  form.innerHTML =
    '<h3>' + (existing ? t('web.px.edit_rule', '编辑规则') : t('web.px.new_rule', '新增规则')) + '</h3>' +
    '<label>' + t('web.px.l_domains', '域名（每行一个）') + ' ' +
    '<textarea id="pxHosts" rows="3" placeholder="home.example.com">' +
    esc((r.hosts || []).join('\n')) + '</textarea></label>' +
    '<label>' + t('web.px.l_upstream', '上游地址') + ' <input id="pxUpstream" placeholder="127.0.0.1:8096" value="' +
    esc(r.upstream) + '"></label>' +
    '<p class="hint">' + t('web.px.hint_private', '只允许本机与内网地址。允许公网地址会让这个功能变成一个<strong>开放代理</strong>。') + '</p>' +
    '<label class="check"><input type="checkbox" id="pxRouteTls"' +
    (r.tls ? ' checked' : '') + '> ' + t('web.px.l_tls', '为此域名提供 HTTPS') + '</label>' +
    '<div class="row"><button id="pxSaveRoute" class="primary">' + t('web.common.save', '保存') + '</button>' +
    '<button id="pxCancel">' + t('web.common.cancel', '取消') + '</button></div>';

  $('pxCancel').onclick = () => { form.hidden = true; };
  $('pxSaveRoute').onclick = () => saveProxyRoute(r.id);
}

async function saveProxyRoute(id) {
  const hosts = $('pxHosts').value
    .split('\n').map((s) => s.trim()).filter(Boolean);

  if (hosts.length === 0) { toast(t('web.px.need_domain', '至少填一个域名'), 'err'); return; }
  if (!$('pxUpstream').value.trim()) { toast(t('web.px.need_upstream', '请填上游地址'), 'err'); return; }

  const route = {
    id: id || ('r' + Date.now()),
    hosts: hosts,
    upstream: $('pxUpstream').value.trim(),
    tls: $('pxRouteTls').checked,
  };

  // 整体替换：先去掉同 ID 的旧项，再追加。
  const next = (state.proxyRoutes || []).filter((x) => x.id !== route.id);
  next.push(route);

  const r = await api('PUT', '/v1/proxy/routes', { items: next });
  if (!r.ok) { toast('保存失败：' + explain(r), 'err'); return; }

  toast(t('web.common.saved', '已保存'), 'ok');
  $('pxForm').hidden = true;
  loadProxy();
}

async function deleteProxyRoute(id) {
  if (!confirm(t('web.px.confirm_delete', '确定删除这条规则？'))) return;

  const next = (state.proxyRoutes || []).filter((x) => x.id !== id);
  const r = await api('PUT', '/v1/proxy/routes', { items: next });
  if (!r.ok) { toast('删除失败：' + explain(r), 'err'); return; }

  toast(t('web.common.deleted', '已删除'), 'ok');
  loadProxy();
}

async function saveProxySettings() {
  const r = await api('PATCH', '/v1/settings', {
    proxy_enabled: $('pxEnabled').checked,
    proxy_port: parseInt($('pxPort').value, 10) || 0,
    proxy_tls: $('pxTls').checked,
  });

  if (!r.ok) {
    // 校验失败的提示原样显示 —— 那是用户唯一能据此行动的线索。
    toast('保存失败：' + explain(r), 'err');
    return;
  }
  toast(t('web.settings.saved', '设置已保存'), 'ok');
  // 等监听重启完再刷新状态。
  setTimeout(loadProxyStatus, 600);
}

async function loadProxyStatus() {
  const r = await api('GET', '/v1/proxy/status');
  if (r.ok) renderProxyStatus(await r.json());
}

// ---------------------------------------------------------------------------
// 证书
// ---------------------------------------------------------------------------

async function loadCerts() {
  const r = await api('GET', '/v1/certs');
  if (!r.ok) {
    $('certList').innerHTML =
      '<p class="err">' + t('web.common.read_failed', '读取失败：') + esc(explain(r)) + '</p>';
    return;
  }

  const data = await r.json();
  const items = data.items || [];
  const box = $('certList');

  if (items.length === 0) {
    box.innerHTML = '<p class="hint">' + t('web.cert.empty', '还没有任何证书。') +
      t('web.cert.empty_hint', '为一条路由启用 HTTPS 之后，内核会自动申请证书。') + '</p>';
    return;
  }

  let html = '';
  for (const c of items) {
    const icon = c.needs_renew ? '⚠️' : '✅';
    html += '<div class="card">' +
      '<strong>' + icon + ' ' + esc(c.name) + '</strong>';

    if (c.domains && c.domains.length) {
      html += '<div class="hint">' + t('web.cert.covers', '覆盖：') + esc(c.domains.join('、')) + '</div>';
    }
    if (c.expires_at) {
      html += '<div class="hint">有效期至 ' +
        esc(new Date(c.expires_at).toLocaleDateString()) + '（还剩 ' +
        esc(daysUntil(c.expires_at)) + ' 天）</div>';
    }

    // 测试环境的证书必须显著标出 —— 它不被浏览器信任，
    // 而用户在界面上只会看到「证书无效」。
    if (c.staging) {
      html += '<div class="err">⚠ 这张证书来自 ACME <strong>测试环境</strong>，' +
        '浏览器不会信任它。要拿到正式证书，请清空 acme_directory 后重新续期。</div>';
    }

    if (c.needs_renew && c.reason) {
      html += '<div class="hint">' + t('web.cert.needs_renewal', '需要续期：') + esc(c.reason) + '</div>';
    }
    if (c.error) {
      html += '<div class="err">' + t('web.cert.last_error', '上次失败：') + esc(c.error) + '</div>';
    }

    html += '</div>';
  }
  box.innerHTML = html;
}

function daysUntil(iso) {
  return Math.floor((new Date(iso).getTime() - Date.now()) / 86400000);
}

async function renewCerts() {
  toast(t('web.cert.checking', '正在检查并续期，可能需要一两分钟…'), 'ok');
  const r = await api('POST', '/v1/certs/renew');
  if (!r.ok) { toast('续期失败：' + explain(r), 'err'); return; }

  const data = await r.json();
  toast('检查完成，共 ' + ((data.items || []).length) + ' 张证书', 'ok');
  loadCerts();
}

// ---------------------------------------------------------------------------
// 通知
// ---------------------------------------------------------------------------

async function loadNotify() {
  const [channels, deliveries] = await Promise.all([
    api('GET', '/v1/notify/channels'),
    api('GET', '/v1/notify/deliveries'),
  ]);

  const cl = channels.ok ? await channels.json() : { items: [] };
  const dl = deliveries.ok ? await deliveries.json() : { items: [] };

  state.notifyChannels = cl.items || [];
  renderNotifyChannels();
  renderDeliveries(dl.items || []);
}

function renderNotifyChannels() {
  const box = $('ntList');
  const items = state.notifyChannels || [];

  if (items.length === 0) {
    box.innerHTML = '<p class="hint">' + t('web.nt.empty', '还没有配置任何通道。') +
      t('web.nt.empty_hint', '（日志通道始终可用，通知会出现在事件流里。）') + '</p>';
    return;
  }

  let html = '<table><thead><tr>' +
    '<th>' + t('web.th.name', '名称') + '</th><th>' + t('web.th.kind', '类型') + '</th><th>' + t('web.th.target', '目标') + '</th><th>' + t('web.th.level', '级别') + '</th><th>' + t('web.th.status', '状态') + '</th><th></th>' +
    '</tr></thead><tbody>';

  for (const c of items) {
    html += '<tr>' +
      '<td>' + esc(c.name) + '</td>' +
      '<td>' + esc(c.kind) + '</td>' +
      '<td><code>' + esc(c.url || '—') + '</code></td>' +
      '<td>' + esc(c.min_severity || 'info') + '</td>' +
      '<td>' + (c.enabled === false ? t('web.common.disabled', '停用') : t('web.common.enabled', '启用')) + '</td>' +
      '<td class="actions">' +
      '<button data-editnt="' + esc(c.id) + '">' + t('web.common.edit', '编辑') + '</button>' +
      '<button data-delnt="' + esc(c.id) + '" class="danger">' + t('web.common.delete', '删除') + '</button>' +
      '</td></tr>';
  }

  box.innerHTML = html + '</tbody></table>';
}

function renderNotifyForm(existing) {
  const form = $('ntForm');
  const c = existing || {
    id: '', name: '', kind: 'webhook', enabled: true,
    url: '', method: 'POST', body_template: '', min_severity: 'info',
  };

  form.hidden = false;
  form.innerHTML =
    '<h3>' + (existing ? t('web.nt.edit', '编辑通道') : t('web.nt.new', '新增通道')) + '</h3>' +
    '<label>' + t('web.th.name', '名称') + ' <input id="ntName" value="' + esc(c.name) + '"></label>' +
    '<label>' + t('web.th.kind', '类型') + ' <select id="ntKind">' +
    '<option value="webhook"' + (c.kind === 'webhook' ? ' selected' : '') +
    '>Webhook</option>' +
    '<option value="log"' + (c.kind === 'log' ? ' selected' : '') +
    '>' + t('web.nt.kind_log', '日志') + '</option></select></label>' +
    '<label>' + t('web.nt.l_url', '目标地址') + ' <input id="ntUrl" placeholder="https://…" value="' +
    esc(c.url) + '"></label>' +
    '<label>' + t('web.nt.l_severity', '最低级别') + ' <select id="ntSeverity">' +
    ['info', 'warning', 'error'].map((s) =>
      '<option value="' + s + '"' + (c.min_severity === s ? ' selected' : '') +
      '>' + s + '</option>').join('') +
    '</select></label>' +
    '<label>' + t('web.nt.l_template', '请求体模板（留空用默认 JSON）') +
    '<textarea id="ntTemplate" rows="4" placeholder=\'{"text":"{{.Title}}"}\'>' +
    esc(c.body_template || '') + '</textarea></label>' +
    '<p class="hint">' +
    '可用变量：<code>{{.Event}}</code> <code>{{.Title}}</code> ' +
    '<code>{{.Body}}</code> <code>{{.Severity}}</code> <code>{{.At}}</code>（RFC3339）。' +
    '<br>模板<strong>语法错误会在保存时被拒绝</strong>，而不是等到发送时 —— ' +
    '否则你看到的会是「通知发不出去」，而真正的问题是少了一个括号。' +
    '</p>' +
    '<label class="check"><input type="checkbox" id="ntEnabled"' +
    (c.enabled === false ? '' : ' checked') + '> 启用</label>' +
    '<div class="row"><button id="ntSave" class="primary">' + t('web.common.save', '保存') + '</button>' +
    '<button id="ntCancel">取消</button></div>';

  $('ntCancel').onclick = () => { form.hidden = true; };
  $('ntSave').onclick = () => saveNotifyChannel(c.id);
}

async function saveNotifyChannel(id) {
  const name = $('ntName').value.trim();
  if (!name) { toast(t('web.nt.need_name', '请填名称'), 'err'); return; }

  const ch = {
    id: id || ('c' + Date.now()),
    name: name,
    kind: $('ntKind').value,
    enabled: $('ntEnabled').checked,
    url: $('ntUrl').value.trim(),
    min_severity: $('ntSeverity').value,
  };
  const tmpl = $('ntTemplate').value;
  if (tmpl.trim()) ch.body_template = tmpl;

  const next = (state.notifyChannels || []).filter((x) => x.id !== ch.id);
  next.push(ch);

  const r = await api('PUT', '/v1/notify/channels', { items: next });
  if (!r.ok) { toast('保存失败：' + explain(r), 'err'); return; }

  toast(t('web.common.saved', '已保存'), 'ok');
  $('ntForm').hidden = true;
  loadNotify();
}

async function deleteNotifyChannel(id) {
  if (!confirm(t('web.nt.confirm_delete', '确定删除这个通道？'))) return;

  const next = (state.notifyChannels || []).filter((x) => x.id !== id);
  const r = await api('PUT', '/v1/notify/channels', { items: next });
  if (!r.ok) { toast('删除失败：' + explain(r), 'err'); return; }

  toast(t('web.common.deleted', '已删除'), 'ok');
  loadNotify();
}

function renderDeliveries(items) {
  const box = $('ntDeliveries');
  if (items.length === 0) {
    box.innerHTML = '<p class="hint">' + t('web.nt.no_deliveries', '还没有投递记录。') + '</p>';
    return;
  }

  let html = '<table><thead><tr>' +
    '<th>' + t('web.th.time', '时间') + '</th><th>' + t('web.th.channel', '通道') + '</th><th>' + t('web.th.kind', '类型') + '</th><th>' + t('web.th.result', '结果') + '</th>' +
    '</tr></thead><tbody>';

  for (const d of items) {
    html += '<tr>' +
      '<td>' + esc(new Date(d.at).toLocaleString()) + '</td>' +
      '<td>' + esc(d.channel) + '</td>' +
      '<td>' + esc(d.kind) + '</td>' +
      '<td>' + (d.ok ? '✅' : '❌ ' + esc(d.error || '')) + '</td>' +
      '</tr>';
  }
  box.innerHTML = html + '</tbody></table>';
}

async function testNotify() {
  const r = await api('POST', '/v1/notify/test', {});
  if (!r.ok) { toast('测试失败：' + explain(r), 'err'); return; }

  const data = await r.json();
  const items = data.items || [];

  // 逐个通道报结果 —— 某个通道配错了应当立刻看得出来。
  const failed = items.filter((d) => !d.ok);
  if (failed.length === 0) {
    toast('测试通知已发送到 ' + items.length + ' 个通道', 'ok');
  } else {
    toast('有 ' + failed.length + ' 个通道失败：' +
      failed.map((d) => d.channel).join('、'), 'err');
  }
  loadNotify();
}

// ---------------------------------------------------------------------------
// 系统服务
// ---------------------------------------------------------------------------

async function loadService() {
  const r = await api('GET', '/v1/service/status');
  const box = $('svcState');

  if (!r.ok) {
    box.innerHTML = '<span class="err">读取失败：' + esc(explain(r)) + '</span>';
    return;
  }

  const s = await r.json();

  // 先报「内核在不在跑」：那是用户最关心的，而且它总是有答案。
  let html = s.daemon_reachable
    ? '▶ 内核：<strong>运行中</strong>（本地接口可连通）'
    : '⏹ 内核：<strong>未运行</strong>';

  if (s.status) {
    html += '<br>系统服务：' + (s.status === 'running' ? '运行中' : '未运行') +
      '（' + esc(s.backend) + '）';
  } else if (s.error) {
    // 服务查询失败不让整块失败 —— 上面那行已经回答了主要问题。
    html += '<br>系统服务：无法查询（' + esc(s.backend) + '）' +
      '<br><span class="err">' + esc(s.error) + '</span>';
  }

  box.innerHTML = html;
}

async function serviceAction(action) {
  const r = await api('POST', '/v1/service/' + action, {
    auto_start: $('svcAuto').checked,
    restart_on_failure: $('svcRestart').checked,
  });

  if (!r.ok) { toast(action + ' 失败：' + explain(r), 'err'); return; }
  toast('已执行：' + action, 'ok');
  loadService();
}
