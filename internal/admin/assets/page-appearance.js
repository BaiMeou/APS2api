function applyBg(v) { document.documentElement.style.setProperty('--bg-img', v); applyThemeColorFromBg(v); }

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
      loadAppearance();
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

async function loadAppearance() {
  const presets = [
    { name: '默认', val: "url('background.jpg')" },
    { name: '纯白', val: 'white' },
    { name: '极暗蓝', val: '#0f172a' },
    { name: '纯黑', val: 'black' },
    { name: '银灰', val: '#f3f4f6' },
  ];
  
  try {
    const res = await fetch('/api/admin/list-bgs');
    const data = await res.json();
    if (res.ok && data.ok && data.files) {
      data.files.forEach((f, i) => {
        presets.push({ name: `自定义${i+1}`, val: `url('/assets/${f}')` });
      });
    }
  } catch (e) {}

  const curBg = document.documentElement.style.getPropertyValue('--bg-img').trim();
  if (curBg && !presets.find(p => p.val === curBg)) {
    presets.unshift({ name: '当前', val: curBg });
  }
  const presetsEl = $('#presets');
  if (presetsEl) {
    presetsEl.innerHTML = presets.map(p => {
      if (p.val.startsWith('url')) {
        return `<div class="thumb" style="background-image:${p.val}" onclick="setBgAndSync(&quot;${p.val}&quot;)" title="${p.name}"></div>`;
      } else {
        return `<div class="thumb" style="background:${p.val}" onclick="setBgAndSync(&quot;${p.val}&quot;)" title="${p.name}"></div>`;
      }
    }).join('');
  }
  PAGE_CACHE['appearance'] = $('#page-appearance').innerHTML;
}

function applyThemeColorFromBg(bgValue) {
  const match = bgValue.match(/url\(['"]?(.*?)['"]?\)/);
  const src = match ? match[1] : null;
  const canvas = document.createElement("canvas");
  const ctx = canvas.getContext("2d", { willReadFrequently: true });
  canvas.width = 64; canvas.height = 64;
  if (src) {
    const img = new Image();
    if (src.startsWith('http') && !src.startsWith(location.origin)) {
      img.crossOrigin = "Anonymous";
    }
    img.onload = () => { ctx.drawImage(img, 0, 0, 64, 64); extractAndSetColors(ctx); };
    img.src = src;
  } else {
    ctx.fillStyle = bgValue;
    ctx.fillRect(0, 0, 64, 64);
    extractAndSetColors(ctx);
  }
}

function extractAndSetColors(ctx) {
  const data = ctx.getImageData(0, 0, 64, 64).data;
  let sumR = 0, sumG = 0, sumB = 0, totalWeight = 0;
  
  for (let i = 0; i < data.length; i += 16) {
    let r = data[i], g = data[i+1], b = data[i+2];
    let [h, s, l] = rgbToHsl(r, g, b);
    let weight = s * (1 - Math.abs(2 * l - 1));
    weight = Math.max(weight, 0.02);
    sumR += r * weight; sumG += g * weight; sumB += b * weight;
    totalWeight += weight;
  }
  
  let r = 0, g = 0, b = 0;
  if (totalWeight > 0) {
    r = sumR / totalWeight; g = sumG / totalWeight; b = sumB / totalWeight;
  }
  
  let [h, s, l] = rgbToHsl(r, g, b);
  if (s < 0.25) s = 0.5; 
  if (l < 0.4) l = 0.6; 
  if (l > 0.7) l = 0.55; 
  
  const c1 = hslToHex(h, s, l);
  const c2 = hslToHex(h, s, Math.max(l - 0.18, 0.25));
  
  const toRgbString = (hex) => {
    let r = parseInt(hex.slice(1,3), 16);
    let g = parseInt(hex.slice(3,5), 16);
    let b = parseInt(hex.slice(5,7), 16);
    return `${r}, ${g}, ${b}`;
  };
  
  const rgb1 = toRgbString(c1);
  const rgb2 = toRgbString(c2);
  
  document.documentElement.style.setProperty("--gold", c1);
  document.documentElement.style.setProperty("--gold-rgb", rgb1);
  document.documentElement.style.setProperty("--gold-deep", c2);
  document.documentElement.style.setProperty("--gold-soft", `rgba(${rgb1}, 0.15)`);
  document.documentElement.style.setProperty("--gold-shadow1", `rgba(${rgb1}, 0.3)`);
  document.documentElement.style.setProperty("--gold-shadow2", `rgba(${rgb1}, 0.42)`);
  
  // Create a complementary blueish color for the second veil based on the dominant hue
  let blueH = (h + 0.5) % 1;
  const cBlue = hslToHex(blueH, s, l);
  const rgbBlue = toRgbString(cBlue);
  document.documentElement.style.setProperty("--blue-soft", `rgba(${rgbBlue}, 0.1)`);
  
  // Background panels adaptive colors (dark theme)
  const glassHex = hslToHex(h, Math.min(s, 0.2), 0.11);
  const veilHex = hslToHex(h, Math.min(s, 0.25), 0.06);
  const veilDarkHex = hslToHex(h, Math.min(s, 0.25), 0.04);
  const strokeHex = hslToHex(h, Math.min(s, 0.4), 0.95);
  
  const glassRgb = toRgbString(glassHex);
  const veilRgb = toRgbString(veilHex);
  const veilDarkRgb = toRgbString(veilDarkHex);
  const strokeRgb = toRgbString(strokeHex);
  
  document.documentElement.style.setProperty("--glass", `rgba(${glassRgb}, 0.38)`);
  document.documentElement.style.setProperty("--glass-solid", `rgba(${glassRgb}, 0.68)`);
  document.documentElement.style.setProperty("--veil-light", `rgba(${veilRgb}, 0.42)`);
  document.documentElement.style.setProperty("--veil-dark", `rgba(${veilDarkRgb}, 0.62)`);
  document.documentElement.style.setProperty("--stroke", `rgba(${strokeRgb}, 0.14)`);
}

function rgbToHsl(r, g, b) {
  r /= 255; g /= 255; b /= 255;
  const max = Math.max(r, g, b), min = Math.min(r, g, b);
  let h, s, l = (max + min) / 2;
  if (max === min) { h = s = 0; }
  else {
    const d = max - min;
    s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
    switch (max) {
      case r: h = (g - b) / d + (g < b ? 6 : 0); break;
      case g: h = (b - r) / d + 2; break;
      case b: h = (r - g) / d + 4; break;
    }
    h /= 6;
  }
  return [h, s, l];
}

function hslToHex(h, s, l) {
  let r, g, b;
  if (s === 0) { r = g = b = l; }
  else {
    const hue2rgb = (p, q, t) => {
      if(t < 0) t += 1; if(t > 1) t -= 1;
      if(t < 1/6) return p + (q - p) * 6 * t;
      if(t < 1/2) return q;
      if(t < 2/3) return p + (q - p) * (2/3 - t) * 6;
      return p;
    };
    const q = l < 0.5 ? l * (1 + s) : l + s - l * s;
    const p = 2 * l - q;
    r = hue2rgb(p, q, h + 1/3); g = hue2rgb(p, q, h); b = hue2rgb(p, q, h - 1/3);
  }
  const toHex = x => {
    const hex = Math.round(x * 255).toString(16);
    return hex.length === 1 ? "0" + hex : hex;
  };
  return `#${toHex(r)}${toHex(g)}${toHex(b)}`;
}
