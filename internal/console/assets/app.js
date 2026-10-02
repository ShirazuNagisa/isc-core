// 本文件里的文案经 window.iscI18n 取（与 panels.js 同一套别名）。
//
// 第二个参数是**兜底**：消息表没到或 key 不存在时显示它。动态渲染的内容
// 必须保留兜底 —— 这些字符串只在这里出现一次，没有第二个来源。
const t = (key, fallback) => window.iscI18n.t(key, fallback);
const tf = (key, fallback, params) => window.iscI18n.tf(key, fallback, params);
/* ISC 验证控制台
 *
 * 定位：验证内核功能的工具，不是最终 GUI。
 * 因此这里的每个操作都尽量显示**真实发生了什么**（实际请求路径、
 * 服务商返回的原始错误、事件流序号），而不是把它包装成好看的样子。
 *
 * 没有框架、没有构建。原因是内核是纯 Go 的，为验证工具引入 Node 工具链
 * 会让 CI 多一套版本管理与供应链审计，而收益只是写起来舒服一点。
 */
'use strict';

// ---------------------------------------------------------------------------
// 状态
// ---------------------------------------------------------------------------

const state = {
  token: '',
  apiBase: '/v1',
  providers: [],
  credentials: [],
  zones: [],
  ws: null,
  lastEventId: 0,
  events: [],
};

// ---------------------------------------------------------------------------
// 基础工具
// ---------------------------------------------------------------------------

const $ = (id) => document.getElementById(id);

/* esc 把任意值转成安全的 HTML 片段。
 *
 * **每次把数据插进 innerHTML 都必须过这里。** 记录名、域名、服务商返回的
 * 错误信息全是用户可控或远端可控的数据 —— 一个恶意构造的 DNS 记录名
 * 就能在本机控制台里执行脚本，而控制台手里握着内核令牌。
 * 本地工具不等于可以不做转义。
 */
function esc(v) {
  if (v === null || v === undefined) return '';
  return String(v)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function status(msg, isErr) {
  const el = $('statusText');
  el.textContent = msg;
  el.style.color = isErr ? 'var(--err)' : '';
}

let toastTimer = null;
function toast(msg, kind) {
  let el = document.querySelector('.toast');
  if (el) el.remove();
  el = document.createElement('div');
  el.className = 'toast ' + (kind || '');
  el.textContent = msg;
  document.body.appendChild(el);
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.remove(), kind === 'err' ? 9000 : 3800);
}

function shortTime(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  return d.toLocaleString('zh-CN', { hour12: false });
}

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

/* api 发一次带鉴权的请求。
 *
 * 返回 {ok, status, body}。**不抛异常**：调用方需要看到失败时的
 * 状态码与响应体 —— 那正是验证工具最该展示的东西。
 */
async function api(method, path, body) {
  const opts = {
    method,
    headers: { 'Authorization': 'Bearer ' + state.token },
  };
  if (body !== undefined && body !== null) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = typeof body === 'string' ? body : JSON.stringify(body);
  }

  const res = await fetch(path, opts);
  const text = await res.text();

  let parsed = null;
  if (text) {
    try { parsed = JSON.parse(text); } catch { parsed = text; }
  }
  return { ok: res.ok, status: res.status, body: parsed };
}

/* explain 把一次失败的响应转成一句能看懂的话。
 *
 * 内核的错误是 problem+json，其中的 detail 常常直接来自服务商
 *（"记录已存在""令牌权限不足"）—— 那恰恰是用户能据此行动的信息，
 * 因此要原样展示，而不是替换成"操作失败"。
 */
function explain(r) {
  if (r.body && typeof r.body === 'object') {
    const title = r.body.title || '';
    const detail = r.body.detail || '';
    if (title && detail) return title + '：' + detail;
    if (detail) return detail;
    if (title) return title;
  }
  if (typeof r.body === 'string' && r.body) return r.body.slice(0, 300);
  return 'HTTP ' + r.status;
}

// ---------------------------------------------------------------------------
// 引导
// ---------------------------------------------------------------------------

async function boot() {
  // 等消息表就绪。
  //
  // 不等的话，动态渲染出来的内容（表格、状态行）会先以中文出现，
  // 而静态部分稍后被替换 —— 页面上会同时存在两种语言。
  // 取不到表时那个 Promise 也会 resolve（见 i18n.js），因此这里不会卡住。
  if (window.iscI18n) { await window.iscI18n.ready; }
  try {
    const res = await fetch('/v1/console/bootstrap');
    if (!res.ok) {
      throw new Error('HTTP ' + res.status + ' —— ' + await res.text());
    }
    const info = await res.json();
    state.token = info.token;
    state.apiBase = info.api_base || '/v1';
    $('pillVersion').textContent = info.version || '—';
  } catch (err) {
    const el = $('bootError');
    el.hidden = false;
    el.innerHTML =
      '<strong>无法取得访问令牌。</strong> ' +
      '请确认是通过 <code>http://127.0.0.1:端口/</code> 打开的控制台 ' +
      '（不是主机名、也不是局域网 IP —— 内核会拒绝非本机 Host 的请求，' +
      '这是为了防 DNS rebinding）。<br>底层错误：' + esc(err.message);
    $('pillConn').textContent = t('web.conn.down', '未连接');
    $('pillConn').className = 'pill err';
    return;
  }

  try {
    const h = await api('GET', '/v1/health');
    if (h.ok) {
      $('pillConn').textContent = t('web.conn.up', '已连接');
      $('pillConn').className = 'pill ok';
    } else {
      $('pillConn').textContent = t('web.conn.auth_failed', '鉴权失败');
      $('pillConn').className = 'pill err';
    }
  } catch {
    $('pillConn').textContent = t('web.conn.failed', '连接失败');
    $('pillConn').className = 'pill err';
  }

  // 服务商列表是所有表单的基础，先拿到。
  const prov = await api('GET', '/v1/providers');
  if (prov.ok && prov.body && prov.body.items) {
    state.providers = prov.body.items;
  }

  await Promise.all([loadOverview(), loadIP(), loadCredentials()]);
}

