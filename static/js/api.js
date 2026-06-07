export class ApiClient {
  constructor(state) {
    this.state = state;
    this.baseUrl = '';
    this._onUnauthorized = null;
  }

  onUnauthorized(cb) {
    this._onUnauthorized = cb;
  }

  async request(method, path, body) {
    const opts = {
      method,
      headers: { 'Content-Type': 'application/json' },
    };
    const token = this.state.get('token');
    if (token) opts.headers['Authorization'] = 'Bearer ' + token;
    if (body !== undefined) opts.body = JSON.stringify(body);
    const res = await fetch(this.baseUrl + path, opts);
    if (res.status === 401) {
      this.state.saveToken('');
      if (typeof this._onUnauthorized === 'function') this._onUnauthorized();
      throw new Error('Unauthorized');
    }
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.detail || data.message || ('HTTP ' + res.status));
    return data;
  }

  async upload(path, formData) {
    const token = this.state.get('token');
    const opts = { method: 'POST', body: formData };
    if (token) opts.headers = { 'Authorization': 'Bearer ' + token };
    const res = await fetch(this.baseUrl + path, opts);
    if (res.status === 401) {
      this.state.saveToken('');
      if (typeof this._onUnauthorized === 'function') this._onUnauthorized();
      throw new Error('Unauthorized');
    }
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.detail || data.message || ('HTTP ' + res.status));
    return data;
  }

  get(path) {
    return this.request('GET', path);
  }

  post(path, body) {
    return this.request('POST', path, body);
  }

  put(path, body) {
    return this.request('PUT', path, body);
  }

  delete(path, body) {
    return this.request('DELETE', path, body);
  }
}
