//go:build !proxynode

package transport

import "github.com/bsfdsagfadg/vertex/internal/config"

// pickProxy 返回本次会话的出站代理：单一 ProxyURL（环境变量 PROXY_URL > config.proxy_url），
// 为空表示直连。非 proxynode 构建走单代理，不引入代理池。
func pickProxy() string {
	return config.ProxyURL()
}
