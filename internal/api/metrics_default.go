//go:build !serveropt

package api

// metricsBody 在精简构建下返回基本状态。内部健康采集（成败率/延迟分位/落盘统计/token 池水位）
// 仅在 serveropt 构建启用，此处仅给一个表明服务存活的最小响应。
func (s *Server) metricsBody() map[string]any {
	return map[string]any{"status": "ok"}
}
