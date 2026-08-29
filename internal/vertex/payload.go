package vertex

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/transform"
)

// batchGraphql 请求的固定包壳常量（逐字节对齐 PoC body.json / vertex_client）。
const (
	querySignature = "2/l8eCsMMY49imcDQ/lwwXyL8cYtTjxZBF2dNqy69LodY="
	operationName  = "StreamGenerateContentAnonymous"
)

func randomPageViewID() int64 {
	minVal := int64(1000000000000000)
	maxVal := int64(9000000000000000)
	n, err := rand.Int(rand.Reader, big.NewInt(maxVal))
	if err != nil {
		return minVal
	}
	return minVal + n.Int64()
}

func randomTrackingID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "d0000000000000000"
	}
	var out [17]byte
	out[0] = 'd'
	for i, b := range raw {
		out[i+1] = '0' + (b % 10)
	}
	return string(out[:])
}

func randomUUID() string {
	uuid := make([]byte, 16)
	_, _ = rand.Read(uuid)
	// Set version 4
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	// Set variant to RFC4122
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

// buildRequestPayload 构建发往上游的完整请求体（对齐 _build_request_payload）：
// 用 transform 构建 variables，再强制注入 region=global 与 recaptchaToken，最后包壳。
func buildRequestPayload(model string, geminiPayload map[string]any, recaptchaToken string, cfg config.ConfigProvider) map[string]any {
	vars := transform.BuildVertexVariables(model, geminiPayload, cfg)
	vars["region"] = "global"
	vars["recaptchaToken"] = recaptchaToken
	trackingID := randomTrackingID()
	return map[string]any{
		"requestContext": map[string]any{
			"clientVersion":    "boq_cloud-boq-clientweb-vertexaistudio_20260630.00_p0",
			"pagePath":         "/agent-platform/studio/multimodal",
			"pageViewId":       randomPageViewID(),
			"trackingId":       trackingID,
			"backendOverrides": map[string]any{},
			"clientSessionId":  randomUUID(),
			"selectedPurview":  map[string]any{},
			"jurisdiction":     "global",
			"localizationData": map[string]any{
				"locale":   "zh_CN",
				"timezone": "Asia/Hong_Kong",
			},
		},
		"querySignature": querySignature,
		"operationName":  operationName,
		"variables":      vars,
	}
}
