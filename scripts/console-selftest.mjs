// 用 CDP 驱动无头 Chrome 检查控制台侧栏按钮：逐个点击并断言切页生效。
const CDP_PORT = process.env.CDP_PORT || '9223';
const URL = process.env.PAGE_URL || 'http://127.0.0.1:10195/';

async function waitFor(check, timeout = 10000, interval = 100) {
  const t0 = Date.now();
  for (;;) {
    const v = await check();
    if (v) return v;
    if (Date.now() - t0 > timeout) throw new Error('timeout');
    await new Promise((r) => setTimeout(r, interval));
  }
}

class CDP {
  constructor(ws) { this.ws = ws; this.id = 0; this.pending = new Map(); this.events = []; }
  static async connect(wsUrl) {
    const ws = new WebSocket(wsUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    const c = new CDP(ws);
    ws.onmessage = (ev) => {
      let msg;
      try {
        msg = JSON.parse(ev.data);
      } catch {
        return; // CDP 偶发会发送非 JSON 帧，忽略即可
      }
      if (msg.id && c.pending.has(msg.id)) {
        const { resolve, reject } = c.pending.get(msg.id);
        c.pending.delete(msg.id);
        msg.error ? reject(new Error(JSON.stringify(msg.error))) : resolve(msg.result);
      } else if (msg.method) {
        c.events.push(msg);
      }
    };
    return c;
  }
  send(method, params = {}) {
    const id = ++this.id;
    this.ws.send(JSON.stringify({ id, method, params }));
    return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }));
  }
  async eval(expr) {
    const r = await this.send('Runtime.evaluate', {
      expression: expr, returnByValue: true, awaitPromise: true,
    });
    if (r.exceptionDetails) throw new Error('页面异常: ' + JSON.stringify(r.exceptionDetails));
    return r.result.value;
  }
}

(async () => {
  // 找到页面 target
  const targets = await (await fetch(`http://127.0.0.1:${CDP_PORT}/json/list`)).json();
  const page = targets.find((t) => t.type === 'page');
  if (!page) throw new Error('没有可用的页面 target');

  const cdp = await CDP.connect(page.webSocketDebuggerUrl);
  await cdp.send('Page.enable');
  await cdp.send('Runtime.enable');
  await cdp.send('Log.enable');

  await cdp.send('Page.navigate', { url: URL });
  await waitFor(async () => (await cdp.eval('document.readyState')) === 'complete');
  await new Promise((r) => setTimeout(r, 800));

  const tabs = await cdp.eval(`Array.from(document.querySelectorAll('#nav button')).map(b => b.dataset.tab)`);
  const results = [];

  for (const tab of tabs) {
    // 通过真实点击（而非直接调用 showTab）验证事件绑定
    await cdp.eval(`document.querySelector('#nav button[data-tab="${tab}"]').click()`);
    await new Promise((r) => setTimeout(r, 250));
    const state = await cdp.eval(`(function(){
      var active = document.querySelector('.tab.on');
      var btn = document.querySelector('#nav button.on');
      var panels = Array.from(document.querySelectorAll('.tab')).filter(function(s){ return s.offsetParent !== null; });
      return {
        activePanel: active && active.id,
        activeBtn: btn && btn.dataset.tab,
        visiblePanels: panels.map(function(s){ return s.id; }),
        pendingBadge: (document.querySelector('[data-pending-checkin]')||{}).hidden,
      };
    })()`);
    results.push({ clicked: tab, ...state });
  }

  // 点击角标本身（span），验证事件冒泡仍能切页
  await cdp.eval(`document.querySelector('#nav button[data-tab="overview"]').click()`);
  await new Promise((r) => setTimeout(r, 200));
  const badgeClick = await cdp.eval(`(function(){
    var span = document.querySelector('[data-pending-checkin]');
    var before = document.querySelector('.tab.on').id;
    if (span) span.click(); else document.querySelector('#nav button[data-tab="accounts"]').click();
    return { before: before, after: document.querySelector('.tab.on').id };
  })()`);
  await new Promise((r) => setTimeout(r, 250));
  const afterBadge = await cdp.eval(`document.querySelector('.tab.on').id`);

  // 收集控制台错误
  const errors = cdp.events
    .filter((e) => e.method === 'Runtime.exceptionThrown' ||
      (e.method === 'Log.entryAdded' && e.params.entry.level === 'error') ||
      (e.method === 'Runtime.consoleAPICalled' && e.params.type === 'error'))
    .map((e) => {
      if (e.method === 'Runtime.exceptionThrown') return 'exception: ' + (e.params.exceptionDetails.text || '') + ' ' + (e.params.exceptionDetails.exception?.description || '');
      if (e.method === 'Log.entryAdded') return 'log: ' + e.params.entry.text;
      return 'console: ' + (e.params.args || []).map((a) => a.value || a.description || '').join(' ');
    });

  console.log('=== 逐个点击结果 ===');
  let bad = 0;
  for (const r of results) {
    const ok = r.activePanel === 'tab-' + r.clicked && r.activeBtn === r.clicked &&
      r.visiblePanels.length === 1 && r.visiblePanels[0] === 'tab-' + r.clicked;
    if (!ok) bad++;
    console.log(`  ${ok ? 'OK  ' : 'FAIL'}  click=${r.clicked.padEnd(14)} panel=${String(r.activePanel).padEnd(20)} btn=${String(r.activeBtn).padEnd(14)} visible=${JSON.stringify(r.visiblePanels)}`);
  }
  console.log('=== 点击角标（span 冒泡） ===');
  console.log('  before=' + badgeClick.before + ' after=' + afterBadge);
  console.log('=== 控制台错误 ===');
  console.log(errors.length ? errors.map((e) => '  ' + e).join('\n') : '  无');
  console.log(`\n汇总: ${tabs.length} 个按钮，失败 ${bad} 个`);
  process.exit(bad ? 1 : 0);
})().catch((e) => { console.error('DRIVER ERROR:', e.message); process.exit(2); });