// ---------------------------------------------------------------------------
// 概览
// ---------------------------------------------------------------------------

async function loadOverview() {
  const meta = await api('GET', '/v1/meta');
  if (!meta.ok) {
    $('overviewCards').innerHTML =
      '<div class="empty">' + t('web.err.read_meta', '读取内核信息失败：') + esc(explain(meta)) + '</div>';
    return;
  }
  const m = meta.body;

  const cards = [
    [t('web.meta.version', '版本'), m.version],
    [t('web.meta.api_version', '接口版本'), m.api_version],
    [t('web.meta.os', '操作系统'), m.os + ' / ' + m.arch],
    [t('web.meta.started', '启动时间'), shortTime(m.started_at)],
    [t('web.meta.commit', '提交'), (m.commit || '').slice(0, 12) || '—'],
    [t('web.meta.build_time', '构建时间'), m.build_time || '—'],
  ];
  $('overviewCards').innerHTML = cards.map(([k, v]) =>
    '<div class="card"><div class="k">' + esc(k) + '</div>' +
    '<div class="v">' + esc(v) + '</div></div>').join('');

  const caps = m.capabilities || {};
  const labels = {
    firewall: t('web.cap.firewall', '防火墙编排'), service_manager: t('web.cap.service_manager', '服务管理（自启）'),
    ip_monitor: t('web.cap.ip_monitor', 'IP / 前缀监控'), secret_store: t('web.cap.secret_store', '密钥库'),
    transport: t('web.cap.transport', '本地传输'), low_port_binder: t('web.cap.low_port', '低端口绑定'),
  };
  const rows = Object.keys(labels).map((key) => {
    const c = caps[key] || {};
    const badge = c.available
      ? '<span class="badge ok">' + t('web.cap.available', '可用') + '</span>'
      : '<span class="badge warn">' + t('web.cap.guided', '引导模式') + '</span>';
    return '<tr><td>' + esc(labels[key]) + '</td>' +
      '<td>' + badge + '</td>' +
      '<td class="mono">' + esc(c.backend || '—') + '</td>' +
      '<td>' + esc(c.note || '') + '</td></tr>';
  });
  $('capTable').querySelector('tbody').innerHTML = rows.join('');
}

// ---------------------------------------------------------------------------
// IP 与前缀
// ---------------------------------------------------------------------------

async function loadIP() {
  const r = await api('GET', '/v1/ip/current');
  if (!r.ok) {
    $('ipList').innerHTML = '<div class="empty">' + t('web.common.read_failed') + esc(explain(r)) + '</div>';
    return;
  }
  const s = r.body;

  $('ipPrimary').innerHTML = [
    [t('web.ip.primary_v6', '主 IPv6'), s.primary_ipv6],
    [t('web.ip.primary_prefix', '主前缀'), s.primary_prefix],
    [t('web.ip.primary_v4', '主 IPv4'), s.primary_ipv4],
  ].map(([k, v]) =>
    '<div class="card"><div class="k">' + esc(k) + '</div>' +
    '<div class="v big ' + (v ? 'accent' : '') + '">' +
    esc(v || t('web.ip.none', '（无 —— 该机器没有可用的公网地址）')) + '</div></div>').join('');

  const list = s.interfaces || [];
  if (!list.length) {
    $('ipList').innerHTML =
      '<div class="empty">没有可用于解析的网卡（已排除回环、虚拟、' +
      '以及只有链路本地地址的接口）。</div>';
    return;
  }

  $('ipList').innerHTML = list.map((i) => {
    const v4 = (i.ipv4 || []).map((a) => '<span class="badge">' + esc(a) + '</span>').join(' ');
    const v6 = (i.global_ipv6 || []).map((a) => '<span class="badge accent">' + esc(a) + '</span>').join(' ');
    const px = (i.prefixes || []).map((p) => '<span class="badge warn">' + esc(p) + '</span>').join(' ');
    const up = i.is_up ? '<span class="badge ok">up</span>' : '<span class="badge muted">down</span>';
    return '<table class="data"><thead><tr><th colspan="2">' +
      esc(i.name) + ' ' + up + '</th></tr></thead><tbody>' +
      '<tr><td style="width:90px">IPv4</td><td>' + (v4 || '—') + '</td></tr>' +
      '<tr><td>IPv6</td><td>' + (v6 || '—') + '</td></tr>' +
      '<tr><td>' + t('web.ip.primary_prefix') + '</td><td>' + (px || '—') + '</td></tr>' +
      '</tbody></table>';
  }).join('');
}

