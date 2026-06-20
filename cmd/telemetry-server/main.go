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
	"strings"
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

type historyPoint struct {
	Time   time.Time `json:"t"`
	Active int       `json:"a"`
}

var (
	mu        sync.RWMutex
	instances = map[string]*instance{}
	history   []historyPoint

	killSwitch  atomic.Bool
	adminPasswd string
	startTime   = time.Now()
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
	http.HandleFunc("/", requireAuth(handleIndex))

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

func handleKillSwitch(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"enabled":` + fmt.Sprintf("%v", killSwitch.Load()) + `}`))
}

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

func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(adminPasswd)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Vertex Proxy 控制面板"`)
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
	byVersion := map[string]int{}
	byPlatform := map[string]int{}
	for _, inst := range instances {
		byVersion[inst.Version]++
		byPlatform[inst.Platform]++
	}
	chartData := history
	if len(chartData) > 120 {
		chartData = chartData[len(chartData)-120:]
	}
	// 构建实例列表
	type instItem struct {
		ID       string `json:"id"`
		Version  string `json:"version"`
		Platform string `json:"platform"`
		LastSeen string `json:"last_seen"`
		Ago      string `json:"ago"`
	}
	var instList []instItem
	for id, inst := range instances {
		instList = append(instList, instItem{
			ID: id[:min(8, len(id))], Version: inst.Version, Platform: inst.Platform,
			LastSeen: inst.LastSeen.Format("15:04:05"), Ago: time.Since(inst.LastSeen).Truncate(time.Second).String(),
		})
	}
	mu.RUnlock()

	ks := killSwitch.Load()
	ksColor := "#22c55e"
	ksText := "正常运行"
	ksBtnText := "🛡️ 开启熔断"
	ksCardClass := "ok"
	if ks {
		ksColor = "#ef4444"
		ksText = "已熔断"
		ksBtnText = "✅ 关闭熔断"
		ksCardClass = ""
	}

	uptime := time.Since(startTime).Truncate(time.Second).String()

	versionJSON, _ := json.Marshal(byVersion)
	platformJSON, _ := json.Marshal(byPlatform)
	chartJSON, _ := json.Marshal(chartData)
	instJSON, _ := json.Marshal(instList)

	replacer := strings.NewReplacer(
		"{{TOTAL}}", fmt.Sprintf("%d", total),
		"{{KS_COLOR}}", ksColor,
		"{{KS_TEXT}}", ksText,
		"{{KS_BTN_TEXT}}", ksBtnText,
		"{{KS_CARD_CLASS}}", ksCardClass,
		"{{UPTIME}}", uptime,
		"{{VERSION_JSON}}", string(versionJSON),
		"{{PLATFORM_JSON}}", string(platformJSON),
		"{{CHART_JSON}}", string(chartJSON),
		"{{INST_JSON}}", string(instJSON),
		"{{INST_COUNT}}", fmt.Sprintf("%d", len(instList)),
	)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(replacer.Replace(pageTemplate)))
}

