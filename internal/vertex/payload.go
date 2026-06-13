package vertex

import (
	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/transform"
)

// batchGraphql 请求的固定包壳常量（逐字节对齐 PoC body.json / vertex_client）。
const (
	querySignature = "2/l8eCsMMY49imcDQ/lwwXyL8cYtTjxZBF2dNqy69LodY="
	operationName  = "StreamGenerateContentAnonymous"
)

// buildRequestPayload 构建发往上游的完整请求体（对齐 _build_request_payload）：
// 用 transform 构建 variables，再强制注入 region=global 与 recaptchaToken，最后包壳。
func buildRequestPayload(model string, geminiPayload map[string]any, recaptchaToken string, cfg config.AppConfig) map[string]any {
	vars := transform.BuildVertexVariables(model, geminiPayload, cfg)
	vars["region"] = "global"
	vars["recaptchaToken"] = recaptchaToken
	return map[string]any{
		"querySignature": querySignature,
		"operationName":  operationName,
		"variables":      vars,
	}
}