// ---------------------------------------------------------------------------
// 凭据
// ---------------------------------------------------------------------------

async function loadCredentials() {
  const r = await api('GET', '/v1/credentials?limit=200');
  if (!r.ok) {
    $('credList').innerHTML = '<div class="empty">' + t('web.common.read_failed') + esc(explain(r)) + '</div>';
    return;
  }
  state.credentials = (r.body && r.body.items) || [];

  if (!state.credentials.length) {
    $('credList').innerHTML = '<div class="empty">' + t('web.cred.empty', '还没有配置任何凭据。') + '</div>';
    renderRecordCredPicker();
    return;
  }

  const rows = state.credentials.map((c) => {
    const p = providerByName(c.provider);
    const caps = (p && p.capabilities) || {};
    const tags = [];
    if (caps.dynamic) tags.push('<span class="badge accent">' + t('web.cap.dynamic', '动态解析') + '</span>');
    if (caps.zone_list) tags.push('<span class="badge">' + t('web.cap.zones', '记录管理') + '</span>');
    if (caps.verify) tags.push('<span class="badge">' + t('web.cap.verify', '可校验') + '</span>');
    if (!caps.available) tags.push('<span class="badge muted">' + t('web.cap.unimplemented', '未实现') + '</span>');

    const fields = Object.keys(c.fields || {})
      .map((k) => esc(k) + '=' + esc(c.fields[k])).join('  ');

    return '<tr>' +
      '<td>' + esc(c.label) + '</td>' +
      '<td>' + esc((p && p.display_name) || c.provider) + '</td>' +
      '<td>' + tags.join(' ') + '</td>' +
      '<td class="mono">' + fields + '</td>' +
      '<td class="mono">' + esc(c.id) + '</td>' +
      '<td class="actions">' +
        (caps.verify ? '<button class="tiny" data-verify="' + esc(c.id) + '">测试连接</button> ' : '') +
        '<button class="tiny danger" data-delcred="' + esc(c.id) + '" ' +
          'data-label="' + esc(c.label) + '">' + t('web.common.delete') + '</button>' +
      '</td></tr>';
  }).join('');

  $('credList').innerHTML =
    '<table class="data"><thead><tr><th>' + t('web.th.name') + '</th><th>' + t('web.cred.provider') + '</th><th>' + t('web.th.caps', '能力') + '</th>' +
    '<th>' + t('web.th.credential', '凭据') + '</th><th>ID</th><th></th></tr></thead><tbody>' + rows + '</tbody></table>';

  renderRecordCredPicker();
}

function providerByName(name) {
  return state.providers.find((p) => p.name === name);
}

function renderCredForm() {
  const el = $('credForm');
  el.hidden = false;

  const opts = state.providers
    .filter((p) => p.capabilities && p.capabilities.available)
    .map((p) => '<option value="' + esc(p.name) + '">' +
      esc(p.display_name || p.name) + '</option>').join('');

  el.innerHTML =
    '<h3>' + t('web.cred.new') + '</h3>' +
    '<div class="form-grid">' +
      '<label class="field"><span>' + t('web.th.name') + '</span>' +
        '<input id="cfLabel" placeholder="例如：我的 Cloudflare"></label>' +
      '<label class="field"><span>' + t('web.cred.provider', '服务商') + '</span>' +
        '<select id="cfProvider">' + opts + '</select></label>' +
    '</div>' +
    '<div class="form-grid" id="cfFields"></div>' +
    '<div class="row"><button id="cfSave" class="primary">' + t('web.common.save') + '</button>' +
      '<button id="cfCancel">' + t('web.common.cancel') + '</button></div>';

  $('cfProvider').onchange = renderCredFields;
  $('cfCancel').onclick = () => { el.hidden = true; };
  $('cfSave').onclick = saveCredential;
  renderCredFields();
}

function renderCredFields() {
  const name = $('cfProvider').value;
  const p = providerByName(name);
  const fields = (p && p.credential_fields) || [];

  $('cfFields').innerHTML = fields.map((f) => {
    const req = f.required ? '' : t('web.cred.optional', '（可选）');
    const help = f.help ? '<small>' + esc(f.help) + '</small>' : '';
    return '<label class="field"><span>' + esc(f.label || f.key) + req + '</span>' +
      '<input data-field="' + esc(f.key) + '" ' +
        (f.secret ? 'type="password" ' : '') +
        'placeholder="' + esc(f.example || '') + '" autocomplete="off">' +
      help + '</label>';
  }).join('');
}

async function saveCredential() {
  const label = $('cfLabel').value.trim();
  if (!label) { toast(t('web.cred.need_name', '请填写名称'), 'err'); return; }

  const fields = {};
  document.querySelectorAll('#cfFields [data-field]').forEach((inp) => {
    if (inp.value.trim()) fields[inp.dataset.field] = inp.value.trim();
  });

  const r = await api('POST', '/v1/credentials', {
    label, provider: $('cfProvider').value, fields,
  });
  if (!r.ok) { toast(t('web.common.save_failed', '保存失败：') + explain(r), 'err'); return; }

  toast(t('web.cred.created', '凭据已创建'), 'ok');
  $('credForm').hidden = true;
  await loadCredentials();
}

