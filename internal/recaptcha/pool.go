// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package recaptcha

import (
	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/transport"
)

// TokenPool 负责获取 reCAPTCHA token，对外暴露统一的获取与生命周期接口。
//
// 当前实现每次 GetToken 直接发起一次网络获取；Start/Stop 是生命周期钩子，
// Stats 报告池容量与水位。poolSize 是为后台预取预留的容量参数。
type TokenPool struct {
	fetch func(proxyURI string) (string, error)
}

// NewTokenPoolSize 构造 token 获取器。poolSize 为预取池容量（当前实现实时获取，不预取）。
func NewTokenPoolSize(net *transport.NetworkClient, poolSize int) *TokenPool {
	return &TokenPool{fetch: func(proxyURI string) (string, error) { return FetchRecaptchaToken(net, proxyURI) }}
}

// Start 启动后台获取（当前实现无需后台 goroutine）。
func (p *TokenPool) Start() {}

// Stop 停止后台获取（当前实现无后台 goroutine）。
func (p *TokenPool) Stop() {}

// Stats 返回池容量与当前水位。
func (p *TokenPool) Stats() (size, fill int) { return 0, 0 }

// GetToken 获取一个 token（使用全局配置 proxy_url）。
func (p *TokenPool) GetToken() (string, error) { return p.fetch(config.Load().ProxyURL) }

// GetTokenWithProxy 使用指定代理获取一个 token。proxyURI 为空时使用全局配置的 proxy_url。
func (p *TokenPool) GetTokenWithProxy(proxyURI string) (string, error) {
	if proxyURI == "" {
		return p.GetToken()
	}
	return p.fetch(proxyURI)
}
