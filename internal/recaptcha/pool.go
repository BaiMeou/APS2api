package recaptcha

import "github.com/bsfdsagfadg/vertex/internal/transport"

// TokenPool 负责获取 reCAPTCHA token，对外暴露统一的获取与生命周期接口。
//
// 当前实现每次 GetToken 直接发起一次网络获取；Start/Stop 是生命周期钩子，
// Stats 报告池容量与水位。poolSize 是为后台预取预留的容量参数。
type TokenPool struct {
	fetch func() (string, error)
}

// NewTokenPoolSize 构造 token 获取器。poolSize 为预取池容量（当前实现实时获取，不预取）。
func NewTokenPoolSize(net *transport.NetworkClient, poolSize int) *TokenPool {
	return &TokenPool{fetch: func() (string, error) { return FetchRecaptchaToken(net) }}
}

// Start 启动后台获取（当前实现无需后台 goroutine）。
func (p *TokenPool) Start() {}

// Stop 停止后台获取（当前实现无后台 goroutine）。
func (p *TokenPool) Stop() {}

// Stats 返回池容量与当前水位。
func (p *TokenPool) Stats() (size, fill int) { return 0, 0 }

// GetToken 获取一个 token。
func (p *TokenPool) GetToken() (string, error) { return p.fetch() }
