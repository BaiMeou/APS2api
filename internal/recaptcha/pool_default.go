//go:build !serveropt

package recaptcha

import "github.com/bsfdsagfadg/vertex/internal/transport"

// TokenPool 是 reCAPTCHA token 获取器的精简实现：每次实时获取，无后台预取。
//
// 后台预取池是面向高并发的服务器优化（削峰、省两段网络往返），单实例自用收益甚微，
// 故精简构建只保留实时获取这条直路。公开方法签名与完整实现一致，故调用方无需改动：
// Start/Stop 为空操作，Stats 恒返回 0,0，GetToken 直接走一次网络获取。
type TokenPool struct {
	fetch func() (string, error)
}

// NewTokenPoolSize 构造一个实时获取器。poolSize 参数被忽略（精简实现无预取池），
// 仅为与完整实现保持相同签名而保留。
func NewTokenPoolSize(net *transport.NetworkClient, poolSize int) *TokenPool {
	return &TokenPool{fetch: func() (string, error) { return FetchRecaptchaToken(net) }}
}

// Start 空操作（无后台 goroutine 可启动）。
func (p *TokenPool) Start() {}

// Stop 空操作（无后台 goroutine 可停止）。
func (p *TokenPool) Stop() {}

// Stats 恒返回 0,0（精简实现无池容量/水位概念）。
func (p *TokenPool) Stats() (size, fill int) { return 0, 0 }

// GetToken 实时获取一个 token。
func (p *TokenPool) GetToken() (string, error) { return p.fetch() }