async function verifyCredential(id) {
  status(t('web.cred.testing', '正在测试连接…'));
  const r = await api('POST', '/v1/credentials/' + encodeURIComponent(id) + '/verify');
  if (!r.ok) { toast(t('web.common.test_failed', '测试失败：') + explain(r), 'err'); status(t('web.raw.ready'), true); return; }

  const b = r.body || {};
  if (b.ok) {
    toast(t('web.cred.ok', '连接正常') + (b.message ? '：' + b.message : ''), 'ok');
    status(t('web.raw.ready'));
  } else {
    toast(t('web.cred.not_passed') + '：' + (b.message || t('web.common.unknown')), 'err');
    status(t('web.cred.not_passed', '测试未通过'), true);
  }
}

// ---------------------------------------------------------------------------
// 动态解析任务
// ---------------------------------------------------------------------------

async function loadTasks() {
  const r = await api('GET', '/v1/ddns-tasks');
  if (!r.ok) {
    $('taskList').innerHTML = '<div class="empty">' + t('web.common.read_failed') + esc(explain(r)) + '</div>';
    return;
  }
  const items = (r.body && r.body.items) || [];
  if (!items.length) {
    $('taskList').innerHTML = '<div class="empty">' + t('web.task.empty', '还没有配置动态解析任务。') + '</div>';
    return;
  }

  const rows = items.map((t) => {
    const st = t.last_status || '';
    let badge = '<span class="badge muted">' + t('web.task.never', '从未执行') + '</span>';
    if (st === 'success') badge = '<span class="badge ok">' + t('web.task.updated', '已更新') + '</span>';
    else if (st === 'failed') badge = '<span class="badge err">' + t('web.task.failed', '失败') + '</span>';
    else if (st === 'unchanged') badge = '<span class="badge">' + t('web.task.unchanged', '无需改动') + '</span>';

    const src = [];
    if (t.ipv4 && t.ipv4.enable) {
      src.push('A ← ' + esc(t.ipv4.get_type) + ' → ' +
        esc((t.ipv4.domains || []).join(', ')));
    }
    if (t.ipv6 && t.ipv6.enable) {
      src.push('AAAA ← ' + esc(t.ipv6.get_type) + ' → ' +
        esc((t.ipv6.domains || []).join(', ')));
    }

    return '<tr>' +
      '<td>' + (t.enabled ? '<span class="badge ok">' + t('web.common.enabled') + '</span>' :
                            '<span class="badge muted">' + t('web.common.disabled') + '</span>') + '<br>' +
        esc(t.label) + '</td>' +
      '<td class="mono">' + src.join('<br>') + '</td>' +
      '<td>' + badge + '<br><small>' + esc(t.last_message || '') + '</small></td>' +
      '<td class="mono">' + esc(t.last_ipv4 || '') +
        (t.last_ipv6 ? '<br>' + esc(t.last_ipv6) : '') + '<br>' +
        '<small>' + esc(shortTime(t.last_run_at)) + '</small></td>' +
      '<td class="actions">' +
        '<button class="tiny" data-runtask="' + esc(t.id) + '">立即执行</button> ' +
        '<button class="tiny danger" data-deltask="' + esc(t.id) + '" ' +
          'data-label="' + esc(t.label) + '">删除</button>' +
      '</td></tr>';
  }).join('');

  $('taskList').innerHTML =
    '<table class="data"><thead><tr><th>' + t('web.task.th_task', '任务') + '</th><th>' + t('web.task.th_sources', '来源与域名') + '</th>' +
    '<th>' + t('web.th.status') + '</th><th>' + t('web.task.th_last_addr', '上次地址') + '</th><th></th></tr></thead><tbody>' + rows + '</tbody></table>';
}

