// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

// telemetry-server 是 Vertex AI Proxy 的中央控制面板。
// 功能：接收心跳统计活跃实例、熔断开关（远程停止所有残血版实例）、管理面板。
// 用法: go run ./cmd/telemetry-server [-addr :8090] [-admin-password 你的密码]
package main

import (
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	instanceTTL     = 15 * time.Minute
	cleanupInterval = 1 * time.Minute
)

type instance struct {
	LastSeen time.Time `json:"last_seen"`
	Version  string    `json:"version"`
	Platform string    `json:"platform"`
}

// historyPoint 记录某一时刻的活跃用户数。
type historyPoint struct {
	Time   time.Time `json:"t"`
	Active int       `json:"a"`
}

var (
	mu        sync.RWMutex
	instances = map[string]*instance{}
	history   []historyPoint // 最近 24h，每分钟一个点

	killSwitch  atomic.Bool
	adminPasswd string
)

func main() {
	addr := flag.String("addr", ":8090", "监听地址")
	passwd := flag.String("admin-password", "", "管理面板密码（必填）")
	flag.Parse()
	adminPasswd = *passwd
	if adminPasswd == "" {
		log.Fatal("必须设置 -admin-password")
	}

	http.HandleFunc("/ping", handlePing)
	http.HandleFunc("/killswitch", handleKillSwitch)
	http.HandleFunc("/stats", handleStats)
	http.HandleFunc("/admin/killswitch", requireAuth(handleAdminKillSwitch))
	http.HandleFunc("/", handleIndex)

	// 后台清理过期实例 + 记录历史
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			mu.Lock()
			now := time.Now()
			for id, inst := range instances {
				if now.Sub(inst.LastSeen) > instanceTTL {
					delete(instances, id)
				}
			}
			// 记录历史（保留最近 24h = 1440 个点）
			history = append(history, historyPoint{Time: now, Active: len(instances)})
			if len(history) > 1440 {
				history = history[len(history)-1440:]
			}
			mu.Unlock()
		}
	}()

	log.Printf("[telemetry-server] 监听 %s | 熔断: 关闭", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

// ---- 心跳 ----

func handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var p struct {
		ID       string `json:"id"`
		Version  string `json:"version"`
		Platform string `json:"platform"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	mu.Lock()
	instances[p.ID] = &instance{LastSeen: time.Now(), Version: p.Version, Platform: p.Platform}
	mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"killswitch":` + fmt.Sprintf("%v", killSwitch.Load()) + `}`))
}

// ---- 熔断查询（客户端每分钟调用） ----

