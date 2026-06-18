function applyBg(v) { document.documentElement.style.setProperty('--bg-img', v); }
(function initBg() { const s = localStorage.getItem('vproxy_bg'); if (s) applyBg(s); })();
function applyBgUrl() { const u = $('#bgUrl').value.trim(); if (!u) return; const v = `url('${u}')`; applyBg(v); localStorage.setItem('vproxy_bg', v); toast('背景已更换'); }
function uploadBg(e) { const f = e.target.files[0]; if (!f) return; const rd = new FileReader(); rd.onload = () => { const v = `url('${rd.result}')`; applyBg(v); localStorage.setItem('vproxy_bg', v); toast('背景已更换'); }; rd.readAsDataURL(f); }
function resetBg() { localStorage.removeItem('vproxy_bg'); applyBg(DEFAULT_BG); toast('已恢复默认'); }
function loadAppearance() {
  const presets = ['background.jpg'];
  $('#presets').innerHTML = presets.map(p => `<div class="thumb" style="background-image:url('${p}')" onclick="applyBg(\"url('${p}')\");localStorage.setItem('vproxy_bg',\"url('${p}')\");toast('背景已更换')"></div>`).join('');
  PAGE_CACHE['appearance'] = $('#page-appearance').innerHTML;
}
