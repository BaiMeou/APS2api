import { showToast } from '../utils.js';

let _api, _state;

export function initSettings(api, state) {
  _api = api;
  _state = state;
  document.querySelector('[data-action="save-settings"]').addEventListener('click', saveSettings);
  document.querySelector('[data-action="save-protection"]').addEventListener('click', saveProtection);
  document.querySelector('[data-action="save-streaming"]').addEventListener('click', saveStreaming);
  document.querySelector('[data-action="save-parallel-pool"]').addEventListener('click', saveParallelPool);
  document.querySelector('[data-action="change-password"]').addEventListener('click', changePassword);
}

export async function loadSettings() {
  const s = await _api.get('/api/admin/settings');
  document.getElementById('port_api').value = s.port_api;
  document.getElementById('max_retries').value = s.max_retries;
  document.getElementById('debug').checked = s.debug;
  document.getElementById('proxy_url').value = s.proxy_url || '';
  document.getElementById('portBadge').textContent = 'Port ' + s.port_api;
  document.getElementById('anti429_enabled').checked = !!s.anti429_enabled;
  document.getElementById('anti429_target').value = s.anti429_target || 'system';
  document.getElementById('force_no_stream').checked = !!s.force_no_stream;
  document.getElementById('parallel_pool_size').value = s.parallel_pool_size || 4;
  document.getElementById('parallel_pool_max_size').value = s.parallel_pool_max_size || 12;
  document.getElementById('parallel_pool_max_rounds').value = s.parallel_pool_max_rounds || 0;
  document.getElementById('parallel_pool_deadline_seconds').value = s.parallel_pool_deadline_seconds || 0;
  document.getElementById('parallel_worker_base_port').value = s.parallel_worker_base_port || 12080;
  document.getElementById('parallel_worker_port_span').value = s.parallel_worker_port_span || 2000;
  document.getElementById('business_session_concurrency_limit').value = s.business_session_concurrency_limit || 0;
  document.getElementById('anti_tracking').checked = !!s.anti_tracking;
  document.getElementById('drop_max_tokens').checked = !!s.drop_max_tokens;

  const warn = document.getElementById('envOverrideWarn');
  if (s.env_proxy_url_override) {
    warn.textContent = '⚠ 环境变量 PROXY_URL=' + s.env_proxy_url_override + ' 已设置，会覆盖此处配置。';
    warn.style.display = 'block';
  } else { warn.style.display = 'none'; }
  document.getElementById('pwLockedWarn').style.display = s.admin_password_env_locked ? 'block' : 'none';
  document.getElementById('newPw').disabled = !!s.admin_password_env_locked;
}

async function saveSettings() {
  const body = {
    port_api: parseInt(document.getElementById('port_api').value, 10),
    max_retries: parseInt(document.getElementById('max_retries').value, 10),
    debug: document.getElementById('debug').checked,
  };
  try {
    const r = await _api.put('/api/admin/settings', body);
    showToast('已保存', 'ok');
    document.getElementById('saveTip').textContent = (r.notes && r.notes.length) ? r.notes.join(' · ') : '';
    document.getElementById('portBadge').textContent = 'Port ' + body.port_api;
  } catch (e) { showToast(e.message, 'err'); }
}

async function saveProtection() {
  try {
    await _api.put('/api/admin/settings', {
      anti_tracking: document.getElementById('anti_tracking').checked,
      drop_max_tokens: document.getElementById('drop_max_tokens').checked,
      anti429_enabled: document.getElementById('anti429_enabled').checked,
      anti429_target: document.getElementById('anti429_target').value,
    });
    showToast('已保存', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}

async function saveStreaming() {
  try {
    await _api.put('/api/admin/settings', { force_no_stream: document.getElementById('force_no_stream').checked });
    showToast('已保存', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}

async function saveParallelPool() {
  try {
    await _api.put('/api/admin/settings', {
      parallel_pool_size: parseInt(document.getElementById('parallel_pool_size').value, 10),
      parallel_pool_max_size: parseInt(document.getElementById('parallel_pool_max_size').value, 10),
      parallel_pool_max_rounds: parseInt(document.getElementById('parallel_pool_max_rounds').value, 10),
      parallel_pool_deadline_seconds: parseFloat(document.getElementById('parallel_pool_deadline_seconds').value),
      parallel_worker_base_port: parseInt(document.getElementById('parallel_worker_base_port').value, 10),
      parallel_worker_port_span: parseInt(document.getElementById('parallel_worker_port_span').value, 10),
      business_session_concurrency_limit: parseInt(document.getElementById('business_session_concurrency_limit').value, 10),
    });
    showToast('已保存', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}

async function changePassword() {
  const v = document.getElementById('newPw').value.trim();
  if (v.length < 6) { showToast('密码至少 6 位', 'err'); return; }
  try {
    await _api.put('/api/admin/settings', { admin_password: v });
    document.getElementById('newPw').value = '';
    showToast('密码已更新', 'ok');
  } catch (e) { showToast(e.message, 'err'); }
}
