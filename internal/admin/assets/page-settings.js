const SETTINGS_FIELDS = [
  { k: 'parallel_pool_enabled', label: '并发请求池', type: 'bool', desc: '同时请求多个健康节点，首包到达即采纳，降低延迟' },
  { k: 'parallel_pool_size', label: '并发数', type: 'number', desc: '并发抢跑的节点数 (默认4)' },
  { k: 'max_retries', label: '上游重试次数', type: 'number', desc: '上游请求失败时的重试次数；总尝试 = 此值 + 1' },
  { k: 'max_n', label: 'n 多候选上限', type: 'number', desc: 'chat 的 n（一次返回多个候选）上限' },
  { k: 'anti429_target', label: 'anti429 注入位置', type: 'select', opts: ['system', 'user'], desc: '随机数注入到 system 指令、还是首条 user 消息前' },
  { k: 'anti429_enabled', label: 'anti429 随机数注入', type: 'bool', desc: '往请求注入随机数字串，削弱上游 429 / 缓存命中' },
  { k: 'force_no_stream', label: '强制非流式', type: 'bool', desc: '把所有流式请求降级为非流式' },
  { k: 'anti_tracking', label: '反追踪', type: 'bool', desc: '移除可能被用于追踪的请求特征' },
  { k: 'drop_max_tokens', label: '移除 maxOutputTokens', type: 'bool', desc: '移除输出 token 上限，让模型自由输出' },
  { k: 'debug_mode', label: 'Debug 日志', type: 'bool', desc: '开启更详细的错误与负载调试日志' },
];

// 遥测设置单独显示（更显眼），不混在普通设置里。
const TELEMETRY_INFO = `
<div class="card glass" style="margin-bottom:14px;padding:18px;border-left:3px solid #3b82f6">
  <div style="display:flex;align-items:center;justify-content:space-between;gap:14px;flex-wrap:wrap">
    <div style="flex:1;min-width:240px">
      <div style="font-size:1rem;font-weight:600;margin-bottom:6px">📡 匿名遥测</div>
      <div class="desc" style="line-height:1.6">
        本软件会向作者发送匿名心跳，<b>仅含运行环境信息</b>（版本、平台、CPU、内存、时区等），
        用于了解版本分布和活跃情况。
        <b>不收集</b>任何 IP、API 密钥、对话内容、文件路径等敏感数据。
        服务端被动接收，不会向你的程序下发任何指令。
        <a href="https://stat.baimeow.icu" target="_blank" style="color:#60a5fa">查看公开统计 →</a>
      </div>
    </div>
    <label class="toggle" style="flex-shrink:0">
      <input type="checkbox" id="set_telemetry_enabled">
      <span class="track"></span>
    </label>
  </div>
</div>`;

let curSettings = {};
async function loadSettings() {
  const d = await API.settings.get(); curSettings = d.settings || d;
  const fld = (f) => {
    const v = curSettings[f.k];
    if (f.type === 'bool') return `<div class="field bool"><div class="min-w-0"><label for="set_${f.k}">${f.label}</label>${f.desc?`<div class="desc mt-4px">${f.desc}</div>`:''}</div><label class="toggle"><input type="checkbox" id="set_${f.k}" ${v?'checked':''}><span class="track"></span></label></div>`;
    let input;
    if (f.type === 'select') input = `<select id="set_${f.k}">${f.opts.map(o => `<option ${o===v?'selected':''}>${o}</option>`).join('')}</select>`;
    else input = `<input type="${f.type}" id="set_${f.k}" value="${v ?? ''}">`;
    return `<div class="field"><label for="set_${f.k}">${f.label}</label>${input}${f.desc?`<div class="desc">${f.desc}</div>`:''}</div>`;
  };
  const nums = SETTINGS_FIELDS.filter(f => f.type !== 'bool'), bools = SETTINGS_FIELDS.filter(f => f.type === 'bool');
  $('#settingsForm').innerHTML =
    TELEMETRY_INFO +
    '<div class="grid grid-2">' + nums.map(fld).join('') + '</div>' +
    '<div class="h-6px"></div><div class="grid grid-2">' + bools.map(fld).join('') + '</div>' +
    '<button class="btn mt-14px" onclick="saveSettings()">保存设置</button>';
  // 遥测开关默认开启（telemetry_enabled 未设置时为 null，按 true 处理）
  const telEnabled = curSettings.telemetry_enabled !== false;
  $('#set_telemetry_enabled').checked = telEnabled;
  PAGE_CACHE['settings'] = $('#page-settings').innerHTML;
}
async function saveSettings() {
  const out = {};
  for (const f of SETTINGS_FIELDS) { const el = $('#set_' + f.k); if (!el) continue; if (f.type === 'bool') out[f.k] = el.checked; else if (f.type === 'number') out[f.k] = parseInt(el.value || '0', 10); else out[f.k] = el.value; }
  // 遥测开关
  const telEl = $('#set_telemetry_enabled');
  if (telEl) out['telemetry_enabled'] = telEl.checked;
  await API.settings.put(out); toast('设置已保存');
}