function renderTaskForm() {
  const el = $('taskForm');
  el.hidden = false;

  const creds = state.credentials.map((c) =>
    '<option value="' + esc(c.id) + '">' + esc(c.label) + '</option>').join('');

  if (!creds) {
    el.innerHTML = '<h3>' + t('web.task.new') + '</h3><div class="empty">' +
      t('web.task.need_cred', '需要先创建一条凭据。') + '</div>';
    return;
  }

  el.innerHTML =
    '<h3>' + t('web.task.new_full', '新建动态解析任务') + '</h3>' +
    '<div class="form-grid">' +
      '<label class="field"><span>' + t('web.th.name') + '</span>' +
        '<input id="tfLabel" placeholder="例如：家里的 IPv6"></label>' +
      '<label class="field"><span>' + t('web.rec.l_cred') + '</span>' +
        '<select id="tfCred">' + creds + '</select></label>' +
    '</div>' +

    '<h3 style="margin-top:18px">IPv6（AAAA）</h3>' +
    '<div class="form-grid">' +
      '<label class="field"><span>' + t('web.common.enabled') + '</span>' +
        '<input type="checkbox" id="tf6Enable" checked></label>' +
      '<label class="field"><span>' + t('web.task.l_getter', '获取方式') + '</span>' +
        '<select id="tf6Type"><option value="netInterface">' + t('web.task.getter_iface', '网卡') + '</option>' +
        '<option value="url">' + t('web.task.getter_url', '外部接口') + '</option>' +
        '<option value="cmd">命令</option></select></label>' +
      '<label class="field full"><span>取值</span>' +
        '<input id="tf6Value" placeholder="网卡名（如 WLAN / eth0）"></label>' +
      '<label class="field"><span>地址选择器（可选）</span>' +
        '<input id="tf6Sel" placeholder="@1 或正则"></label>' +
      '<label class="field full"><span>域名（每行一个）</span>' +
        '<textarea id="tf6Domains" rows="2" ' +
        'placeholder="home.example.com"></textarea></label>' +
    '</div>' +

    '<h3 style="margin-top:18px">IPv4（A）</h3>' +
    '<div class="form-grid">' +
      '<label class="field"><span>' + t('web.common.enabled') + '</span>' +
        '<input type="checkbox" id="tf4Enable"></label>' +
      '<label class="field"><span>' + t('web.task.l_getter', '获取方式') + '</span>' +
        '<select id="tf4Type"><option value="url">外部接口</option>' +
        '<option value="netInterface">网卡</option>' +
        '<option value="cmd">命令</option></select></label>' +
      '<label class="field full"><span>取值</span>' +
        '<input id="tf4Value" placeholder="https://api.ipify.org"></label>' +
      '<label class="field full"><span>域名（每行一个）</span>' +
        '<textarea id="tf4Domains" rows="2"></textarea></label>' +
    '</div>' +

    '<div class="row"><button id="tfSave" class="primary">保存并执行</button>' +
      '<button id="tfCancel">取消</button></div>';

  $('tfCancel').onclick = () => { el.hidden = true; };
  $('tfSave').onclick = saveTask;

  // 网卡来源的取值提示改用下拉，避免用户手打网卡名打错 ——
  // 打错的症状是"任务一直失败但看不出原因"。
  fillNetInterfaces();
}

async function fillNetInterfaces() {
  const r = await api('GET', '/v1/ip/current');
  if (!r.ok) return;
  const names = ((r.body && r.body.interfaces) || []).map((i) => i.name);
  const dl = document.createElement('datalist');
  dl.id = 'ifaceNames';
  dl.innerHTML = names.map((n) => '<option value="' + esc(n) + '">').join('');
  document.body.appendChild(dl);
  const inp = $('tf6Value');
  if (inp) inp.setAttribute('list', 'ifaceNames');
}

function splitLines(v) {
  return v.split('\n').map((s) => s.trim()).filter(Boolean);
}

async function saveTask() {
  const label = $('tfLabel').value.trim();
  if (!label) { toast('请填写任务名称', 'err'); return; }

  const body = {
    credential_id: $('tfCred').value,
    label,
    enabled: true,
    ipv4: {
      enable: $('tf4Enable').checked,
      get_type: $('tf4Type').value,
      value: $('tf4Value').value.trim(),
      domains: splitLines($('tf4Domains').value),
    },
    ipv6: {
      enable: $('tf6Enable').checked,
      get_type: $('tf6Type').value,
      value: $('tf6Value').value.trim(),
      selector: $('tf6Sel').value.trim(),
      domains: splitLines($('tf6Domains').value),
    },
    ttl: '',
    http_interface: '',
  };

  const r = await api('POST', '/v1/ddns-tasks', body);
  if (!r.ok) { toast(t('web.common.save_failed', '保存失败：') + explain(r), 'err'); return; }

  toast('任务已创建，正在执行首次解析…', 'ok');
  $('taskForm').hidden = true;
  await loadTasks();

  // 首次执行由调度器在后台完成，隔一会儿刷新一次状态 ——
  // 用户按下"保存"时的期待是马上看到结果。
  setTimeout(loadTasks, 1800);
  setTimeout(loadTasks, 4500);
}

async function runTask(id) {
  const r = await api('POST', '/v1/ddns-tasks/' + encodeURIComponent(id) + '/run');
  if (!r.ok) { toast('触发失败：' + explain(r), 'err'); return; }
  toast('已受理，正在执行…', 'ok');
  setTimeout(loadTasks, 1500);
  setTimeout(loadTasks, 4000);
}

// ---------------------------------------------------------------------------
// DNS 记录
// ---------------------------------------------------------------------------

function renderRecordCredPicker() {
  const sel = $('recCred');
  const managed = state.credentials.filter((c) => {
    const p = providerByName(c.provider);
    return p && p.capabilities && p.capabilities.zone_list;
  });

  if (!managed.length) {
    sel.innerHTML = '<option value="">（没有支持记录管理的凭据）</option>';
    $('recCapNote').textContent =
      '记录管理仅对 Tier-1 服务商可用（Cloudflare / 阿里云 / 腾讯云 / ' +
      'DNSPod / 华为云 / GoDaddy）。Tier-2 服务商只提供动态解析。';
    return;
  }
  $('recCapNote').textContent = '';
  sel.innerHTML = managed.map((c) =>
    '<option value="' + esc(c.id) + '">' + esc(c.label) + '</option>').join('');
  loadZones();
}