func handleKillSwitch(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"enabled":` + fmt.Sprintf("%v", killSwitch.Load()) + `}`))
}

// ---- 统计 ----

func handleStats(w http.ResponseWriter, _ *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	total := len(instances)
	byVersion := map[string]int{}
	byPlatform := map[string]int{}
	for _, inst := range instances {
		byVersion[inst.Version]++
		byPlatform[inst.Platform]++
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"active": total, "by_version": byVersion, "by_platform": byPlatform,
		"killswitch": killSwitch.Load(), "timestamp": time.Now().Format(time.RFC3339),
	})
}

// ---- 管理面板熔断操作 ----

func handleAdminKillSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	killSwitch.Store(body.Enabled)
	if body.Enabled {
		log.Printf("[telemetry-server] ⚠️  熔断已开启 — 所有残血版实例将停止服务")
	} else {
		log.Printf("[telemetry-server] ✅ 熔断已关闭 — 残血版恢复正常")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "enabled": body.Enabled})
}

// ---- 鉴权中间件 ----

func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(adminPasswd)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="admin"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// ---- 管理面板 ----

func handleIndex(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	total := len(instances)
	// 最近 60 个历史点（约 1 小时）
	chartData := history
	if len(chartData) > 60 {
		chartData = chartData[len(chartData)-60:]
	}
	mu.RUnlock()

	ks := killSwitch.Load()
	ksColor := "#22c55e"
	ksText := "正常运行"
	if ks {
		ksColor = "#ef4444"
		ksText = "已熔断"
	}

	// 构建图表数据 JSON
	chartJSON := "["
	for i, p := range chartData {
		if i > 0 {
			chartJSON += ","
		}
		chartJSON += fmt.Sprintf("[%d,%d]", p.Time.UnixMilli(), p.Active)
	}
	chartJSON += "]"

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Vertex Proxy 控制面板</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,system-ui,sans-serif;background:#0f172a;color:#e2e8f0;min-height:100vh;display:flex;flex-direction:column;align-items:center;padding:40px 20px}
h1{font-size:1.5rem;margin-bottom:8px;color:#94a3b8;font-weight:400}
.big{font-size:5rem;font-weight:700;margin:20px 0}
.cards{display:flex;gap:20px;margin:30px 0;flex-wrap:wrap;justify-content:center}
.card{background:#1e293b;border-radius:16px;padding:28px 36px;text-align:center;min-width:200px}
.card .label{color:#64748b;font-size:.85rem;margin-bottom:8px}
.card .val{font-size:2rem;font-weight:600}
.ks-btn{padding:16px 48px;font-size:1.1rem;border:none;border-radius:12px;cursor:pointer;font-weight:600;color:#fff;transition:.2s;margin-top:10px}
.ks-btn:hover{transform:scale(1.03)}
.chart-box{background:#1e293b;border-radius:16px;padding:24px;margin-top:30px;width:100%%;max-width:700px}
.chart-title{color:#94a3b8;font-size:.85rem;margin-bottom:12px}
canvas{width:100%%;height:200px}
.confirm-overlay{position:fixed;inset:0;background:rgba(0,0,0,.7);display:none;justify-content:center;align-items:center;z-index:10}
.confirm-box{background:#1e293b;border-radius:16px;padding:36px;text-align:center;max-width:400px}
.confirm-box h2{margin-bottom:12px;color:#f87171}
.confirm-box p{color:#94a3b8;margin-bottom:24px}
.confirm-box .btns{display:flex;gap:12px;justify-content:center}
.confirm-box .btns button{padding:10px 28px;border:none;border-radius:8px;cursor:pointer;font-size:.95rem;font-weight:500}
</style></head><body>
<h1>🦊 Vertex Proxy 控制面板</h1>
<div class="big" style="color:%s">%d</div>
<div style="color:#64748b;margin-top:-10px">活跃实例</div>

<div class="cards">
  <div class="card"><div class="label">服务状态</div><div class="val" style="color:%s">%s</div></div>
  <div class="card"><div class="label">熔断状态</div><div class="val" style="color:%s">%s</div></div>
</div>

%s

<div class="chart-box">
  <div class="chart-title">活跃用户趋势（最近 1 小时）</div>
  <canvas id="chart"></canvas>
</div>

<!-- 确认弹窗 -->
<div class="confirm-overlay" id="overlay">
  <div class="confirm-box">
    <h2>⚠️ 确认操作</h2>
    <p id="confirmText"></p>
    <div class="btns">
      <button style="background:#334155;color:#e2e8f0" onclick="closeOverlay()">取消</button>
      <button id="confirmBtn" style="background:#ef4444;color:#fff">确认</button>
    </div>
  </div>
</div>

<script>
const KS = %v;
const DATA = %s;
const PASS = prompt("请输入管理密码：");

function toggleKS() {
  const newState = !KS;
  document.getElementById("confirmText").textContent = newState
    ? "开启熔断后，所有残血版实例将立即停止服务，用户将看到错误提示。确定要开启吗？"
    : "关闭熔断后，所有残血版实例将恢复正常服务。确定要关闭吗？";
  document.getElementById("confirmBtn").onclick = function() { doToggle(newState); };
  document.getElementById("overlay").style.display = "flex";
}
function closeOverlay() { document.getElementById("overlay").style.display = "none"; }
function doToggle(enabled) {
  fetch("/admin/killswitch", {
    method: "POST",
    headers: {"Content-Type": "application/json", "Authorization": "Basic " + btoa("admin:" + PASS)},
    body: JSON.stringify({enabled})
  }).then(r => r.json()).then(d => { if(d.ok) location.reload(); else alert("操作失败"); });
}

// 简易 canvas 图表
(function(){
  const c = document.getElementById("chart");
  const ctx = c.getContext("2d");
  c.width = c.offsetWidth * 2; c.height = 400;
  ctx.scale(2,2);
  const W = c.offsetWidth, H = 200;
  if(!DATA.length) return;
  const max = Math.max(...DATA.map(d=>d[1]), 1);
  const pad = {l:40,r:10,t:10,b:30};
  const pw = W-pad.l-pad.r, ph = H-pad.t-pad.b;
  ctx.strokeStyle = "#334155"; ctx.lineWidth = 1;
  for(let i=0;i<=4;i++){
    const y = pad.t + ph - (ph*i/4);
    ctx.beginPath(); ctx.moveTo(pad.l,y); ctx.lineTo(W-pad.r,y); ctx.stroke();
    ctx.fillStyle="#64748b"; ctx.font="10px sans-serif"; ctx.textAlign="right";
    ctx.fillText(Math.round(max*i/4), pad.l-6, y+4);
  }
  ctx.beginPath();
  ctx.strokeStyle="#38bdf8"; ctx.lineWidth=2;
  DATA.forEach((d,i)=>{
    const x = pad.l + (i/(DATA.length-1||1))*pw;
    const y = pad.t + ph - (d[1]/max)*ph;
    i===0?ctx.moveTo(x,y):ctx.lineTo(x,y);
  });
  ctx.stroke();
  // 填充
  const last = DATA.length-1;
  ctx.lineTo(pad.l+pw, pad.t+ph);
  ctx.lineTo(pad.l, pad.t+ph);
  ctx.closePath();
  ctx.fillStyle="rgba(56,189,248,.1)";
  ctx.fill();
})();
</script></body></html>`,
		ksColor, total, ksColor, ksText, ksColor, ksText,
		`<div style="text-align:center;margin:20px 0"><button class="ks-btn" style="background:` + ksColor + `" onclick="toggleKS()">` + ksText + `</button></div>`,
		ks, chartJSON,
	)
}
