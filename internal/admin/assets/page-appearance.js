function applyBg(v) { document.documentElement.style.setProperty('--bg-img', v); }

async function initBg() {
  try {
    const data = await API.settings.get();
    if (data && data.background_image) {
      applyBg(data.background_image);
    }
  } catch (e) {
    const s = localStorage.getItem('vproxy_bg');
    if (s) applyBg(s);
  }
}
initBg();

async function setBgAndSync(v) {
  applyBg(v);
  localStorage.setItem('vproxy_bg', v); // Fallback
  try {
    await API.settings.put({ background_image: v });
    toast('背景已更换');
  } catch (e) {
    toast('同步背景失败', true);
  }
}

function applyBgUrl() { 
  const u = $('#bgUrl').value.trim(); 
  if (!u) return; 
  setBgAndSync(`url('${u}')`); 
}

async function uploadBg(e) { 
  const f = e.target.files[0]; 
  if (!f) return;
  if (f.size > 10 * 1024 * 1024) {
    toast('文件不能超过10MB', true);
    return;
  }
  const fd = new FormData();
  fd.append('file', f);
  try {
    const res = await fetch('/api/admin/upload-bg', { method: 'POST', body: fd });
    const data = await res.json();
    if (res.ok && data.ok) {
      setBgAndSync(data.url);
    } else {
      toast(data.error?.message || '上传失败', true);
    }
  } catch (err) {
    toast('上传失败', true);
  }
}

function resetBg() { 
  localStorage.removeItem('vproxy_bg'); 
  applyBg(DEFAULT_BG);
  API.settings.put({ background_image: DEFAULT_BG }).catch(()=>{});
  toast('已恢复默认'); 
}

function loadAppearance() {
  const presets = [
    { name: '默认', val: "url('background.jpg')" },
    { name: '纯白', val: 'white' },
    { name: '极暗蓝', val: '#0f172a' },
    { name: '纯黑', val: 'black' },
    { name: '银灰', val: '#f3f4f6' },
  ];
  $('#presets').innerHTML = presets.map(p => {
    if (p.val.startsWith('url')) {
      return `<div class="thumb" style="background-image:${p.val}" onclick="setBgAndSync(\\"${p.val}\\")" title="${p.name}"></div>`;
    } else {
      return `<div class="thumb" style="background:${p.val}" onclick="setBgAndSync('${p.val}')" title="${p.name}"></div>`;
    }
  }).join('');
  PAGE_CACHE['appearance'] = $('#page-appearance').innerHTML;
}
