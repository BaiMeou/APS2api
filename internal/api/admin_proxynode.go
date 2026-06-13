//go:build proxynode

// 本文件实现「多代理节点」的代理池管理端点（GET/POST/DELETE /api/admin/proxies）。
//
// 代理池让部署者导入多个出站代理，会话在其间轮询分散（见 transport 的代理选择）。
// 仅 proxynode 构建编译，并通过 adminProxyRoute 钩子在管理后台路由分发器里注册。
package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bsfdsagfadg/vertex/internal/config"
)

// adminProxyRoute 处理代理池相关路径，命中则返回 true。调用方（handleAdminAPI）在 requireAdmin 之后调用。
func (s *Server) adminProxyRoute(w http.ResponseWriter, r *http.Request, path string) bool {
	switch {
	case path == "/proxies":
		switch r.Method {
		case http.MethodGet:
			s.adminGetProxies(w, r)
		case http.MethodPost:
			s.adminAddProxy(w, r)
		default:
			s.adminMethodNotAllowed(w)
		}
		return true
	case strings.HasPrefix(path, "/proxies/"):
		s.adminDeleteProxy(w, r, strings.TrimPrefix(path, "/proxies/"))
		return true
	}
	return false
}

// adminGetProxies 处理 GET /api/admin/proxies：返回当前代理池 {"proxies":[...]}。
func (s *Server) adminGetProxies(w http.ResponseWriter, _ *http.Request) {
	pool := config.ProxyPoolList()
	out := make([]any, 0, len(pool))
	for i, p := range pool {
		out = append(out, map[string]any{"index": i, "url": p})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"proxies": out})
}

// adminAddProxy 处理 POST /api/admin/proxies：{url} 追加到代理池（去重），持久化后下次建会话即生效。
func (s *Server) adminAddProxy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	u := strings.TrimSpace(body.URL)
	if !validProxyURL(u) {
		s.writeJSON(w, http.StatusBadRequest, adminErr("代理地址无效，需形如 http://host:port 或 socks5://host:port (invalid proxy url)"))
		return
	}
	pool := config.ProxyPoolList()
	for _, p := range pool {
		if p == u {
			s.writeJSON(w, http.StatusBadRequest, adminErr("代理已存在 (proxy already exists)"))
			return
		}
	}
	pool = append(pool, u)
	if err := config.WriteProxyPool(pool); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, adminErr("写入代理池失败 (failed to write proxy pool)"))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// adminDeleteProxy 处理 DELETE /api/admin/proxies/{idx}：按下标删除一个代理；下标非法返回 404。
func (s *Server) adminDeleteProxy(w http.ResponseWriter, r *http.Request, rawIdx string) {
	if r.Method != http.MethodDelete {
		s.adminMethodNotAllowed(w)
		return
	}
	idx, err := strconv.Atoi(strings.TrimSpace(rawIdx))
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, adminErr("下标必须是整数 (index must be an integer)"))
		return
	}
	pool := config.ProxyPoolList()
	if idx < 0 || idx >= len(pool) {
		s.writeJSON(w, http.StatusNotFound, adminErr("下标越界，未找到该代理 (proxy index out of range)"))
		return
	}
	pool = append(pool[:idx], pool[idx+1:]...)
	if err := config.WriteProxyPool(pool); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, adminErr("写入代理池失败 (failed to write proxy pool)"))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// validProxyURL 校验代理地址：可解析、带受支持的 scheme、且有 host。
// 受支持 scheme 对齐 tls-client 接受的代理协议（http/https/socks5/socks5h）。
func validProxyURL(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return true
	default:
		return false
	}
}
