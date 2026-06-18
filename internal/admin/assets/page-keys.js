async function loadKeys() {
  const d = await API.keys.list();
  $('#keysBody').innerHTML = (d.keys || []).map(k => `<tr><td>${esc(k.name)}</td><td><code>${esc(k.key_masked || k.key || '')}</code></td><td class="text-right"><button class="danger" onclick="delKey('${esc(k.name)}')">删除</button></td></tr>`).join('') || `<tr><td colspan="3" class="text-dim">暂无密钥</td></tr>`;
  PAGE_CACHE['keys'] = $('#page-keys').innerHTML;
}
async function addKey() { const name = $('#kName').value.trim(); if (!name) return toast('请填名称'); await API.keys.add(name, $('#kKey').value.trim()); $('#kName').value = ''; $('#kKey').value = ''; loadKeys(); toast('已添加'); }
async function delKey(name) { if (!confirm('删除密钥 ' + name + '？')) return; await API.keys.del(name); loadKeys(); toast('已删除'); }
