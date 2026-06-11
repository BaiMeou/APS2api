import { esc, showToast, maskKey, toggleReveal, formatNodeTestMeta, formatImportSummary, updateCountsFromNodes, formatRecentTest, setTestProgress } from '../utils.js';

let _api, _state;

const _tmap = {
  A: atob('VkxFU1M='), B: atob('Vk1lc3M='), C: atob('VHJvamFu'),
  D: atob('U1M='), E: atob('U1NS'), F: atob('SHlzdGVyaWEy'),
  H: atob('QW55VExT'), I: atob('VFVJQw=='), J: atob('SHkx'),
};

export function initProxy(api, state) {
  _api = api;
  _state = state;

  document.querySelector('[data-action="add-subscription"]').addEventListener('click', addSubscription);
  document.querySelector('[data-action="select-all-subs"]').addEventListener('click', () => selectAllSubscriptions(true));
  document.querySelector('[data-action="clear-all-subs"]').addEventListener('click', () => selectAllSubscriptions(false));
  document.querySelector('[data-action="enable-selected-subs"]').addEventListener('click', () => setSelectedSubscriptionsEnabled(true));
  document.querySelector('[data-action="disable-selected-subs"]').addEventListener('click', () => setSelectedSubscriptionsEnabled(false));
  document.querySelector('[data-action="fetch-selected"]').addEventListener('click', () => fetchSubscriptions('selected'));
  document.querySelector('[data-action="fetch-enabled"]').addEventListener('click', () => fetchSubscriptions('enabled'));
  document.querySelector('[data-action="fetch-all"]').addEventListener('click', () => fetchSubscriptions('all'));
  document.querySelector('[data-action="import-append"]').addEventListener('click', () => importNodeFile('append'));
  document.querySelector('[data-action="import-replace"]').addEventListener('click', () => importNodeFile('replace'));
  document.querySelector('[data-action="check-core"]').addEventListener('click', () => checkCore());
  document.querySelector('[data-action="prepare-core"]').addEventListener('click', prepareCore);
  document.querySelector('[data-action="test-all"]').addEventListener('click', testAllNodes);
  document.querySelector('[data-action="deduplicate"]').addEventListener('click', deduplicateNodes);
  document.querySelector('[data-action="delete-disabled"]').addEventListener('click', deleteDisabledNodes);
  document.querySelector('[data-action="save-proxy"]').addEventListener('click', saveProxy);
  const sidebarParallelTog = document.getElementById('sidebarParallelTog');
  if (sidebarParallelTog) {
    sidebarParallelTog.addEventListener('change', async (e) => {
      const enabled = e.target.checked;
      await _api.put('/api/admin/settings', { parallel_pool_enabled: enabled });
      await loadProxyStatus();
      const nodes = _state.get('allNodes') || [];
      renderNodes(nodes);
    });
  }

  const subList = document.getElementById('subList');
  subList.addEventListener('change', (e) => {
    const cb = e.target.closest('.sub-cb');
    if (cb) {
      toggleSubSelect(cb);
      return;
    }
    const tog = e.target.closest('.mini-toggle input[type="checkbox"]');
    if (tog) {
      const id = tog.closest('.sub-item').querySelector('.sub-cb').dataset.id;
      toggleSubscriptionEnabled(id, tog.checked);
    }
  });

  subList.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    if (btn.dataset.action === 'edit-sub') {
      editSubscription(btn.dataset.id);
    } else if (btn.dataset.action === 'delete-sub') {
      deleteSubscription(btn.dataset.id);
    }
  });

  document.getElementById('nodeList').addEventListener('click', async (e) => {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    const action = btn.dataset.action;
    if (action === 'test-node') {
      await testNode(btn);
    } else if (action === 'enable-node') {
      await enableNode(btn);
    } else if (action === 'use-node') {
      await useNode(btn);
    } else if (action === 'delete-node') {
      await deleteNode(btn);
    }
  });
}

export async function loadSubscriptions(opts = {}) {
  try {
    const r = await _api.get('/api/admin/subscriptions');
    const subs = r.subscriptions || [];
    const selected = _state.get('selectedSubIds') || new Set();
    const cleaned = new Set([...selected].filter(id => subs.some(s => s.id === id)));
    if (!cleaned.size) subs.filter(s => s.enabled).forEach(s => cleaned.add(s.id));
    _state.set('subscriptions', subs);
    _state.set('selectedSubIds', cleaned);
    renderSubscriptions();
    if (subs.some(s => s.enabled) && !opts.skipAutoFetch) {
      fetchSubscriptions('enabled', { silent: true }).catch(() => {});
    }
  } catch (e) {}
}

