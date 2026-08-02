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

func WithNoCancelOnSuccess() RaceOption {
	return func(cfg *raceConfig) {
		cfg.noCancelOnSuccess = true
	}
}

func safeResetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

type raceResult[T any] struct {
	uri string
	val T
	err error
}

// RunRace runs a hedge race across multiple candidate nodes.
//
// It handles:
//   - sticky pool acquisition (when enabled)
//   - node selection via SelectForParallel
//   - sticky pool filtering (enabled: exclude sticky URIs; disabled: prepend sticky URIs as priority)
//   - fallback to single node when pool is disabled or no candidates
//   - hedge timer with static/dynamic delay
//   - result collection: first success wins immediately
//   - background collection of remaining results (30s timeout)
//   - error classification: 429 → RecordRateLimit, others → RecordTest(ok=false)
//   - hard error (non-retryable) terminates the race early
//   - context.Canceled errors are not counted as failures
func RunRace[T any](ctx context.Context, cfg config.ConfigProvider,
	run func(ctx context.Context, proxyURI string) (T, error),
	opts ...RaceOption,
) (T, error) {
	var rc raceConfig
	for _, o := range opts {
		o(&rc)
	}

	stickyPool := nodes.GetStickyPool()

	cands := nodes.SelectForParallel(cfg.ParallelPoolSize(), cfg.ParallelNodeTopK(), cfg.DebugMode(), cfg.StickyNodePriority())

	if !cfg.ParallelPoolEnabled() || len(cands) == 0 {
		proxy := cfg.ActiveNodeURI()
		if proxy == "" {
			proxy = cfg.ProxyURL()
		}
		log.Printf("[Vertex] [RunParallel] 降级为单节点运行: %s", nodes.GetNodeName(proxy))
		return run(ctx, proxy)
	}

	if cfg.DebugMode() {
		log.Printf("[Vertex] [RunParallel] 开启对冲延迟竞速, %d 个节点参与", len(cands))
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

	resCh := make(chan raceResult[T], len(cands))
	var active int32
	activeKeys := make(map[string]bool)
	var mu sync.Mutex

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

	raceTimeout := cfg.RaceTimeout()

	launchNode := func(uri string) {
		mu.Lock()
		if activeKeys[uri] {
			mu.Unlock()
			return
		}
		activeKeys[uri] = true
		mu.Unlock()

		// 单节点独立超时（RaceTimeout > 0 时生效）：某节点 x 秒未返回首包即单独淘汰，
		// 走失败分支触发对冲接力换下一个候选，不影响其他节点继续竞速，
		// 也不会让 active 永不归零而卡死整轮（429 全失败后无法进入下一次重试）。
		// 已胜出节点不受影响（胜出即 return，其余 goroutine 随 ctxRace 释放）。
		var candCtx context.Context
		var candCancel context.CancelFunc
		if raceTimeout > 0 {
			candCtx, candCancel = context.WithTimeout(ctxRace, time.Duration(raceTimeout)*time.Second)
		} else {
			candCtx, candCancel = context.WithCancel(ctxRace)
		}
		cancelsMu.Lock()
		cancels[uri] = candCancel
		cancelsMu.Unlock()

		atomic.AddInt32(&active, 1)
		go func(u string) {
			v, err := run(candCtx, u)
			if err != nil && errors.Is(err, context.DeadlineExceeded) && raceTimeout > 0 {
				err = NewUnavailableError(fmt.Sprintf("节点 %s 竞速超时（%d 秒），已淘汰", nodes.GetNodeName(u), raceTimeout))
			}
			resCh <- raceResult[T]{u, v, err}
		}(uri)
	}

	launchNode(cands[0].RawURI)

	delay := time.Duration(cfg.ParallelPoolDelayMs()) * time.Millisecond
	if cfg.ParallelPoolDelayDynamic() {
		delay = time.Duration(nodes.GetAverageLatency()) * time.Millisecond
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	nextIdx := 1
	var zero T

	for {
		select {
		case <-ctx.Done():
			cancel()
			return zero, ctx.Err()

		case <-timer.C:
			if nextIdx < len(cands) {
				if cfg.DebugMode() {
					log.Printf("[Racing] 对冲延迟唤醒，启动备份节点: %s", cands[nextIdx].Name)
				}
				launchNode(cands[nextIdx].RawURI)
				nextIdx++
				timer.Reset(delay)
			}

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

				if nextIdx < len(cands) {
					if cfg.DebugMode() {
						log.Printf("[Racing] 竞速失败触发极速对冲接力...")
					}
					launchNode(cands[nextIdx].RawURI)
					nextIdx++
					safeResetTimer(timer, delay)
				}
			} else {
				if cfg.DebugMode() {
					log.Printf("[Racing] 节点 %s 拨号取消", name)
				}
			}

			if atomic.LoadInt32(&active) == 0 && nextIdx >= len(cands) {
				cancel()
				if res.err != nil {
					return zero, res.err
				}
				return zero, fmt.Errorf("all nodes failed")
			}
		}
	}
}
