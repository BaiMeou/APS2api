package vertex

import (
	"context"
	"log"
	"sort"

	"github.com/bsfdsagfadg/vertex/internal/nodes"
)

type candidateResult struct {
	proxyURI string
	resp     map[string]any
	err      error
}

func (c *VertexAIClient) CompleteChat(ctx context.Context, model string, geminiPayload map[string]any) (map[string]any, error) {
	cands := nodes.SelectForParallel(c.cfg.GetParallelPoolSize(), c.cfg.GetParallelNodeTopK(), c.cfg.GetDebugMode(), c.cfg.GetStickyPoolEnabled())

	if !c.cfg.GetParallelPoolEnabled() || len(cands) == 0 {
		proxy := c.cfg.GetActiveNodeURI()
		if proxy == "" {
			proxy = c.cfg.GetProxyURL()
		}
		log.Printf("[Vertex] [CompleteChat] 降级为单节点运行: %s", nodes.GetNodeName(proxy))
		return c.runSingleCandidate(ctx, model, geminiPayload, proxy)
	}

	if c.cfg.GetDebugMode() {
		log.Printf("[Vertex] [CompleteChat] 启动所有候选, %d 个节点参与", len(cands))
		for _, cand := range cands {
			log.Printf("[Vertex] [CompleteChat] 参与节点: %s", cand.Name)
		}
	}

	ctxRace, cancel := context.WithCancel(ctx)
	defer cancel()

	resCh := make(chan candidateResult, len(cands))
	for _, cand := range cands {
		uri := cand.RawURI
		go func() {
			resp, err := c.runSingleCandidate(ctxRace, model, geminiPayload, uri)
			select {
			case resCh <- candidateResult{proxyURI: uri, resp: resp, err: err}:
			case <-ctxRace.Done():
			}
		}()
	}

	remaining := len(cands)
	var successes []candidateResult

	for remaining > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case res := <-resCh:
			remaining--
			if res.err != nil {
				nodes.RecordTest(res.proxyURI, false, 0, res.err.Error())
				if c.cfg.GetDebugMode() {
					log.Printf("[Vertex] [CompleteChat] 节点 %s 失败: %s", nodes.GetNodeName(res.proxyURI), res.err.Error())
				}
				continue
			}
			nodes.RecordTest(res.proxyURI, true, 50, "")
			if candidateFinish(res.resp) == "STOP" {
				cancel()
				return res.resp, nil
			}
			successes = append(successes, res)
		}
	}

	if len(successes) == 0 {
		return nil, NewEmptyResponseError("所有候选节点均失败")
	}

	return pickBestResult(successes)
}

func (c *VertexAIClient) runSingleCandidate(ctx context.Context, model string, geminiPayload map[string]any, proxyURI string) (map[string]any, error) {
	var chunks []map[string]any
	var firstErr *VertexError

	c.executeStreamingWithRetries(ctx, model, geminiPayload, proxyURI, func(chunk StreamChunk) bool {
		if chunk.Err != nil {
			if firstErr == nil {
				firstErr = chunk.Err
			}
			return false
		}
		if chunk.Data != nil {
			chunks = append(chunks, chunk.Data)
		}
		return true
	})

	if firstErr != nil {
		return nil, firstErr
	}
	if len(chunks) == 0 {
		return nil, NewEmptyResponseError("Upstream returned no data")
	}

	result := collectChunksToParseResult(chunks)
	resp, err := c.buildCompleteResponse(result)
	if err != nil {
		return nil, err
	}

	if _, hasSafety := geminiPayload["safetySettings"]; candidateFinish(resp) == "SAFETY" && !hasSafety {
		retryPayload := shallowCopy(geminiPayload)
		retryPayload["safetySettings"] = defaultSafetySettings
		return c.runSingleCandidate(ctx, model, retryPayload, proxyURI)
	}

	return resp, nil
}

func pickBestResult(results []candidateResult) (map[string]any, error) {
	sort.Slice(results, func(i, j int) bool {
		fi := candidateFinish(results[i].resp)
		fj := candidateFinish(results[j].resp)
		if fi == "MAX_TOKENS" && fj != "MAX_TOKENS" {
			return true
		}
		if fj == "MAX_TOKENS" && fi != "MAX_TOKENS" {
			return false
		}
		return responseContentLength(results[i].resp) > responseContentLength(results[j].resp)
	})
	return results[0].resp, nil
}

func responseContentLength(resp map[string]any) int {
	cands, ok := resp["candidates"].([]any)
	if !ok || len(cands) == 0 {
		return 0
	}
	c, ok := cands[0].(map[string]any)
	if !ok {
		return 0
	}
	content, ok := c["content"].(map[string]any)
	if !ok {
		return 0
	}
	parts, ok := content["parts"].([]any)
	if !ok {
		return 0
	}
	total := 0
	for _, pRaw := range parts {
		p, ok := pRaw.(map[string]any)
		if !ok {
			continue
		}
		total += len(toStr(p["text"]))
	}
	return total
}