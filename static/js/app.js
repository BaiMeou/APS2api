import { ApiClient } from './api.js';
import { AppState, defaultState } from './state.js';
import { showToast } from './utils.js';
import { initSettings, loadSettings } from './views/settings.js';
import { initKeys, loadKeys } from './views/keys.js';
import { initModels, loadModels } from './views/models.js';
import { initProxy, loadSubscriptions, loadUnifiedNodes, loadProxyStatus } from './views/proxy.js';

const state = new AppState({ ...defaultState });
const api = new ApiClient(state);

api.onUnauthorized(() => {
  showLogin();
});

initSettings(api, state);
initKeys(api, state);
initModels(api, state);
initProxy(api, state);

document.getElementById('loginForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const pw = document.getElementById('pw').value;
  const errEl = document.getElementById('loginErr');
  errEl.style.display = 'none';
  try {
    const r = await fetch('/api/admin/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: pw }),
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.detail || 'Login failed');
    state.saveToken(j.token);
    document.getElementById('pw').value = '';
    showApp();
  } catch (err) { errEl.textContent = err.message; errEl.style.display = 'block'; }
});

document.querySelector('[data-action="logout"]').addEventListener('click', async () => {
  try { await api.post('/api/admin/logout'); } catch (e) {}
  state.saveToken('');
  showLogin();
});

document.querySelectorAll('.sidebar-item').forEach(t => {
  t.addEventListener('click', () => {
    document.querySelectorAll('.sidebar-item').forEach(x => x.classList.remove('active'));
    document.querySelectorAll('.panel').forEach(x => x.classList.remove('active'));
    t.classList.add('active');
    document.getElementById('panel-' + t.dataset.tab).classList.add('active');
  });
});

function showLogin() {
  document.getElementById('loginView').style.display = 'flex';
  document.getElementById('appView').classList.add('hidden');
}

function showApp() {
  document.getElementById('loginView').style.display = 'none';
  document.getElementById('appView').classList.remove('hidden');
  loadAll();
}

async function loadAll() {
  try {
    await loadSettings();
    await loadKeys();
    await loadModels();
    await loadSubscriptions();
    await loadProxyStatus();
    await loadUnifiedNodes();
  } catch (e) {}
}

(async () => {
  const saved = state.loadToken();
  if (saved) {
    try {
      await api.get('/api/admin/settings');
      showApp();
      return;
    } catch (e) {}
  }
  showLogin();
})();