async function loadZones() {
  const credId = $('recCred').value;
  const zoneSel = $('recZone');
  if (!credId) return;

  zoneSel.disabled = true;
  zoneSel.innerHTML = '<option>加载中…</option>';

  const r = await api('GET', '/v1/credentials/' + encodeURIComponent(credId) + '/zones');
  if (!r.ok) {
    zoneSel.innerHTML = '<option>读取失败</option>';
    $('recList').innerHTML = '<div class="empty">读取区域失败：' +
      esc(explain(r)) + '</div>';
    return;
  }

  state.zones = (r.body && r.body.items) || [];
  if (!state.zones.length) {
    zoneSel.innerHTML = '<option value="">（该账号下没有活跃域名）</option>';
    return;
  }
  zoneSel.disabled = false;
  zoneSel.innerHTML = state.zones.map((z) =>
    '<option value="' + esc(z.id) + '">' + esc(z.name) + '</option>').join('');
  await loadRecords();
}

async function loadRecords() {
  const credId = $('recCred').value;
  const zoneId = $('recZone').value;
  if (!credId || !zoneId) return;

  $('recNew').disabled = false;
  const path = '/v1/credentials/' + encodeURIComponent(credId) +
    '/zones/' + encodeURIComponent(zoneId) + '/records';

  const r = await api('GET', path);
  if (!r.ok) {
    $('recList').innerHTML = '<div class="empty">读取记录失败：' +
      esc(explain(r)) + '</div>';
    return;
  }

  const items = (r.body && r.body.items) || [];
  if (!items.length) {
    $('recList').innerHTML = '<div class="empty">该区域下没有记录。</div>';
    return;
  }

  const rows = items.map((rec) => {
    const tags = [];
    if (rec.proxied) tags.push('<span class="badge accent">代理</span>');
    if (rec.priority) tags.push('<span class="badge">优先级 ' + esc(rec.priority) + '</span>');

    return '<tr>' +
      '<td><span class="badge accent">' + esc(rec.type) + '</span></td>' +
      '<td class="mono">' + esc(rec.name) + '</td>' +
      '<td class="mono">' + esc(rec.content) + '</td>' +
      '<td class="mono">' + (rec.ttl ? esc(rec.ttl) : 'auto') + '</td>' +
      '<td>' + tags.join(' ') + '</td>' +
      '<td class="mono">' + esc(rec.id) + '</td>' +
      '<td class="actions">' +
        '<button class="tiny" data-editrec="' + esc(rec.id) + '">编辑</button> ' +
        '<button class="tiny danger" data-delrec="' + esc(rec.id) + '" ' +
          'data-name="' + esc(rec.name) + '" data-type="' + esc(rec.type) + '">删除</button>' +
      '</td></tr>';
  }).join('');

  $('recList').innerHTML =
    '<p class="hint">共 ' + items.length + ' 条记录。</p>' +
    '<table class="data"><thead><tr><th>类型</th><th>名称</th><th>内容</th>' +
    '<th>TTL</th><th></th><th>ID</th><th></th></tr></thead><tbody>' +
    rows + '</tbody></table>';

  state.records = items;
}

function renderRecordForm(existing) {
  const el = $('recForm');
  el.hidden = false;
  const rec = existing || {};

  el.innerHTML =
    '<h3>' + (existing ? '编辑记录' : '新增记录') + '</h3>' +
    '<div class="form-grid">' +
      '<label class="field"><span>类型</span>' +
        '<input id="rfType" value="' + esc(rec.type || 'A') + '" ' +
          (existing ? 'readonly' : '') + '></label>' +
      '<label class="field"><span>名称（完整域名）</span>' +
        '<input id="rfName" value="' + esc(rec.name || '') + '" spellcheck="false"></label>' +
      '<label class="field full"><span>内容</span>' +
        '<input id="rfContent" value="' + esc(rec.content || '') + '" spellcheck="false"></label>' +
      '<label class="field"><span>TTL（秒，0 = 服务商默认）</span>' +
        '<input id="rfTTL" type="number" min="0" value="' + esc(rec.ttl || 0) + '"></label>' +
      '<label class="field"><span>优先级（MX / SRV）</span>' +
        '<input id="rfPrio" type="number" min="0" value="' + esc(rec.priority || 0) + '"></label>' +
      '<label class="field"><span>CDN 代理</span>' +
        '<input type="checkbox" id="rfProxied" ' + (rec.proxied ? 'checked' : '') + '></label>' +
      '<label class="field full"><span>备注</span>' +
        '<input id="rfComment" value="' + esc(rec.comment || '') + '"></label>' +
    '</div>' +
    '<div class="row"><button id="rfSave" class="primary">保存</button>' +
      '<button id="rfCancel">取消</button></div>';

  $('rfCancel').onclick = () => { el.hidden = true; };
  $('rfSave').onclick = () => saveRecord(existing ? rec.id : null);
}

