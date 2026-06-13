//go:build !proxynode

package api

import "net/http"

// adminProxyRoute 在非 proxynode 构建下恒返回 false（不注册代理池管理端点）。
// 代理池是多代理节点特性，仅 proxynode 构建提供 GET/POST/DELETE /api/admin/proxies。
func (s *Server) adminProxyRoute(w http.ResponseWriter, r *http.Request, path string) bool {
	return false
}