async function loadSubscriptionsOnly() {
  const r = await _api.get('/api/admin/subscriptions');
  const subs = r.subscriptions || [];
  const selected = _state.get('selectedSubIds') || new Set();
  const cleaned = new Set([...selected].filter(id => subs.some(s => s.id === id)));
  _state.set('subscriptions', subs);
  _state.set('selectedSubIds', cleaned);
  renderSubscriptions();
}

function renderSubscriptions() {
  const subs = _state.get('subscriptions') || [];
  const selectedSubIds = _state.get('selectedSubIds') || new Set();
  const listEl = document.getElementById('subList');
  const bar = document.getElementById('subSelectBar');
  listEl.innerHTML = '';
  if (!subs.length) {
    listEl.innerHTML = '<div class="empty">还没有订阅。添加订阅后可单选、多选或全选拉取节点。</div>';
    bar.style.display = 'none';
    refreshSubscriptionSelection();
    return;
  }
  bar.style.display = 'flex';
  subs.forEach(s => {
    const checked = selectedSubIds.has(s.id);
    const updated = s.updated_at ? new Date(s.updated_at * 1000).toLocaleString() : '未拉取';
    const div = document.createElement('div');
    div.className = 'sub-item' + (s.enabled ? '' : ' disabled');
    div.innerHTML = `
      <input type="checkbox" class="sub-cb" data-id="${esc(s.id)}" ${checked ? 'checked' : ''} title="选择此订阅参与本次拉取">
      <div class="sub-main">
        <div class="sub-name">${esc(s.name || '订阅')}</div>
        <div class="sub-url">${esc(s.url)}</div>
        <div class="sub-meta">nodes ${esc(s.node_count || 0)} · ${esc(updated)}</div>
      </div>
      <div class="sub-actions">
        <label class="mini-toggle"><input type="checkbox" ${s.enabled ? 'checked' : ''}> 启用</label>
        <button class="btn btn-sm" data-action="edit-sub" data-id="${esc(s.id)}">编辑</button>
        <button class="btn btn-sm btn-danger" data-action="delete-sub" data-id="${esc(s.id)}">删除</button>
      </div>`;
    listEl.appendChild(div);
  });
  refreshSubscriptionSelection();
}

function refreshSubscriptionSelection() {
  const selectedSubIds = _state.get('selectedSubIds') || new Set();
  const count = selectedSubIds.size;
  const btn = document.getElementById('fetchBtn');
  const countEl = document.getElementById('subSelectCount');
  if (btn) {
    btn.textContent = `拉取选中 (${count})`;
    btn.disabled = count < 1;
  }
  if (countEl) countEl.textContent = count ? `已选择 ${count} 个订阅` : '选择订阅后拉取节点';
}

function toggleSubSelect(cb) {
  const selectedSubIds = _state.get('selectedSubIds') || new Set();
  const id = cb.dataset.id;
  if (cb.checked) selectedSubIds.add(id);
  else selectedSubIds.delete(id);
  _state.set('selectedSubIds', selectedSubIds);
  refreshSubscriptionSelection();
}

function selectAllSubscriptions(checked) {
  const subs = _state.get('subscriptions') || [];
  const selectedSubIds = new Set();
  if (checked) subs.forEach(s => selectedSubIds.add(s.id));
  _state.set('selectedSubIds', selectedSubIds);
  renderSubscriptions();
}

async function setSelectedSubscriptionsEnabled(enabled) {
  const selectedSubIds = _state.get('selectedSubIds') || new Set();
  if (!selectedSubIds.size) { showToast('请至少选择一个订阅', 'err'); return; }
  try {
    await Promise.all([...selectedSubIds].map(id => _api.put('/api/admin/subscriptions/' + encodeURIComponent(id), { enabled })));
    await loadSubscriptionsOnly();
    showToast(enabled ? '已批量启用订阅' : '已批量停用订阅', 'ok');
  } catch (e) { showToast(e.message, 'err'); await loadSubscriptionsOnly(); }
}

