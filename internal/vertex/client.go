package vertex

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/metrics"
	"github.com/bsfdsagfadg/vertex/internal/recaptcha"
	"github.com/bsfdsagfadg/vertex/internal/spool"
	"github.com/bsfdsagfadg/vertex/internal/transport"
)

// 匿名 batchGraphql 端点常量（逐字节保持既定值）。
const (
	anonBaseURL      = "https://cloudconsole-pa.clients6.google.com"
	batchGraphqlPath = "/v3/entityServices/AiplatformEntityService/schemas/AIPLATFORM_GRAPHQL:batchGraphql"
	anonAPIKey       = "AIzaSyCI-zsRP85UVOi0DjtiCwWBwQ1djDy741g"
)

var batchGraphqlURL = anonBaseURL + batchGraphqlPath + "?key=" + anonAPIKey + "&prettyPrint=false"

// defaultSafetySettings 是 SAFETY 自动重试时注入的全 BLOCK_NONE 设置（5 类）。
var defaultSafetySettings = []any{
	map[string]any{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"},
	map[string]any{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_NONE"},
	map[string]any{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "BLOCK_NONE"},
	map[string]any{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "BLOCK_NONE"},
	map[string]any{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"},
}

// VertexAIClient 是 batchGraphql 客户端（里程碑1 非流式部分；流式留里程碑2）。
type VertexAIClient struct {
	net        *transport.NetworkClient
	pool       *recaptcha.TokenPool
	maxRetries int
}

// NewVertexAIClient 构造客户端。
func NewVertexAIClient() *VertexAIClient {
	net := transport.NewNetworkClient()
	cfg := config.Load()
	mr := cfg.MaxRetries
	if mr <= 0 {
		mr = 2
	}
	return &VertexAIClient{
		net:        net,
		pool:       recaptcha.NewTokenPoolSize(net, cfg.TokenPoolSize),
		maxRetries: mr,
	}
}

// StartTokenPool 启动后台 token 预取（main 启动时调一次）。
func (c *VertexAIClient) StartTokenPool() { c.pool.Start() }

// StopTokenPool 停止后台 token 预取并等待退出（优雅关闭时调）。
func (c *VertexAIClient) StopTokenPool() { c.pool.Stop() }

// TokenPoolStats 返回 token 池容量与当前水位（供 /metrics）。
func (c *VertexAIClient) TokenPoolStats() (size, fill int) { return c.pool.Stats() }

// CompleteChat 非流式请求（里程碑1 入口）。非流式请求主循环。
func (c *VertexAIClient) CompleteChat(ctx context.Context, model string, geminiPayload map[string]any) (map[string]any, error) {
	return c.completeInner(ctx, model, geminiPayload)
}

// CompleteChatN 并发发 n 次单候选请求，返回成功的响应列表（n 多候选用）。
// 并发候选：扇出多个 complete_chat，部分失败不连累其它候选。
//
// 每次 CompleteChat 自带完整重试/recaptcha（复用既有机制）；部分失败不连累其它候选，
// 只要至少一个成功就返回成功列表；全失败则返回第一个错误（保持出现顺序的确定性）。
func (c *VertexAIClient) CompleteChatN(ctx context.Context, model string, geminiPayload map[string]any, n int) ([]map[string]any, error) {
	type res struct {
		resp map[string]any
		err  error
	}
	results := make([]res, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			// 子 goroutine 必须自带 recover：HTTP 中间件的 withRecover 只兜请求 goroutine，
			// 这些扇出 goroutine 里任何 panic 会直接崩整个进程（掉全部在途请求）。
			// panic 转成该候选的 err。
			defer func() {
				if rec := recover(); rec != nil {
					results[idx] = res{err: NewInternalError(fmt.Sprintf("candidate panic: %v", rec))}
				}
			}()
			// 共享请求 ctx：客户端断开则所有候选的上游请求与重试一并中止。
			r, err := c.completeInner(ctx, model, geminiPayload)
			results[idx] = res{resp: r, err: err}
		}(i)
	}
	wg.Wait()

	var ok []map[string]any
	var firstErr error
	for _, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		ok = append(ok, r.resp)
	}
	if len(ok) == 0 {
		if firstErr == nil {
			firstErr = NewInternalError("All candidates failed")
		}
		return nil, firstErr
	}
	return ok, nil
}

// completeInner 非流式重试主循环。
func (c *VertexAIClient) completeInner(ctx context.Context, model string, geminiPayload map[string]any) (map[string]any, error) {
	maxRetries := c.maxRetries
	recaptchaToken := ""
	isFirstAuth := true
	attempt := 0

	sess, err := c.net.CreateSession(180)
	if err != nil {
		return nil, NewInternalError("create session: " + err.Error())
	}
	defer sess.Close()

	for attempt <= maxRetries {
		if recaptchaToken == "" {
			tok, _ := c.pool.GetToken()
			recaptchaToken = tok
			isFirstAuth = true
		}
		if recaptchaToken == "" {
			if attempt == maxRetries {
				return nil, NewAuthenticationError("Could not fetch recaptcha token.")
			}
			attempt++
			if err := sleepCtx(ctx, time.Second); err != nil {
				return nil, ctxCanceledError(err)
			}
			continue
		}

		result, reqErr := c.executeCompleteRequest(ctx, sess, model, geminiPayload, recaptchaToken, isFirstAuth)
		// SAFETY 自动重试：finishReason==SAFETY 且用户未自带 safetySettings → 注入 BLOCK_NONE 重试一次。
		// 用同 token、同 attempt 重打；retry 的结果（含可能的错误）覆盖 result/reqErr，统一走下面处理。
		if reqErr == nil {
			if _, hasSafety := geminiPayload["safetySettings"]; candidateFinish(result) == "SAFETY" && !hasSafety {
				retryPayload := shallowCopy(geminiPayload)
				retryPayload["safetySettings"] = defaultSafetySettings
				result, reqErr = c.executeCompleteRequest(ctx, sess, model, retryPayload, recaptchaToken, false)
			}
		}
		if reqErr == nil {
			return result, nil
		}

		ve := asVertexError(reqErr)
		switch {
		case ve != nil && ve.Kind == "auth":
			isVerifyFail := strings.Contains(ve.Message, "Failed to verify action") ||
				strings.Contains(ve.Message, "The caller does not have permission")
			if isFirstAuth && isVerifyFail {
				// 首次认证重试：token 不清空，同一 token 再打一次（匿名端点首帧预期 verify-fail）。
				// 这是预期内的预热、非真故障，不计入指标。
				isFirstAuth = false
				if err := sleepCtx(ctx, 500*time.Millisecond); err != nil {
					return nil, ctxCanceledError(err)
				}
				continue // attempt 与 token 都不变
			}
			metrics.Default.IncUpstreamAuth() // 真实认证/recaptcha 失败（§5 recaptcha 健康信号）
			recaptchaToken = ""
			isFirstAuth = true
			if attempt < maxRetries {
				attempt++
				if err := sleepCtx(ctx, time.Second); err != nil {
					return nil, ctxCanceledError(err)
				}
				continue
			}
			return nil, ve

		case ve != nil && ve.Kind == "ratelimit":
			metrics.Default.IncUpstream429() // 上游 429 噪音（反映上游有多吵，含被重试的）
			if attempt >= maxRetries {
				return nil, ve
			}
			// 429：销毁旧 session 重建新的，换 token
			sess.Close()
			newSess, e := c.net.CreateSession(180)
			if e != nil {
				return nil, NewInternalError("recreate session: " + e.Error())
			}
			sess = newSess
			recaptchaToken = ""
			wait := ve.RetryAfter
			if wait <= 0 {
				wait = min(10, 1+attempt)
			}
			attempt++
			if err := sleepCtx(ctx, time.Duration(wait)*time.Second); err != nil {
				return nil, ctxCanceledError(err)
			}
			continue

		case ve != nil:
			if ve.Kind == "empty" {
				metrics.Default.IncUpstreamEmpty() // 上游 0-token 空回
			}
			if !ve.IsRetryable() || attempt >= maxRetries {
				return nil, ve
			}
			attempt++
			if err := sleepCtx(ctx, backoff(attempt)); err != nil {
				return nil, ctxCanceledError(err)
			}
			continue

		default:
			// 非 VertexError 的未预期错误
			if attempt >= maxRetries {
				return nil, NewInternalError("Internal error: " + reqErr.Error())
			}
			attempt++
			if err := sleepCtx(ctx, time.Second); err != nil {
				return nil, ctxCanceledError(err)
			}
			continue
		}
	}
	return nil, NewInternalError("All retries exhausted")
}

// executeCompleteRequest 执行单次非流式请求：构建→发送→解析→返回 Gemini 格式 dict。
func (c *VertexAIClient) executeCompleteRequest(ctx context.Context, sess *transport.Session, model string, geminiPayload map[string]any, recaptchaToken string, isFirstAuth bool) (map[string]any, error) {
	cfg := config.Load()
	newBody := buildRequestPayload(model, geminiPayload, recaptchaToken, cfg)
	// 上游请求 payload 序列化到 spool 缓冲（大媒体自动落盘，避免与已解析 body 同占内存）。
	buf, err := spool.EncodeJSON(newBody)
	if err != nil {
		return nil, NewInternalError("marshal payload: " + err.Error())
	}
	defer buf.Close()
	reader, err := buf.Reader()
	if err != nil {
		return nil, NewInternalError("spool reader: " + err.Error())
	}
	header := transport.XHRHeaders(
		"application/json", "*/*",
		"https://console.cloud.google.com", "https://console.cloud.google.com/", "cross-site",
	)

	status, raw, err := sess.DoAndRead(ctx, "POST", batchGraphqlURL, header, reader)
	if err != nil {
		return nil, NewInternalError("upstream request: " + err.Error())
	}

	// HTTP 错误处理
	if status != 200 {
		errText := string(raw)
		if status == 401 || status == 403 ||
			strings.Contains(errText, "Failed to verify action") ||
			strings.Contains(errText, "The caller does not have permission") {
			return nil, NewAuthenticationError("Authentication/Recaptcha failed: " + errText)
		}
		if parsed := parseErrorResponse(errText); parsed != nil {
			parsed.UpstreamResponse = errText
			return nil, parsed
		}
		return nil, raiseForStatus(status, "", "Upstream Error: "+errText, nil, errText)
	}

	// 空数据
	if len(raw) == 0 {
		return nil, NewEmptyResponseError("Upstream returned no data")
	}

	result := ParseUpstreamData(string(raw))

	// 解析出的上游错误（无 parts 时才当作错误抛）
	if result.HasError && len(result.Parts) == 0 {
		errMsg := result.ErrorMessage
		isAuth := strings.Contains(errMsg, "Failed to verify action") ||
			strings.Contains(errMsg, "The caller does not have permission")
		if isAuth {
			return nil, NewAuthenticationError("Authentication/Recaptcha failed: " + errMsg)
		}
		if result.ErrorObj != nil {
			return nil, result.ErrorObj
		}
		lower := strings.ToLower(errMsg)
		isRate := strings.Contains(lower, "resource has been exhausted") || strings.Contains(lower, "quota")
		switch {
		case strings.Contains(lower, "not found"):
			return nil, NewNotFoundError(errMsg)
		case isRate:
			return nil, NewRateLimitError(errMsg, 0)
		default:
			return nil, NewInvalidArgumentError(errMsg)
		}
	}

	return c.buildCompleteResponse(result)
}

// buildCompleteResponse 从解析结果构建完整的 Gemini 格式响应。
func (c *VertexAIClient) buildCompleteResponse(r *ParseResult) (map[string]any, error) {
	// 上游 0-token 空回（无 parts、无 error、无 promptFeedback）→ 报错让客户端重试。
	if len(r.Parts) == 0 && !r.HasError && len(r.PromptFeedback) == 0 {
		return nil, NewEmptyResponseError("Upstream returned empty response (no content)")
	}

	allParts := r.Parts
	if len(allParts) == 0 {
		allParts = []map[string]any{{"text": " "}}
	}
	candidate := map[string]any{
		"index":   r.CandidateIndex,
		"content": map[string]any{"parts": toAnySlice(allParts), "role": "model"},
	}
	if r.FinishReason != "" {
		candidate["finishReason"] = strings.ToUpper(r.FinishReason)
	}
	setIfPresent(candidate, "finishMessage", r.FinishMessage)
	setIfPresent(candidate, "safetyRatings", r.SafetyRatings)
	setIfPresent(candidate, "citationMetadata", r.CitationMetadata)
	setIfPresent(candidate, "groundingMetadata", r.GroundingMetadata)
	setIfPresent(candidate, "tokenCount", r.TokenCount)
	setIfPresent(candidate, "avgLogprobs", r.AvgLogprobs)
	setIfPresent(candidate, "logprobsResult", r.LogprobsResult)

	resp := map[string]any{"candidates": []any{candidate}}
	setIfPresent(resp, "createTime", r.CreateTime)
	setIfPresent(resp, "modelVersion", r.ModelVersion)
	if len(r.PromptFeedback) > 0 {
		resp["promptFeedback"] = r.PromptFeedback
	}
	setIfPresent(resp, "responseId", r.ResponseID)
	if len(r.UsageMetadata) > 0 {
		resp["usageMetadata"] = r.UsageMetadata
	}
	setIfPresent(resp, "modelStatus", r.ModelStatus)
	return resp, nil
}

// ---- 小工具 ----

func candidateFinish(result map[string]any) string {
	if cands, ok := result["candidates"].([]any); ok && len(cands) > 0 {
		if c, ok := cands[0].(map[string]any); ok {
			return toStr(c["finishReason"])
		}
	}
	return ""
}

func shallowCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func asVertexError(err error) *VertexError {
	if ve, ok := err.(*VertexError); ok {
		return ve
	}
	return nil
}

func setIfPresent(m map[string]any, key string, v any) {
	if v == nil {
		return
	}
	switch x := v.(type) {
	case string:
		if x == "" {
			return
		}
	case []any:
		if len(x) == 0 {
			return
		}
	case map[string]any:
		if len(x) == 0 {
			return
		}
	}
	m[key] = v
}

// backoff 返回 min(15, 1.5^attempt) 秒（错误退避）。
func backoff(attempt int) time.Duration {
	v := math.Pow(1.5, float64(attempt))
	if v > 15 {
		v = 15
	}
	return time.Duration(v * float64(time.Second))
}

// sleepCtx 是可被 ctx 取消打断的睡眠：正常睡满返回 nil，ctx 在睡眠期间取消则立即返回 ctx.Err()。
// 重试循环里的退避用它替代裸 time.Sleep——客户端断开 / 优雅关闭时不再空睡到底再徒劳重试。
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ctxCanceledError 把 ctx 取消错误包成 VertexError，使其走统一的错误返回路径。
// 客户端已断开/在关闭，响应不会被读到，这里只为干净终止重试循环。
func ctxCanceledError(err error) error {
	return NewInternalError("request canceled: " + err.Error())
}
