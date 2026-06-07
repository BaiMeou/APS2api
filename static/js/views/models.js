import { esc, showToast } from '../utils.js';

let _api, _state;

export function initModels(api, state) {
  _api = api;
  _state = state;

  document.querySelector('[data-action="add-model"]').addEventListener('click', addModel);
  document.querySelector('[data-action="add-alias"]').addEventListener('click', addAlias);

  document.getElementById('modelsTbody').addEventListener('click', (e) => {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    if (btn.dataset.action === 'delete-model') {
      deleteModel(btn.dataset.id);
    }
  });

  document.getElementById('aliasTbody').addEventListener('click', (e) => {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    if (btn.dataset.action === 'delete-alias') {
      deleteAlias(btn.dataset.from);
    }
  });
}

export async function loadModels() {
  const data = await _api.get('/api/admin/models');
  _state.set('modelsCache', { models: data.models, alias_map: data.alias_map });
  renderModels(data.models, data.alias_map);
}

function renderModels(models, aliasMap) {
  const tbody = document.getElementById('modelsTbody');
  tbody.innerHTML = '';
  if (!models.length) {
    tbody.innerHTML = '<tr><td colspan="2" class="empty">还没有模型</td></tr>';
  } else {
    models.forEach(m => {
      const tr = document.createElement('tr');
      tr.innerHTML = `<td class="mono-cell" style="color:var(--accent)">${esc(m)}</td>
        <td><button class="btn btn-sm btn-danger" data-action="delete-model" data-id="${esc(m)}">删除</button></td>`;
      tbody.appendChild(tr);
    });
  }
  const atbody = document.getElementById('aliasTbody');
  atbody.innerHTML = '';
  const entries = Object.entries(aliasMap || {});
  if (!entries.length) {
    atbody.innerHTML = '<tr><td colspan="4" class="empty">还没有别名映射</td></tr>';
  } else {
    entries.forEach(([from, to]) => {
      const tr = document.createElement('tr');
      tr.innerHTML = `<td class="mono-cell" style="color:var(--accent)">${esc(from)}</td>
        <td style="color:var(--text-dim);font-size:16px">→</td>
        <td class="mono-cell">${esc(to)}</td>
        <td><button class="btn btn-sm btn-danger" data-action="delete-alias" data-from="${esc(from)}">删除</button></td>`;
      atbody.appendChild(tr);
    });
  }
}

async function addModel() {
  const cache = _state.get('modelsCache');
  const id = document.getElementById('new_model_id').value.trim();
  if (!id) { showToast('请输入模型 ID', 'err'); return; }
  const models = [...new Set([...cache.models, id])];
  try {
    await _api.put('/api/admin/models', { models });
    document.getElementById('new_model_id').value = '';
    await loadModels();
    showToast('已添加模型', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}

async function deleteModel(id) {
  const cache = _state.get('modelsCache');
  const models = cache.models.filter(m => m !== id);
  try {
    await _api.put('/api/admin/models', { models });
    await loadModels();
    showToast('已删除', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}

async function addAlias() {
  const from = document.getElementById('alias_from').value.trim();
  const to = document.getElementById('alias_to').value.trim();
  if (!from || !to) { showToast('别名和目标均不能为空', 'err'); return; }
  const cache = _state.get('modelsCache');
  const alias_map = { ...cache.alias_map, [from]: to };
  try {
    await _api.put('/api/admin/models', { alias_map });
    document.getElementById('alias_from').value = '';
    document.getElementById('alias_to').value = '';
    await loadModels();
    showToast('别名已添加', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}

async function deleteAlias(from) {
  const cache = _state.get('modelsCache');
  const alias_map = { ...cache.alias_map };
  delete alias_map[from];
  try {
    await _api.put('/api/admin/models', { alias_map });
    await loadModels();
    showToast('已删除', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}