async function saveRecord(recordId) {
  const credId = $('recCred').value;
  const zoneId = $('recZone').value;

  const body = {
    name: $('rfName').value.trim(),
    type: $('rfType').value.trim().toUpperCase(),
    content: $('rfContent').value,
    ttl: Number($('rfTTL').value) || 0,
    priority: Number($('rfPrio').value) || 0,
    proxied: $('rfProxied').checked,
    comment: $('rfComment').value.trim(),
  };
  if (!body.name) { toast('请填写记录名', 'err'); return; }

  const base = '/v1/credentials/' + encodeURIComponent(credId) +
    '/zones/' + encodeURIComponent(zoneId) + '/records';

  let r;
  if (recordId) {
    r = await api('PUT', base + '/' + encodeURIComponent(recordId), body);
  } else {
    r = await api('POST', base, body);
  }

  if (!r.ok) { toast(t('web.common.save_failed', '保存失败：') + explain(r), 'err'); return; }

  toast(recordId ? '记录已更新' : '记录已创建', 'ok');
  $('recForm').hidden = true;
  await loadRecords();
}

async function deleteRecord(recordId, name, type) {
  const p = providerByName(currentCredProvider());
  const note = p && (p.name === 'godaddy' || p.name === 'huaweicloud')
    ? '\n\n注意：这家服务商的删除会波及同名的其它值（见 docs/PROVIDER-MATRIX.md）。'
    : '';
  if (!confirm('确定删除 ' + type + ' 记录 ' + name + ' ？' + note)) return;

  const credId = $('recCred').value;
  const zoneId = $('recZone').value;
  const r = await api('DELETE', '/v1/credentials/' + encodeURIComponent(credId) +
    '/zones/' + encodeURIComponent(zoneId) + '/records/' + encodeURIComponent(recordId));

  if (!r.ok) { toast('删除失败：' + explain(r), 'err'); return; }
  toast('记录已删除', 'ok');
  await loadRecords();
}

function currentCredProvider() {
  const c = state.credentials.find((x) => x.id === $('recCred').value);
  return c ? c.provider : '';
}

// ---------------------------------------------------------------------------
// 事件流
// ---------------------------------------------------------------------------

function connectEvents() {
  if (state.ws) return;

  // 浏览器无法为 WebSocket 设置请求头，因此令牌走子协议传递。
  // 服务端会回显这个子协议，所以我们能确认它被接受了。
  const proto = 'isc.token.' + state.token;
  const url = (location.protocol === 'https:' ? 'wss://' : 'ws://') +
    location.host + '/v1/events';

  let ws;
  try {
    ws = new WebSocket(url, [proto]);
  } catch (err) {
    toast('建立事件流失败：' + err.message, 'err');
    return;
  }
  state.ws = ws;

  ws.onopen = () => {
    $('evConnect').disabled = true;
    $('evDisconnect').disabled = false;
    $('pillConn').textContent = '已连接（事件流）';
    status('事件流已连接');
  };

  ws.onmessage = (ev) => {
    let payload;
    try { payload = JSON.parse(ev.data); } catch { payload = { raw: ev.data }; }
    if (payload.seq) state.lastEventId = payload.seq;
    pushEvent(payload);
  };

  ws.onclose = () => {
    state.ws = null;
    $('evConnect').disabled = false;
    $('evDisconnect').disabled = true;
    status('事件流已断开');
  };

  ws.onerror = () => {
    // 浏览器出于安全考虑不暴露错误细节，只能给出通用提示。
    status('事件流出错（可能是令牌被拒）', true);
  };

  // 断线后自动重连，并带上 lastEventId 请求补发。
  ws.addEventListener('close', () => {
    if (!state.autoReconnect) return;
    setTimeout(() => { if (state.autoReconnect) connectEvents(); }, 2000);
  });
  state.autoReconnect = true;
}

function disconnectEvents() {
  state.autoReconnect = false;
  if (state.ws) state.ws.close();
  state.ws = null;
}

function pushEvent(payload) {
  state.events.push(payload);
  if (state.events.length > 500) state.events.shift();

  const log = $('evLog');
  const div = document.createElement('div');
  div.className = 'ev' + (payload.type === 'events.gap' ? ' gap' : '');

  const data = Object.assign({}, payload);
  delete data.seq; delete data.type; delete data.at;

  div.innerHTML =
    '<span class="seq">' + esc(payload.seq || '') + '</span>' +
    '<span class="type">' + esc(payload.type || '') + '</span>' +
    '<span class="data">' + esc(JSON.stringify(data)) + '</span>';
  log.appendChild(div);

  if ($('evFollow').checked) log.scrollTop = log.scrollHeight;
}

// ---------------------------------------------------------------------------
// 原始接口
// ---------------------------------------------------------------------------

async function sendRaw() {
  const method = $('rawMethod').value;
  const path = $('rawPath').value.trim();
  const bodyText = $('rawBody').value.trim();

  if (!path) { toast('请填写路径', 'err'); return; }

  let body;
  if (bodyText) {
    try { JSON.parse(bodyText); } catch (e) {
      toast('请求体不是合法 JSON：' + e.message, 'err');
      return;
    }
    body = bodyText;
  }

  status('请求中…');
  const r = await api(method, path, body);

  $('rawStatus').textContent = method + ' ' + path + ' → HTTP ' + r.status +
    (r.ok ? ' （成功）' : ' （失败）');
  $('rawStatus').style.color = r.ok ? 'var(--ok)' : 'var(--err)';

  $('rawResult').textContent = r.body === null
    ? '（空响应体）'
    : (typeof r.body === 'string' ? r.body : JSON.stringify(r.body, null, 2));

  status(t('web.raw.ready'));
}