var pageTemplate = `<!DOCTYPE html><html lang="zh"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Vertex Proxy 控制面板</title>
<style>
:root{--bg:#0a0e1a;--card:#111827;--card2:#1a2235;--border:#1e293b;--text:#e2e8f0;--muted:#64748b;--accent:#3b82f6;--green:#22c55e;--red:#ef4444;--radius:16px;--glass:rgba(17,24,39,.6)}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,system-ui,sans-serif;background:var(--bg);color:var(--text);min-height:100vh;overflow-x:hidden}
body::before{content:'';position:fixed;top:-50%;left:-50%;width:200%;height:200%;background:radial-gradient(circle at 30% 20%,rgba(59,130,246,.06) 0%,transparent 50%),radial-gradient(circle at 70% 80%,rgba(139,92,246,.04) 0%,transparent 50%);pointer-events:none;z-index:0}
.container{max-width:960px;margin:0 auto;padding:32px 20px;position:relative;z-index:1}
header{text-align:center;margin-bottom:40px}
header h1{font-size:1.6rem;font-weight:600;background:linear-gradient(135deg,#60a5fa,#a78bfa);-webkit-background-clip:text;-webkit-text-fill-color:transparent;margin-bottom:4px}
header .sub{color:var(--muted);font-size:.85rem}
.hero{text-align:center;margin-bottom:36px}
.hero .num{font-size:4.5rem;font-weight:700;background:linear-gradient(135deg,#60a5fa,#34d399);-webkit-background-clip:text;-webkit-text-fill-color:transparent;line-height:1.1}
.hero .label{color:var(--muted);font-size:.9rem;margin-top:4px}
.grid{display:grid;gap:16px;margin-bottom:24px}
.grid-3{grid-template-columns:repeat(3,1fr)}
.grid-2{grid-template-columns:repeat(2,1fr)}
@media(max-width:640px){.grid-3,.grid-2{grid-template-columns:1fr}}
.card{background:var(--glass);backdrop-filter:blur(20px);border:1px solid var(--border);border-radius:var(--radius);padding:24px;transition:all .25s}
.card:hover{border-color:rgba(59,130,246,.3);box-shadow:0 0 20px rgba(59,130,246,.05)}
.card .label{color:var(--muted);font-size:.78rem;text-transform:uppercase;letter-spacing:.5px;margin-bottom:8px}
.card .val{font-size:1.8rem;font-weight:700}
.card .hint{color:var(--muted);font-size:.75rem;margin-top:4px}
.ks-card{background:linear-gradient(135deg,rgba(239,68,68,.05),rgba(239,68,68,.02));border-color:rgba(239,68,68,.15)}
.ks-card.ok{background:linear-gradient(135deg,rgba(34,197,94,.05),rgba(34,197,94,.02));border-color:rgba(34,197,94,.15)}
.ks-toggle{display:flex;align-items:center;justify-content:space-between;gap:16px}
.ks-toggle .info{flex:1}
.ks-toggle .info h3{font-size:1rem;font-weight:600;margin-bottom:4px}
.ks-toggle .info p{color:var(--muted);font-size:.8rem}
.btn{padding:10px 24px;border:none;border-radius:10px;cursor:pointer;font-size:.85rem;font-weight:600;color:#fff;transition:all .2s;white-space:nowrap}
.btn:hover{transform:translateY(-1px);box-shadow:0 4px 12px rgba(0,0,0,.3)}
.btn-red{background:linear-gradient(135deg,#ef4444,#dc2626)}
.btn-green{background:linear-gradient(135deg,#22c55e,#16a34a)}
.chart-box{background:var(--glass);backdrop-filter:blur(20px);border:1px solid var(--border);border-radius:var(--radius);padding:24px;margin-bottom:24px}
.chart-title{color:var(--muted);font-size:.8rem;text-transform:uppercase;letter-spacing:.5px;margin-bottom:16px;display:flex;justify-content:space-between;align-items:center}
.chart-title .refresh{color:var(--accent);cursor:pointer;font-size:.75rem;text-transform:none;letter-spacing:0}
canvas{width:100%;height:180px;display:block}
.dist-row{display:flex;gap:8px;flex-wrap:wrap;margin-top:12px}
.dist-tag{background:var(--card2);border:1px solid var(--border);border-radius:8px;padding:6px 12px;font-size:.78rem;color:var(--muted)}
.dist-tag b{color:var(--text);margin-right:4px}
.table-box{background:var(--glass);backdrop-filter:blur(20px);border:1px solid var(--border);border-radius:var(--radius);overflow:hidden;margin-bottom:24px}
.table-box .head{padding:16px 20px;border-bottom:1px solid var(--border);display:flex;justify-content:space-between;align-items:center}
.table-box .head h3{font-size:.9rem;font-weight:600}
.table-box .head .count{color:var(--muted);font-size:.8rem}
table{width:100%;border-collapse:collapse}
th{text-align:left;padding:10px 20px;color:var(--muted);font-size:.75rem;text-transform:uppercase;letter-spacing:.5px;border-bottom:1px solid var(--border);font-weight:500}
td{padding:10px 20px;font-size:.82rem;border-bottom:1px solid rgba(30,41,59,.5)}
tr:last-child td{border-bottom:none}
tr:hover td{background:rgba(59,130,246,.03)}
.mono{font-family:'SF Mono',ui-monospace,monospace;font-size:.78rem;color:var(--muted)}
.badge{display:inline-block;padding:2px 8px;border-radius:6px;font-size:.72rem;font-weight:500}
.badge-blue{background:rgba(59,130,246,.15);color:#60a5fa}
.overlay{position:fixed;inset:0;background:rgba(0,0,0,.75);backdrop-filter:blur(4px);display:none;justify-content:center;align-items:center;z-index:100}
.overlay.show{display:flex}
.dialog{background:var(--card);border:1px solid var(--border);border-radius:20px;padding:36px;text-align:center;max-width:420px;width:90%}
.dialog h2{font-size:1.2rem;margin-bottom:8px}
.dialog p{color:var(--muted);font-size:.85rem;margin-bottom:24px;line-height:1.6}
.dialog .btns{display:flex;gap:12px;justify-content:center}
.dialog .btns .btn{padding:10px 32px}
footer{text-align:center;color:var(--muted);font-size:.75rem;margin-top:40px;padding:20px 0;border-top:1px solid var(--border)}
footer a{color:var(--accent);text-decoration:none}
</style></head><body>
<div class="container">
<header>
  <h1>🦊 Vertex Proxy 控制面板</h1>
  <div class="sub">Copyright (c) 2026 BaiMeow · PolyForm Noncommercial</div>
</header>
<div class="hero">
  <div class="num">{{TOTAL}}</div>
  <div class="label">当前活跃实例</div>
</div>
<div class="grid grid-3">
  <div class="card">
    <div class="label">服务状态</div>
    <div class="val" style="color:{{KS_COLOR}}">{{KS_TEXT}}</div>
    <div class="hint">运行时间 {{UPTIME}}</div>
  </div>
  <div class="card ks-card {{KS_CARD_CLASS}}">
    <div class="label">熔断状态</div>
    <div class="val" style="color:{{KS_COLOR}}">{{KS_TEXT}}</div>
  </div>
  <div class="card ks-card {{KS_CARD_CLASS}}">
    <div class="ks-toggle">
      <div class="info"><h3>远程熔断</h3><p>开启后所有分发实例停止服务</p></div>
      <button class="btn" onclick="toggleKS()" id="ksBtn">{{KS_BTN_TEXT}}</button>
    </div>
  </div>
</div>
<div class="chart-box">
  <div class="chart-title"><span>📈 活跃用户趋势（最近 2 小时）</span><span class="refresh" onclick="location.reload()">↻ 刷新</span></div>
  <canvas id="trendChart"></canvas>
</div>
<div class="grid grid-2">
  <div class="chart-box">
    <div class="chart-title"><span>📦 版本分布</span></div>
    <canvas id="versionChart"></canvas>
    <div class="dist-row" id="versionTags"></div>
  </div>
  <div class="chart-box">
    <div class="chart-title"><span>💻 平台分布</span></div>
    <canvas id="platformChart"></canvas>
    <div class="dist-row" id="platformTags"></div>
  </div>
</div>
<div class="table-box">
  <div class="head"><h3>实例列表</h3><span class="count">{{INST_COUNT}} 个实例</span></div>
  <table><thead><tr><th>ID</th><th>版本</th><th>平台</th><th>最后心跳</th></tr></thead><tbody id="instBody"></tbody></table>
</div>
<footer>Vertex Proxy Telemetry · <a href="https://discord.gg/odysseia">Discord 社区</a> · 版权所有 BaiMeow</footer>
</div>
<div class="overlay" id="overlay">
  <div class="dialog">
    <h2 id="dialogTitle">⚠️ 确认操作</h2>
    <p id="dialogText"></p>
    <div class="btns">
      <button class="btn" style="background:#334155" onclick="closeDialog()">取消</button>
      <button class="btn btn-red" id="dialogConfirm">确认</button>
    </div>
  </div>
</div>
<script>
var KS={{KS}};
var VER={{VERSION_JSON}};
var PLAT={{PLATFORM_JSON}};
var DATA={{CHART_JSON}};
var INST={{INST_JSON}};

function toggleKS(){
  var on=!KS;
  document.getElementById("dialogText").textContent=on
    ?"开启熔断后，所有分发实例将立即停止服务。确定要开启吗？"
    :"关闭熔断后，所有分发实例将恢复正常服务。确定要关闭吗？";
  document.getElementById("dialogTitle").textContent=on?"🔴 开启熔断":"🟢 关闭熔断";
  document.getElementById("dialogConfirm").className="btn "+(on?"btn-red":"btn-green");
  document.getElementById("dialogConfirm").textContent=on?"确认开启":"确认关闭";
  document.getElementById("dialogConfirm").onclick=function(){doToggle(on)};
  document.getElementById("overlay").classList.add("show");
}
function closeDialog(){document.getElementById("overlay").classList.remove("show")}
function doToggle(on){
  var pw=prompt("请输入管理密码确认操作：");
  if(!pw)return;
  fetch("/admin/killswitch",{method:"POST",headers:{"Content-Type":"application/json","Authorization":"Basic "+btoa("admin:"+pw)},body:JSON.stringify({enabled:on})})
  .then(function(r){return r.json()}).then(function(d){if(d.ok)location.reload();else alert("操作失败")}).catch(function(){alert("网络错误")});
}

(function(){
  var c=document.getElementById("trendChart"),ctx=c.getContext("2d");
  var dpr=window.devicePixelRatio||1;var W=c.offsetWidth;var H=180;
  c.width=W*dpr;c.height=H*dpr;ctx.scale(dpr,dpr);
  if(!DATA.length){ctx.fillStyle="#64748b";ctx.font="14px sans-serif";ctx.textAlign="center";ctx.fillText("暂无数据",W/2,H/2);return}
  var max=0;for(var i=0;i<DATA.length;i++)if(DATA[i][1]>max)max=DATA[i][1];if(max<1)max=1;
  var pad={l:44,r:16,t:12,b:32};var pw=W-pad.l-pad.r,ph=H-pad.t-pad.b;
  ctx.strokeStyle="rgba(30,41,59,.8)";ctx.lineWidth=1;
  for(var i=0;i<=4;i++){var y=pad.t+ph-(ph*i/4);ctx.beginPath();ctx.moveTo(pad.l,y);ctx.lineTo(W-pad.r,y);ctx.stroke();ctx.fillStyle="#475569";ctx.font="11px sans-serif";ctx.textAlign="right";ctx.fillText(Math.round(max*i/4),pad.l-8,y+4)}
  var step=Math.max(1,Math.floor(DATA.length/6));
  for(var i=0;i<DATA.length;i+=step){var x=pad.l+(i/(DATA.length-1||1))*pw;ctx.fillStyle="#475569";ctx.font="10px sans-serif";ctx.textAlign="center";ctx.fillText(new Date(DATA[i][0]).toLocaleTimeString("zh-CN",{hour:"2-digit",minute:"2-digit"}),x,H-6)}
  var grad=ctx.createLinearGradient(0,pad.t,0,pad.t+ph);grad.addColorStop(0,"rgba(56,189,248,.2)");grad.addColorStop(1,"rgba(56,189,248,.01)");
  ctx.beginPath();for(var i=0;i<DATA.length;i++){var x=pad.l+(i/(DATA.length-1||1))*pw;var y=pad.t+ph-(DATA[i][1]/max)*ph;i===0?ctx.moveTo(x,y):ctx.lineTo(x,y)}
  ctx.lineTo(pad.l+pw,pad.t+ph);ctx.lineTo(pad.l,pad.t+ph);ctx.closePath();ctx.fillStyle=grad;ctx.fill();
  ctx.beginPath();ctx.strokeStyle="#38bdf8";ctx.lineWidth=2.5;ctx.lineJoin="round";
  for(var i=0;i<DATA.length;i++){var x=pad.l+(i/(DATA.length-1||1))*pw;var y=pad.t+ph-(DATA[i][1]/max)*ph;i===0?ctx.moveTo(x,y):ctx.lineTo(x,y)}ctx.stroke();
  var last=DATA[DATA.length-1];var lx=pad.l+pw;var ly=pad.t+ph-(last[1]/max)*ph;
  ctx.beginPath();ctx.arc(lx,ly,4,0,Math.PI*2);ctx.fillStyle="#38bdf8";ctx.fill();
  ctx.beginPath();ctx.arc(lx,ly,8,0,Math.PI*2);ctx.fillStyle="rgba(56,189,248,.2)";ctx.fill();
})();

function drawPie(canvasId,data,colors){
  var c=document.getElementById(canvasId),ctx=c.getContext("2d");
  var dpr=window.devicePixelRatio||1;c.width=c.offsetWidth*dpr;c.height=160*dpr;ctx.scale(dpr,dpr);
  var entries=[];for(var k in data)entries.push([k,data[k]]);
  if(!entries.length){ctx.fillStyle="#64748b";ctx.font="12px sans-serif";ctx.textAlign="center";ctx.fillText("无数据",c.offsetWidth/2,80);return}
  var total=0;for(var i=0;i<entries.length;i++)total+=entries[i][1];
  var cx=60,cy=80,r=55;var angle=-Math.PI/2;
  for(var i=0;i<entries.length;i++){var slice=(entries[i][1]/total)*Math.PI*2;ctx.beginPath();ctx.moveTo(cx,cy);ctx.arc(cx,cy,r,angle,angle+slice);ctx.closePath();ctx.fillStyle=colors[i%colors.length];ctx.fill();angle+=slice}
  ctx.beginPath();ctx.arc(cx,cy,30,0,Math.PI*2);ctx.fillStyle="#111827";ctx.fill();
  ctx.fillStyle="#e2e8f0";ctx.font="bold 16px sans-serif";ctx.textAlign="center";ctx.textBaseline="middle";ctx.fillText(total,cx,cy);
}
var COLORS=["#3b82f6","#8b5cf6","#ec4899","#f59e0b","#22c55e","#06b6d4","#f97316","#6366f1"];
drawPie("versionChart",VER,COLORS);drawPie("platformChart",PLAT,COLORS);

function renderTags(elId,data){var el=document.getElementById(elId);var html="";for(var k in data)html+='<span class="dist-tag"><b>'+data[k]+'</b>'+k+'</span>';el.innerHTML=html}
renderTags("versionTags",VER);renderTags("platformTags",PLAT);

var body=document.getElementById("instBody");
document.getElementById("instCount").textContent=INST.length+" 个实例";
body.innerHTML=INST.map(function(i){return '<tr><td class="mono">'+i.id+'</td><td><span class="badge badge-blue">'+i.version+'</span></td><td>'+i.platform+'</td><td class="mono">'+i.last_seen+' ('+i.ago+'前)</td></tr>'}).join("");

setTimeout(function(){location.reload()},30000);
</script></body></html>`