async function addSubscription() {
  const name = document.getElementById('sub_name').value.trim();
  const url = document.getElementById('sub_url').value.trim();
  if (!url) { showToast('请输入订阅地址', 'err'); return; }
  try {
    const r = await _api.post('/api/admin/subscriptions', { name, url, enabled: true });
    const selectedSubIds = _state.get('selectedSubIds') || new Set();
    selectedSubIds.add(r.subscription.id);
    _state.set('selectedSubIds', selectedSubIds);
    document.getElementById('sub_name').value = '';
    document.getElementById('sub_url').value = '';
    await loadSubscriptionsOnly();
    showToast('订阅已添加', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}

async function toggleSubscriptionEnabled(id, enabled) {
  try {
    await _api.put('/api/admin/subscriptions/' + encodeURIComponent(id), { enabled });
    await loadSubscriptionsOnly();
    showToast(enabled ? '订阅已启用' : '订阅已停用', 'ok');
  } catch (e) { showToast(e.message, 'err'); await loadSubscriptionsOnly(); }
}

async function editSubscription(id) {
  const subs = _state.get('subscriptions') || [];
  const sub = subs.find(s => s.id === id);
  if (!sub) return;
  const name = prompt('订阅名称', sub.name || '');
  if (name === null) return;
  const url = prompt('订阅地址', sub.url || '');
  if (url === null) return;
  try {
    await _api.put('/api/admin/subscriptions/' + encodeURIComponent(id), { name, url });
    await loadSubscriptionsOnly();
    showToast('订阅已更新', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}

async function deleteSubscription(id) {
  const subs = _state.get('subscriptions') || [];
  const sub = subs.find(s => s.id === id);
  try {
    await _api.delete('/api/admin/subscriptions/' + encodeURIComponent(id));
    const selectedSubIds = _state.get('selectedSubIds') || new Set();
    selectedSubIds.delete(id);
    _state.set('selectedSubIds', selectedSubIds);
    await loadSubscriptionsOnly();
    showToast('订阅已删除', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}

async function fetchSubscription(opts = {}) {
  const url = document.getElementById('sub_url').value.trim();
  if (url) {
    try {
      const r = await _api.post('/api/admin/subscriptions', { url, enabled: true });
      const selectedSubIds = new Set([r.subscription.id]);
      _state.set('selectedSubIds', selectedSubIds);
      await loadSubscriptionsOnly();
      return fetchSubscriptions('selected', opts);
    } catch (e) { if (!opts.silent) showToast(e.message, 'err'); return; }
  }
  return fetchSubscriptions('selected', opts);
}

async function fetchSubscriptions(mode = 'selected', opts = {}) {
  const selectedSubIds = _state.get('selectedSubIds') || new Set();
  if (mode === 'selected' && !selectedSubIds.size) { if (!opts.silent) showToast('请至少选择一个订阅', 'err'); return; }
  const btn = document.getElementById('fetchBtn');
  const listEl = document.getElementById('nodeList');
  if (btn) { btn.disabled = true; btn.textContent = 'Fetching...'; }
  listEl.innerHTML = '';
  document.getElementById('subSummary').textContent = '';
  try {
    const data = await _api.post('/api/admin/subscriptions/fetch', { mode, ids: [...selectedSubIds] });
    const warn = data.errors && data.errors.length ? `，${data.errors.length} 个订阅失败` : '';
    document.getElementById('subSummary').textContent = `共 ${data.total} 个节点 · ${data.subscription_count} 个订阅${warn}`;
    const nodes = data.nodes || [];
    _state.set('allNodes', nodes);
    const counts = updateCountsFromNodes(nodes);
    _state.set('nodeCounts', counts);
    if (!nodes.length) { listEl.innerHTML = '<div class="empty">没有解析到任何节点</div>'; return; }
    document.getElementById('poolBar').style.display = 'flex';
    renderNodes(nodes);
    updatePoolUI();
    await loadSubscriptionsOnly();
  } catch (e) {
    if (!opts.silent) showToast(e.message, 'err');
    if (!opts.silent) listEl.innerHTML = '<div class="empty">' + esc(e.message) + '</div>';
  } finally { if (btn) { btn.disabled = false; refreshSubscriptionSelection(); } }
}

export async function loadUnifiedNodes() {
  try {
    const r = await _api.get('/api/admin/nodes');
    const nodes = r.nodes || [];
    _state.set('allNodes', nodes);
    _state.set('nodeHealth', r.health || {});
    const counts = { total: r.total || nodes.length, enabled_count: r.enabled_count || 0, disabled_count: r.disabled_count || 0 };
    _state.set('nodeCounts', counts);
    renderNodes(nodes);
    updatePoolUI();
  } catch (e) {}
}

function updatePoolUI() {
  const topBadge = document.getElementById('poolBadgeTop');
  const topBadgeText = document.getElementById('poolBadgeText');
  const poolBar = document.getElementById('poolBar');
  const nodes = _state.get('allNodes') || [];
  const counts = _state.get('nodeCounts') || { total: 0, enabled_count: 0, disabled_count: 0 };
  const enabled = counts.enabled_count ?? nodes.filter(n => !n.disabled).length;
  const disabled = counts.disabled_count ?? nodes.filter(n => n.disabled).length;
  const total = counts.total ?? nodes.length;
  if (poolBar) poolBar.style.display = total > 0 ? 'flex' : 'none';
  if (total > 0) {
    const text = `节点 ${enabled}/${total}`;
    if (topBadge) topBadge.style.display = 'flex';
    if (topBadgeText) topBadgeText.textContent = text;
  } else {
    if (topBadge) topBadge.style.display = 'none';
  }
  refreshPoolButton();
}

function refreshPoolButton() {
  const countEl = document.getElementById('poolCount');
  const nodes = _state.get('allNodes') || [];
  const counts = _state.get('nodeCounts') || { total: 0, enabled_count: 0, disabled_count: 0 };
  const total = counts.total ?? nodes.length;
  const enabled = counts.enabled_count ?? nodes.filter(n => !n.disabled).length;
  const disabled = counts.disabled_count ?? nodes.filter(n => n.disabled).length;
  if (countEl) countEl.textContent = total ? `可用 ${enabled} / 禁用 ${disabled} / 总计 ${total}` : '订阅拉取后自动保存为候选节点';
}

function renderNodes(nodes) {
  const activeUri = _state.get('activeUri') || '';
  const health = _state.get('nodeHealth') || {};
  const listEl = document.getElementById('nodeList');
  listEl.innerHTML = '';
  if (!nodes.length) {
    listEl.innerHTML = '<div class="empty">还没有候选节点。拉取订阅或导入文件后会显示在这里。</div>';
    return;
  }
  nodes.forEach((n, i) => {
    const isActive = activeUri && activeUri === n.raw_uri;
    const displayType = _tmap[n.type] || n.type;
    const isDisabled = !!n.disabled;
    const testMeta = formatNodeTestMeta(n, health);

    const div = document.createElement('div');
    div.className = 'node' + (isActive ? ' active' : '') + (isDisabled ? ' disabled' : '');
    div.innerHTML = `
      <div class="node-info">
        <div class="node-name">${esc(n.name || '未命名')}</div>
        <div class="node-meta"><span class="node-type">${esc(displayType)}</span><span>${esc(n.server)}:${n.port}</span>${n.subscription_name ? `<span>${esc(n.subscription_name)}</span>` : ''}${testMeta}</div>
      </div>
      <div class="node-badges">
        ${isActive ? '<span class="nbadge nbadge-active">当前</span>' : ''}
        ${isDisabled ? '<span class="nbadge nbadge-disabled">已禁用</span>' : '<span class="nbadge nbadge-pool">候选</span>'}
      </div>
      <button class="btn btn-sm" data-action="test-node" data-uri="${esc(n.raw_uri)}" data-name="${esc(n.name || '')}">测试</button>
      ${isDisabled ? `<button class="btn btn-sm btn-primary" data-action="enable-node" data-uri="${esc(n.raw_uri)}">启用</button>` : ''}
      <button class="btn btn-sm ${isActive ? '' : 'btn-primary'}" data-action="use-node" data-uri="${esc(n.raw_uri)}" data-name="${esc(n.name || '')}">
        ${isActive ? '重新应用' : '使用'}
      </button>
      <button class="btn btn-sm btn-danger" data-action="delete-node" data-uri="${esc(n.raw_uri)}" data-name="${esc(n.name || '')}">删除</button>`;
    listEl.appendChild(div);
  });
}

async function useNode(btn) {
  const uri = btn.dataset.uri;
  const name = btn.dataset.name || '';
  const idleText = btn.textContent;
  btn.disabled = true;
  btn.textContent = '应用中...';
  try {
    const r = await _api.post('/api/admin/use-node', { raw_uri: uri, name });
    showToast('已启用: ' + (name || r.proxy_url), 'ok');
    _state.set('activeUri', uri);
    await loadProxyStatus();
    const nodes = _state.get('allNodes') || [];
    renderNodes(nodes);
  } catch (e) { showToast(e.message, 'err'); btn.disabled = false; btn.textContent = idleText || '使用'; }
}

async function deleteNode(btn) {
  const uri = btn.dataset.uri || '';
  const name = btn.dataset.name || uri.slice(0, 60);
  btn.disabled = true;
  btn.textContent = '删除中...';
  try {
    const r = await _api.delete('/api/admin/nodes', { raw_uri: uri });
    const nodes = _state.get('allNodes') || [];
    _state.set('allNodes', nodes.filter(n => n.raw_uri !== uri));
    document.getElementById('subSummary').textContent = `已删除 1 个节点 · 当前 ${r.total} 个候选节点`;
    await loadUnifiedNodes();
    if (r.active_cleared) await loadProxyStatus();
    showToast('节点已删除', 'ok');
  } catch (e) {
    showToast(e.message, 'err');
    btn.disabled = false;
    btn.textContent = '删除';
  }
}

async function testNode(btn) {
  const uri = btn.dataset.uri || '';
  btn.disabled = true;
  btn.textContent = '测试中...';
  try {
    const r = await _api.post('/api/admin/nodes/test', { raw_uri: uri, auto_disable: true, timeout_seconds: 25 });
    await loadUnifiedNodes();
    showToast(r.ok ? `测试通过 ${Math.round(r.elapsed_ms)}ms` : `测试失败: ${r.error}`, r.ok ? 'ok' : 'err');
  } catch (e) {
    showToast(e.message, 'err');
    btn.disabled = false;
    btn.textContent = '测试';
  }
}

async function testAllNodes() {
  const nodes = _state.get('allNodes') || [];
  if (!nodes.length) { showToast('没有候选节点', 'err'); return; }
  const total = nodes.length;
  const concurrency = Math.min(4, total);
  const btn = document.getElementById('testAllBtn');
  const deleteBtn = document.getElementById('deleteDisabledBtn');
  const summary = document.getElementById('subSummary');
  let next = 0;
  let done = 0;
  let ok = 0;
  let failed = 0;
  let disabled = 0;
  btn.disabled = true;
  btn.textContent = '测试中...';
  if (deleteBtn) deleteBtn.disabled = true;
  if (summary) summary.textContent = `正在测试 ${total} 个节点，并发 ${concurrency}`;
  setTestProgress(true, `已完成 0/${total} · 通过 0 · 失败 0 · 禁用 0 · 并发 ${concurrency}`, '准备测试...', 0);
  try {
    async function workerLoop() {
      while (next < total) {
        const node = nodes[next++];
        const result = await _api.post('/api/admin/nodes/test', { raw_uri: node.raw_uri, auto_disable: true, timeout_seconds: 25 })
          .catch(e => ({ ok: false, error: e.message, disabled: true }));
        done++;
        if (result.ok) ok++;
        else failed++;
        if (result.disabled) disabled++;
        const text = `已完成 ${done}/${total} · 通过 ${ok} · 失败 ${failed} · 禁用 ${disabled} · 并发 ${concurrency}`;
        setTestProgress(true, text, formatRecentTest(node, result), done / total);
        if (summary) summary.textContent = text;
      }
    }
    await Promise.all(Array.from({ length: concurrency }, workerLoop));
    await loadUnifiedNodes();
    showToast(`批量测试完成：通过 ${ok} / 失败 ${failed}`, failed ? 'err' : 'ok');
  } catch (e) {
    if (summary) summary.textContent = e.message;
    showToast(e.message, 'err');
  } finally {
    btn.disabled = false;
    btn.textContent = '批量测试';
    if (deleteBtn) deleteBtn.disabled = false;
  }
}

async function enableNode(btn) {
  const uri = btn.dataset.uri || '';
  btn.disabled = true;
  btn.textContent = '启用中...';
  try {
    await _api.post('/api/admin/nodes/enable', { raw_uri: uri });
    await loadUnifiedNodes();
    showToast('节点已启用', 'ok');
  } catch (e) {
    showToast(e.message, 'err');
    btn.disabled = false;
    btn.textContent = '启用';
  }
}

async function deleteDisabledNodes() {
  const counts = _state.get('nodeCounts') || {};
  const nodes = _state.get('allNodes') || [];
  const disabled = counts.disabled_count ?? nodes.filter(n => n.disabled).length;
  if (!disabled) { showToast('没有已禁用节点', 'ok'); return; }
  const btn = document.getElementById('deleteDisabledBtn');
  btn.disabled = true;
  btn.textContent = '删除中...';
  try {
    const r = await _api.delete('/api/admin/nodes/disabled');
    document.getElementById('subSummary').textContent = `已删除禁用节点 ${r.deleted_count} 个 · 当前 ${r.total} 个候选节点`;
    await loadUnifiedNodes();
    if (r.active_cleared) await loadProxyStatus();
    showToast('禁用节点已删除', 'ok');
  } catch (e) {
    showToast(e.message, 'err');
  } finally {
    btn.disabled = false;
    btn.textContent = '删除禁用节点';
  }
}

async function deduplicateNodes() {
  const btn = document.getElementById('dedupBtn');
  btn.disabled = true;
  btn.textContent = '去重中...';
  try {
    const r = await _api.post('/api/admin/nodes/deduplicate');
    document.getElementById('subSummary').textContent = `去重完成，移除 ${r.removed_count} 个重复节点 · 当前 ${r.total} 个节点`;
    await loadUnifiedNodes();
    showToast(`去重完成，移除 ${r.removed_count} 个重复节点`, 'ok');
  } catch (e) {
    showToast(e.message, 'err');
  } finally {
    btn.disabled = false;
    btn.textContent = '一键去重';
  }
}

export async function checkCore(opts = {}) {
  try {
    const s = await _api.get('/api/admin/worker-core');
    const el = document.getElementById('coreSummary');
    if (el) {
      el.textContent = s.status_msg || (s.binary_available ? `已就绪 · ${s.platform} · ${s.binary_path}` : `未就绪 · ${s.platform} · ${s.bin_dir}`);
    }
    const badge = document.getElementById('coreBadge');
    if (badge) badge.style.display = s.binary_available ? 'none' : 'inline-block';
    if (!opts.silent) showToast(s.status_msg || (s.binary_available ? '内核已就绪' : '内核未就绪'), 'ok');
    return s;
  } catch (e) { if (!opts.silent) showToast(e.message, 'err'); }
}

async function prepareCore() {
  const el = document.getElementById('coreSummary');
  const btn = document.getElementById('prepareCoreBtn');
  if (el) el.textContent = '正在下载内核，请勿重复点击';
  if (btn) { btn.disabled = true; btn.textContent = '下载中...'; }
  try {
    const s = await _api.post('/api/admin/worker-core/prepare');
    if (el) el.textContent = `已就绪 · ${s.platform} · ${s.binary_path}`;
    await loadProxyStatus();
    showToast('内核已就绪', 'ok');
  } catch (e) {
    if (el) el.textContent = e.message;
    showToast(e.message, 'err');
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = '下载内核'; }
  }
}

async function importNodeFile(mode = 'append') {
  const input = document.getElementById('nodeImportFile');
  const file = input && input.files && input.files[0];
  if (!file) { showToast('请选择配置文件', 'err'); return; }
  const fd = new FormData();
  fd.append('file', file);
  try {
    const r = await _api.upload('/api/admin/nodes/import?mode=' + encodeURIComponent(mode), fd);
    const nodes = r.nodes || [];
    _state.set('allNodes', nodes);
    const counts = updateCountsFromNodes(nodes);
    _state.set('nodeCounts', counts);
    const summary = formatImportSummary(r);
    document.getElementById('subSummary').textContent = summary;
    document.getElementById('poolBar').style.display = 'flex';
    renderNodes(nodes);
    updatePoolUI();
    if (r.active_cleared) await loadProxyStatus();
    input.value = '';
    showToast(summary, 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}

export async function loadProxyStatus() {
  try {
    const s = await _api.get('/api/admin/proxy-status');
    const cfg = await _api.get('/api/admin/settings');
    _state.set('activeUri', s.active_node_uri || '');
    _state.set('proxyMode', s.proxy_mode || 'none');
    const coreBadge = document.getElementById('coreBadge');

    const sidebarStatus = document.getElementById('sidebarStatus');
    const sidebarLabel = document.getElementById('sidebarLabel');
    const sidebarSub = document.getElementById('sidebarSub');
    const dot = sidebarStatus?.querySelector('.sidebar-status-dot');
    if (sidebarLabel) sidebarLabel.textContent = '并行模式';

    coreBadge.style.display = s.binary_available ? 'none' : 'inline-block';
    const coreSummary = document.getElementById('coreSummary');
    if (coreSummary && !coreSummary.textContent) {
      coreSummary.textContent = s.binary_available ? '内核已就绪' : '内核未就绪';
    }

    const nodes = _state.get('allNodes') || [];
    const counts = _state.get('nodeCounts') || {};
    const total = counts.total ?? nodes.length;
    const enabled = counts.enabled_count ?? nodes.filter(n => !n.disabled).length;
    const configuredProxyUrl = (s.configured_proxy_url || cfg.proxy_url || '').trim();

    if (s.active_node_uri) {
      const statusText = s.active_node_name || '已连接';
      if (sidebarStatus) sidebarStatus.classList.add('active');
      if (sidebarSub) { sidebarSub.textContent = statusText; sidebarSub.title = statusText; }
      if (dot) dot.className = 'sidebar-status-dot active';
    } else if (cfg.parallel_pool_enabled) {
      const statusText = '并行模式已开启';
      if (sidebarStatus) sidebarStatus.classList.remove('active');
      if (sidebarSub) { sidebarSub.textContent = statusText; sidebarSub.title = statusText; }
      if (dot) dot.className = 'sidebar-status-dot';
    } else if (configuredProxyUrl) {
      const statusText = '手动代理已启用';
      if (sidebarStatus) sidebarStatus.classList.add('active');
      if (sidebarSub) { sidebarSub.textContent = statusText; sidebarSub.title = configuredProxyUrl; }
      if (dot) dot.className = 'sidebar-status-dot active';
    } else {
      const statusText = '未启用节点';
      if (sidebarStatus) sidebarStatus.classList.remove('active');
      if (sidebarSub) { sidebarSub.textContent = statusText; sidebarSub.title = statusText; }
      if (dot) dot.className = 'sidebar-status-dot';
    }

    const parallelTog = document.getElementById('sidebarParallelTog');
    const parallelTogWrap = document.getElementById('parallelTogWrap');
    if (parallelTog) parallelTog.checked = !!cfg.parallel_pool_enabled;
    if (parallelTogWrap) parallelTogWrap.style.display = '';

    const proxyInput = document.getElementById('proxy_url');
    if (proxyInput) proxyInput.value = cfg.proxy_url || '';

    const topBadge = document.getElementById('poolBadgeTop');
    const topBadgeText = document.getElementById('poolBadgeText');
    if (total > 0) {
      const text = `节点 ${enabled}/${total}`;
      if (topBadge) topBadge.style.display = 'flex';
      if (topBadgeText) topBadgeText.textContent = text;
    } else {
      if (topBadge) topBadge.style.display = 'none';
    }
  } catch (e) {}
}

async function saveProxy() {
  try {
    const proxyInput = document.getElementById('proxy_url');
    const proxyUrl = proxyInput.value.trim();
    await _api.post('/api/admin/stop-proxy');
    await _api.put('/api/admin/settings', { proxy_url: proxyUrl });
    showToast('代理已保存', 'ok');
    await loadProxyStatus();
    const nodes = _state.get('allNodes') || [];
    renderNodes(nodes);
  } catch (e) { showToast(e.message, 'err'); }
}

async function stopProxy() {
  try {
    await _api.post('/api/admin/stop-proxy');
    _state.set('activeUri', '');
    document.getElementById('proxy_url').value = '';
    showToast('已停止代理', 'ok');
    await loadProxyStatus();
    const nodes = _state.get('allNodes') || [];
    renderNodes(nodes);
  } catch (e) { showToast(e.message, 'err'); }
}