// ---------------------------------------------------------------------------
// 事件绑定
// ---------------------------------------------------------------------------

function initTabs() {
  const tabs = $('tabs');
  tabs.addEventListener('click', (e) => {
    const btn = e.target.closest('button[data-tab]');
    if (!btn) return;

    tabs.querySelectorAll('button').forEach((b) =>
      b.classList.toggle('active', b === btn));
    document.querySelectorAll('.tab-panel').forEach((p) =>
      p.classList.toggle('active', p.id === 'tab-' + btn.dataset.tab));

    // 切到某个标签时按需加载，避免开页面就打一堆请求。
    const tab = btn.dataset.tab;
    if (tab === 'tasks') loadTasks();
    if (tab === 'credentials') loadCredentials();
    if (tab === 'ip') loadIP();
    if (tab === 'overview') loadOverview();
    if (tab === 'proxy') loadProxy();
    if (tab === 'certs') loadCerts();
    if (tab === 'notify') loadNotify();
    if (tab === 'service') loadService();
  });
}

function initActions() {
  $('ipRefresh').onclick = loadIP;
  $('credRefresh').onclick = loadCredentials;
  $('credNew').onclick = renderCredForm;
  $('taskRefresh').onclick = loadTasks;
  $('taskNew').onclick = renderTaskForm;
  $('recLoad').onclick = loadRecords;
  $('recNew').onclick = () => renderRecordForm(null);
  $('recCred').onchange = loadZones;
  $('recZone').onchange = loadRecords;
  $('evConnect').onclick = connectEvents;
  $('evDisconnect').onclick = disconnectEvents;
  $('evClear').onclick = () => { state.events = []; $('evLog').innerHTML = ''; };
  $('rawSend').onclick = sendRaw;

  // 反向代理
  $('pxRefresh').onclick = loadProxy;
  $('pxStatus').onclick = loadProxyStatus;
  $('pxNew').onclick = () => renderProxyForm(null);
  $('pxSave').onclick = saveProxySettings;

  // 证书
  $('certRefresh').onclick = loadCerts;
  $('certRenew').onclick = renewCerts;

  // 通知
  $('ntRefresh').onclick = loadNotify;
  $('ntNew').onclick = () => renderNotifyForm(null);
  $('ntTest').onclick = testNotify;

  // 系统服务
  $('svcRefresh').onclick = loadService;
  $('svcStart').onclick = () => serviceAction('start');
  $('svcStop').onclick = () => serviceAction('stop');
  $('svcInstall').onclick = () => serviceAction('install');
  $('svcUninstall').onclick = () => {
    if (!confirm('确定卸载系统服务？数据目录会保留。')) return;
    serviceAction('uninstall');
  };

  // 委托绑定：表格里的按钮是动态渲染出来的，逐个绑定会漏掉重渲染后的元素。
  document.addEventListener('click', (e) => {
    const btn = e.target.closest('button');
    if (!btn) return;

    if (btn.dataset.verify) verifyCredential(btn.dataset.verify);

    if (btn.dataset.delcred) {
      if (confirm('确定删除凭据「' + btn.dataset.label + '」？')) {
        api('DELETE', '/v1/credentials/' + encodeURIComponent(btn.dataset.delcred))
          .then((r) => {
            if (!r.ok) { toast('删除失败：' + explain(r), 'err'); return; }
            toast('凭据已删除', 'ok');
            loadCredentials();
          });
      }
    }

    if (btn.dataset.runtask) runTask(btn.dataset.runtask);

    if (btn.dataset.deltask) {
      if (confirm('确定删除任务「' + btn.dataset.label + '」？')) {
        api('DELETE', '/v1/ddns-tasks/' + encodeURIComponent(btn.dataset.deltask))
          .then((r) => {
            if (!r.ok) { toast('删除失败：' + explain(r), 'err'); return; }
            toast('任务已删除', 'ok');
            loadTasks();
          });
      }
    }

    if (btn.dataset.editrec) {
      const rec = (state.records || []).find((x) => x.id === btn.dataset.editrec);
      if (rec) renderRecordForm(rec);
    }

    if (btn.dataset.delrec) {
      deleteRecord(btn.dataset.delrec, btn.dataset.name, btn.dataset.type);
    }

    // 新面板的按钮同样是动态渲染的，必须走委托绑定。
    if (btn.dataset.editpx) {
      const r = (state.proxyRoutes || []).find((x) => x.id === btn.dataset.editpx);
      if (r) renderProxyForm(r);
    }
    if (btn.dataset.delpx) deleteProxyRoute(btn.dataset.delpx);

    if (btn.dataset.editnt) {
      const c = (state.notifyChannels || []).find((x) => x.id === btn.dataset.editnt);
      if (c) renderNotifyForm(c);
    }
    if (btn.dataset.delnt) deleteNotifyChannel(btn.dataset.delnt);
  });
}

// ---------------------------------------------------------------------------

initTabs();
initActions();
boot();
