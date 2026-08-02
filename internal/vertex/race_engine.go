package vertex

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/cli"
	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/nodes"
)

type RaceOption func(*raceConfig)

type raceConfig struct {
	noCancelOnSuccess bool
}

type raceRoundKey struct{}

func WithNoCancelOnSuccess() RaceOption {
	return func(cfg *raceConfig) {
		cfg.noCancelOnSuccess = true
	}
}

type raceResult[T any] struct {
	uri string
	val T
	err error
}

// RunRace runs a standard concurrent race across candidate nodes.
//
// 模型：标准并发竞速（无对冲延迟）——每轮候选节点全部立即并发启动，谁先成功谁赢。
//
// 轮次换批（重试）：
//   - 单节点重试关闭（ParallelPoolRetryEnabled=false）：每轮节点全部失败后，
//     重新 SelectForParallel 换一批从未用过的节点再试，最多 MaxRetries 批
//     （总轮数 = MaxRetries + 1，每轮至多并发数个节点）。
//   - 单节点重试开启：重试在节点内 attempt 循环完成，竞速层不换批（roundBudget=0）。
//
// 其他行为：
//   - 单节点独立超时（RaceTimeout>0）：某节点超时即单独淘汰，不影响其他节点与整轮推进。
//   - sticky pool 优先/降级单节点（并发池关闭或无候选）。
//   - 429 → RecordRateLimit(30s) + Evict；其他失败 → RecordTest(false) + Evict。
//   - 不可重试硬错误立即终止整个竞速。
//   - 胜出后其余节点在后台收集（30s 上限），不影响已胜出节点的数据流。
//   - context.Canceled 不视为失败。
func RunRace[T any](ctx context.Context, cfg config.ConfigProvider,
	run func(ctx context.Context, proxyURI string) (T, error),
	opts ...RaceOption,
) (T, error) {
	var rc raceConfig
	for _, o := range opts {
		o(&rc)
	}

	stickyPool := nodes.GetStickyPool()
	raceTimeout := cfg.RaceTimeout()

	// 换批预算：关单节点重试时，重试由"换一批新节点"完成（最多 MaxRetries 批）。
	roundBudget := 0
	if cfg.ParallelPoolEnabled() && !cfg.ParallelPoolRetryEnabled() {
		roundBudget = cfg.MaxRetries()
	}

	usedURIs := make(map[string]bool)
	var zero T

	selectFreshCands := func() []nodes.Node {
		cands := nodes.SelectForParallel(cfg.ParallelPoolSize(), cfg.ParallelNodeTopK(), cfg.DebugMode(), cfg.StickyNodePriority())
		fresh := make([]nodes.Node, 0, len(cands))
		for _, c := range cands {
			if !usedURIs[c.RawURI] {
				fresh = append(fresh, c)
			}
		}
		return fresh
	}

	cands := selectFreshCands()
	if !cfg.ParallelPoolEnabled() || len(cands) == 0 {
		proxy := cfg.ActiveNodeURI()
		if proxy == "" {
			proxy = cfg.ProxyURL()
		}
		log.Printf("[Vertex] [RunParallel] 降级为单节点运行: %s", nodes.GetNodeName(proxy))
		return run(ctx, proxy)
	}

	if cfg.DebugMode() {
		log.Printf("[Vertex] [RunParallel] 标准并发竞速, %d 个节点参与", len(cands))
		for _, c := range cands {
			log.Printf("[Vertex] [RunParallel] 参与节点: %s", c.Name)
		}
	}

	cli.UpdateReqState(RequestIDFromContext(ctx), "⚡ 并发竞速", "\033[33m", fmt.Sprintf("并行节点: %d", len(cands)))

	ctxRace, cancel := context.WithCancel(ctx) //nolint:govet // cancel called on error paths; win path relies on parent ctx
	var returnedOnWinPath bool
	defer func() {
		if !returnedOnWinPath || !rc.noCancelOnSuccess {
			cancel()
		}
	}()

	cancels := make(map[string]context.CancelFunc)
	var cancelsMu sync.Mutex

	recordResult := func(res raceResult[T]) {
		if res.err == nil {
			stickyPool.Add(res.uri)
			return
		}

		if errors.Is(res.err, context.Canceled) {
			return
		}

		ve := asVertexError(res.err)
		if ve != nil && ve.Kind == "ratelimit" {
			nodes.RecordRateLimit(res.uri, 30)
			stickyPool.Evict(res.uri)
			return
		}

		nodes.RecordTest(res.uri, false, 0, res.err.Error())
		stickyPool.Evict(res.uri)
	}

	var lastErr error

	for round := 0; ; round++ {
		resCh := make(chan raceResult[T], len(cands))
		var active int32

		launchBatch := func(cands []nodes.Node, resCh chan raceResult[T], active *int32) {
			for _, c := range cands {
				uri := c.RawURI
				usedURIs[uri] = true

				var candCtx context.Context
				var candCancel context.CancelFunc
				if raceTimeout > 0 {
					candCtx, candCancel = context.WithTimeout(ctxRace, time.Duration(raceTimeout)*time.Second)
				} else {
					candCtx, candCancel = context.WithCancel(ctxRace)
				}
				candCtx = context.WithValue(candCtx, raceRoundKey{}, round)

				cancelsMu.Lock()
				cancels[uri] = candCancel
				cancelsMu.Unlock()

				atomic.AddInt32(active, 1)
				go func(u string) {
					v, err := run(candCtx, u)
					if err != nil && errors.Is(err, context.DeadlineExceeded) && raceTimeout > 0 {
						err = NewUnavailableError(fmt.Sprintf("节点 %s 竞速超时（%d 秒），已淘汰", nodes.GetNodeName(u), raceTimeout))
					}
					resCh <- raceResult[T]{u, v, err}
				}(uri)
			}
		}

		launchBatch(cands, resCh, &active)

	InnerLoop:
		for {
			select {
			case <-ctx.Done():
				cancel()
				return zero, ctx.Err()

			case res := <-resCh:
				atomic.AddInt32(&active, -1)
				name := nodes.GetNodeName(res.uri)

				if res.err == nil {
					log.Printf("[Racing] 竞速胜出节点: %s", name)
					cli.UpdateReqWinner(RequestIDFromContext(ctx), name)
					cli.UpdateReqState(RequestIDFromContext(ctx), "🟢 数据传输", "\033[32m", "已建立连接")
					nodes.RecordTest(res.uri, true, 50, "")
					stickyPool.Add(res.uri)

					returnedOnWinPath = true

					if !rc.noCancelOnSuccess {
						cancelsMu.Lock()
						for u, cancelFn := range cancels {
							if u != res.uri {
								cancelFn()
							}
						}
						cancelsMu.Unlock()
					}

					collectTimeout := time.Duration(min(30, 5+cfg.ParallelPoolSize())) * time.Second
					go func() {
						collectCtx, collectCancel := context.WithTimeout(context.Background(), collectTimeout)
						defer collectCancel()
						if atomic.LoadInt32(&active) == 0 {
							if !rc.noCancelOnSuccess {
								cancel()
							}
							return
						}
						for {
							select {
							case bgRes := <-resCh:
								atomic.AddInt32(&active, -1)
								recordResult(bgRes)
								if atomic.LoadInt32(&active) == 0 {
									if !rc.noCancelOnSuccess {
										cancel()
									}
									return
								}
							case <-collectCtx.Done():
								if !rc.noCancelOnSuccess {
									cancel()
								}
								return
							}
						}
					}()

					return res.val, nil
				}

				if !errors.Is(res.err, context.Canceled) {
					if cfg.DebugMode() {
						log.Printf("[Racing] 节点 %s 失败: %s", name, res.err.Error())
					}

					ve := asVertexError(res.err)
					if ve != nil && ve.Kind == "ratelimit" {
						if cfg.DebugMode() {
							log.Printf("[Racing] 节点 %s 触发 429 API 限制，进入 30 秒短时歇息", name)
						}
						nodes.RecordRateLimit(res.uri, 30)
						stickyPool.Evict(res.uri)
					} else {
						nodes.RecordTest(res.uri, false, 0, res.err.Error())
						stickyPool.Evict(res.uri)
					}

					if ve != nil && !ve.IsRetryable() {
						if cfg.DebugMode() {
							log.Printf("[Racing] 节点 %s 触发不可重试的硬性错误，终止竞速", name)
						}
						cancel()
						return zero, res.err
					}
				} else {
					if cfg.DebugMode() {
						log.Printf("[Racing] 节点 %s 拨号取消", name)
					}
				}

				lastErr = res.err

				if atomic.LoadInt32(&active) == 0 {
					// 本轮全部结束且无成功：换一批从未用过的节点再试（关单节点重试模式）。
					if roundBudget > 0 {
						next := selectFreshCands()
						if len(next) == 0 {
							if cfg.DebugMode() {
								log.Printf("[Racing] 新鲜节点已耗尽，清空防重过滤，允许节点跨轮次重试复用...")
							}
							usedURIs = make(map[string]bool)
							next = selectFreshCands()
						}
						if len(next) == 0 {
							cancel()
							if lastErr != nil {
								return zero, lastErr
							}
							return zero, fmt.Errorf("all nodes failed")
						}
						roundBudget--
						cands = next
						if cfg.DebugMode() {
							log.Printf("[Racing] 本轮 %d 个节点全部失败，换批重试（剩余轮次 %d）", len(cands), roundBudget)
						}
						break InnerLoop // 进入下一轮
					}
					cancel()
					if lastErr != nil {
						return zero, lastErr
					}
					return zero, fmt.Errorf("all nodes failed")
				}
			}
		}
	}
}
