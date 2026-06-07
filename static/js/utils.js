export function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
}

export function showToast(msg, type = 'info') {
  const wrap = document.getElementById('toastWrap');
  const el = document.createElement('div');
  el.className = 'toast ' + type;
  el.textContent = msg;
  wrap.appendChild(el);
  setTimeout(() => { el.style.opacity = '0'; el.style.transition = 'opacity .25s'; }, 2800);
  setTimeout(() => el.remove(), 3100);
}

export function maskKey(k) {
  return (!k || k.length < 8) ? k : (k.slice(0, 6) + '…' + k.slice(-4));
}

export function toggleReveal(el) {
  const f = el.dataset.full;
  if (el.dataset.shown === '1') {
    el.textContent = maskKey(f);
    el.dataset.shown = '0';
  } else {
    el.textContent = f;
    el.dataset.shown = '1';
  }
}

export function formatNodeTestMeta(node, health) {
  const h = health[node.raw_uri] || {};
  const bits = [];
  if (h.last_test_at) {
    const cls = h.last_test_ok ? 'node-test-ok' : 'node-test-bad';
    const label = h.last_test_ok ? '测试通过' : '测试失败';
    const ms = h.last_test_ms ? ` ${Math.round(h.last_test_ms)}ms` : '';
    bits.push(`<span class="${cls}">${label}${ms}</span>`);
  }
  const err = node.disabled_reason || h.last_test_error || h.last_error || '';
  if (err) bits.push(`<span title="${esc(err)}">错误 ${esc(String(err).slice(0, 90))}</span>`);
  return bits.join('');
}

export function formatImportSummary(data) {
  return `解析 ${data.imported_count || 0} · 新增 ${data.added_count || 0} · 更新 ${data.updated_count || 0} · 总计 ${data.total || 0}`;
}

export function updateCountsFromNodes(nodes) {
  const total = nodes.length;
  const disabled_count = nodes.filter(n => n.disabled).length;
  return { total, enabled_count: total - disabled_count, disabled_count };
}

export function formatRecentTest(node, result) {
  const name = node.name || node.server || '未命名节点';
  if (result.ok) {
    const ms = result.elapsed_ms ? ` ${Math.round(result.elapsed_ms)}ms` : '';
    return `最近：${name} · 测试通过${ms}`;
  }
  const error = result.error ? ` ${String(result.error).slice(0, 90)}` : '';
  return `最近：${name} · 测试失败${error}`;
}

export function setTestProgress(visible, text = '', detail = '', ratio = 0) {
  const wrap = document.getElementById('testProgress');
  const textEl = document.getElementById('testProgressText');
  const fillEl = document.getElementById('testProgressFill');
  const detailEl = document.getElementById('testProgressDetail');
  if (!wrap) return;
  wrap.style.display = visible ? 'block' : 'none';
  if (textEl) textEl.textContent = text;
  if (detailEl) detailEl.textContent = detail;
  if (fillEl) fillEl.style.width = `${Math.max(0, Math.min(100, ratio * 100))}%`;
}
