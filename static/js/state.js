const TOKEN_KEY = 'vp_admin_token';

export class AppState {
  constructor(initial = {}) {
    this._state = { ...initial };
    this._listeners = new Map();
  }

  get(key) {
    return this._state[key];
  }

  set(key, value) {
    this._state[key] = value;
    this._notify(key, value);
  }

  subscribe(key, cb) {
    if (!this._listeners.has(key)) {
      this._listeners.set(key, []);
    }
    this._listeners.get(key).push(cb);
    return () => {
      const handlers = this._listeners.get(key);
      if (handlers) {
        const idx = handlers.indexOf(cb);
        if (idx >= 0) handlers.splice(idx, 1);
      }
    };
  }

  _notify(key, value) {
    const handlers = this._listeners.get(key);
    if (handlers) {
      handlers.forEach(cb => cb(value));
    }
  }

  clear() {
    this._state = {};
  }

  loadToken() {
    const saved = localStorage.getItem(TOKEN_KEY) || '';
    this.set('token', saved);
    return saved;
  }

  saveToken(token) {
    this.set('token', token);
    if (token) {
      localStorage.setItem(TOKEN_KEY, token);
    } else {
      localStorage.removeItem(TOKEN_KEY);
    }
  }
}

export const defaultState = {
  token: '',
  activeUri: '',
  allNodes: [],
  subscriptions: [],
  selectedSubIds: new Set(),
  nodeHealth: {},
  nodeCounts: { total: 0, enabled_count: 0, disabled_count: 0 },
  modelsCache: { models: [], alias_map: {} },
};
