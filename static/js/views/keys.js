import { esc, showToast, maskKey } from '../utils.js';

let _api, _state;

export function initKeys(api, state) {
  _api = api;
  _state = state;

  document.querySelector('[data-action="add-key"]').addEventListener('click', addKey);
  document.querySelector('[data-action="generate-key"]').addEventListener('click', generateKey);

  document.getElementById('keysTbody').addEventListener('click', (e) => {
    const copyBtn = e.target.closest('.action-copy-btn');
    if (copyBtn) {
      e.stopPropagation();
      navigator.clipboard.writeText(copyBtn.dataset.key).then(() => showToast('已复制', 'ok')).catch(() => showToast('复制失败', 'err'));
      return;
    }

    const delBtn = e.target.closest('[data-action="delete-key"]');
    if (delBtn) {
      deleteKey(delBtn.dataset.name);
      return;
    }
  });

}

function generateKey() {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  let sk = 'sk-';
  for (let i = 0; i < 48; i++) sk += chars.charAt(Math.floor(Math.random() * chars.length));
  document.getElementById('key_value').value = sk;
}

export async function loadKeys() {
  const data = await _api.get('/api/admin/keys');
  const tbody = document.getElementById('keysTbody');
  tbody.innerHTML = '';
  if (!data.keys.length) {
    tbody.innerHTML = '<tr><td colspan="4" class="empty">还没有密钥。</td></tr>';
    return;
  }
  data.keys.forEach(k => {
    const tr = document.createElement('tr');
    tr.innerHTML = `
      <td class="mono-cell name-cell">${esc(k.name)}</td>
      <td class="mono-cell key-cell"><span class="masked">${esc(maskKey(k.key))}</span><button class="action-copy-btn" data-key="${esc(k.key)}" title="复制密钥"><svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button></td>
      <td class="desc-cell" style="color:var(--text-muted)">${esc(k.description || '—')}</td>
      <td style="text-align:center"><button class="btn btn-sm btn-danger" data-action="delete-key" data-name="${esc(k.name)}">删除</button></td>`;
    tbody.appendChild(tr);
  });
}

async function addKey() {
  const name = document.getElementById('key_name').value.trim();
  const key = document.getElementById('key_value').value.trim();
  const description = document.getElementById('key_desc').value.trim();
  if (!name || !key) { showToast('name / key 不能为空', 'err'); return; }
  try {
    await _api.post('/api/admin/keys', { name, key, description });
    document.getElementById('key_name').value = '';
    document.getElementById('key_value').value = '';
    document.getElementById('key_desc').value = '';
    await loadKeys();
    showToast('已添加', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}

async function deleteKey(name) {
  try {
    await _api.delete('/api/admin/keys/' + encodeURIComponent(name));
    await loadKeys();
    showToast('已删除', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}
