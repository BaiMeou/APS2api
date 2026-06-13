package api

// metricsBody 返回服务的实时状态，供 /metrics 与管理后台 stats 做存活探测。
func (s *Server) metricsBody() map[string]any {
	return map[string]any{"status": "ok"}
}
