// ISC 控制台的前端本地化。
//
// # 为什么消息表整份取过来，而不是逐条内联
//
// 控制台的句子被 <strong> / <code> 切开。逐文本节点抽取会得到不可翻译的
// 碎片 —— 试过一次：抽出 128 条，大半是"前缀，该前缀下的"这种，译者拿到
// 手里完全不知道在说什么。
//
// 因此服务端把**整句含内联标签**作为一条消息（GET /console/i18n.json），
// 这里对 [data-i18n-html] 用 innerHTML 注入。
//
// # 安全前提
//
// 这个文件里的两次 innerHTML 都只吃**消息表里的值**，而那张表来自
// internal/i18n/console_{zh,en}.go —— 纯静态文案，绝无用户输入。
// 一旦有人往那张表里塞进用户可控的内容，这里就成了 XSS 入口。
// 动态数据一律走 textContent（见 app.js 的 esc() 与既有渲染代码）。

(function () {
  'use strict';

  var messages = {};
  var readyResolve;
  var ready = new Promise(function (resolve) { readyResolve = resolve; });

  // t 取一条消息。表还没到或 key 不存在时返回 fallback。
  //
  // 返回 fallback 而不是 key：控制台的中文原文就写在调用处，因此
  // 表没到的时候页面仍然是完整的（只是没翻译），而不是满屏的 key。
  function t(key, fallback) {
    if (Object.prototype.hasOwnProperty.call(messages, key)) {
      return messages[key];
    }
    return fallback !== undefined ? fallback : key;
  }

  // applyI18n 把静态 DOM 里的标记替换掉。
  //
  //   data-i18n        纯文本 → textContent
  //   data-i18n-html   含内联标签 → innerHTML
  //   data-i18n-attr   属性   → 形如 "title:console.x,placeholder:console.y"
  //
  // 分开两个属性而不是一个：用错方向的代价不对称 —— 把含标签的句子塞进
  // textContent 只是显示成字面量（能看出来），而把纯文本塞进 innerHTML
  // 会让其中的 < > 被当成标签（看不出来）。
  function applyI18n(root) {
    var scope = root || document;

    scope.querySelectorAll('[data-i18n]').forEach(function (el) {
      el.textContent = t(el.getAttribute('data-i18n'), el.textContent);
    });

    scope.querySelectorAll('[data-i18n-html]').forEach(function (el) {
      el.innerHTML = t(el.getAttribute('data-i18n-html'), el.innerHTML);
    });

    scope.querySelectorAll('[data-i18n-attr]').forEach(function (el) {
      el.getAttribute('data-i18n-attr').split(',').forEach(function (pair) {
        var i = pair.indexOf(':');
        if (i < 0) { return; }
        var attr = pair.slice(0, i).trim();
        var key = pair.slice(i + 1).trim();
        if (attr && key) {
          el.setAttribute(attr, t(key, el.getAttribute(attr)));
        }
      });
    });
  }

  function load() {
    fetch('/console/i18n.json', { cache: 'no-store' })
      .then(function (res) {
        if (!res.ok) { throw new Error('HTTP ' + res.status); }
        return res.json();
      })
      .then(function (table) {
        messages = table || {};
        // 让 <html lang> 也跟着走 —— 它决定浏览器用哪种字体与断行规则。
        var tag = messages['web.lang_tag'];
        if (tag) { document.documentElement.lang = tag; }
        applyI18n(document);
      })
      .catch(function (err) {
        // 取不到就保持中文原文。**不弹错**：本地化是旁路，
        // 它失败了不该让控制台看起来像坏了。
        if (window.console) {
          console.warn('控制台消息表加载失败，界面保持原文：', err);
        }
      })
      .then(readyResolve);
  }

  window.iscI18n = {
    t: t,
    apply: applyI18n,
    ready: ready
  };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', load);
  } else {
    load();
  }
})();
