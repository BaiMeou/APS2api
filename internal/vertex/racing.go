// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package vertex

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/nodes"
)

// safeResetTimer 安全重置定时器并排空通道，防止未读取的过期事件残留导致对冲轮询提前抢跑
func safeResetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func RunParallel[T any](ctx context.Context, cfg config.AppConfig, run func(context.Context, string) (T, error)) (T, error) {
	cands := nodes.SelectForParallel(cfg.ParallelPoolSize)
	if !cfg.ParallelPoolEnabled || len(cands) == 0 {
		proxy := cfg.ActiveNodeURI
		if proxy == "" {
			proxy = cfg.ProxyURL
		}
		log.Printf("[Vertex] [RunParallel] 降级为单节点运行: %s", nodes.GetNodeName(proxy))
		return run(ctx, proxy)
	}

	log.Printf("[Vertex] [RunParallel] 开启对冲延迟竞速, %d 个节点参与", len(cands))
	for _, c := range cands {
		log.Printf("[Vertex] [RunParallel] 参与节点: %s", c.Name)
	}

	ctxRace, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		uri string
		val T
		err error
	}

	resCh := make(chan result, len(cands)+20)
	var active int32
	activeKeys := make(map[string]bool)
	var mu sync.Mutex

	launchNode := func(uri string) {
		mu.Lock()
		if activeKeys[uri] {
			mu.Unlock()
			return
		}
		activeKeys[uri] = true
		mu.Unlock()

		atomic.AddInt32(&active, 1)
		go func(u string) {
			v, err := run(ctxRace, u)
			select {
			case resCh <- result{u, v, err}:
			case <-ctxRace.Done():
			}
		}(uri)
	}

	// 启动首个节点
	launchNode(cands[0].RawURI)

	// 计算延迟间隔
	delay := time.Duration(cfg.ParallelPoolDelayMs) * time.Millisecond
	if cfg.ParallelPoolDelayDynamic {
		delay = time.Duration(nodes.GetAverageLatency()) * time.Millisecond
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	nextIdx := 1
	var zero T

	for {
		select {
		case <-ctx.Done():
			return zero, ctx.Err()

		case <-timer.C:
			// 延迟对冲触发，启动下一个节点备份
			if nextIdx < len(cands) {
				log.Printf("[Racing] 对冲延迟唤醒，启动备份节点: %s", cands[nextIdx].Name)
				launchNode(cands[nextIdx].RawURI)
				nextIdx++
				timer.Reset(delay)
			}

		case res := <-resCh:
			atomic.AddInt32(&active, -1)
			name := nodes.GetNodeName(res.uri)

			if res.err == nil {
				log.Printf("[Racing] 竞速胜出节点: %s", name)
				nodes.RecordTest(res.uri, true, 50, "")
				return res.val, nil
			}

			// 忽略上下文主动取消
			if res.err != context.Canceled && !errors.Is(res.err, context.Canceled) {
				log.Printf("[Racing] 节点 %s 失败: %s", name, res.err.Error())

				ve := asVertexError(res.err)
				if ve != nil && ve.Kind == "ratelimit" {
					// 软降温：仅进行 30 秒静默不调度，保留原 Success/Fail 统计，防止硬性淘汰
					log.Printf("[Racing] 节点 %s 触发 429 API 限制，进入 30 秒短时歇息", name)
					nodes.RecordRateLimit(res.uri, 30)
				} else {
					nodes.RecordTest(res.uri, false, 0, res.err.Error())
				}

				if ve != nil && !ve.IsRetryable() {
					log.Printf("[Racing] 节点 %s 触发不可重试的硬性错误，终止竞速", name)
					cancel()
					return zero, res.err
				}

				// Fast-path hedging: 如果首发失败，立刻补齐后续节点，缩短故障容灾耗时
				if nextIdx < len(cands) {
					log.Printf("[Racing] 竞速失败触发极速对冲接力...")
					launchNode(cands[nextIdx].RawURI)
					nextIdx++
					safeResetTimer(timer, delay)
				}
			} else {
				log.Printf("[Racing] 节点 %s 拨号取消", name)
			}

			if atomic.LoadInt32(&active) == 0 && nextIdx >= len(cands) {
				if res.err != nil {
					return zero, res.err
				}
				return zero, fmt.Errorf("all nodes failed")
			}
		}
	}
}

func StreamParallel(ctx context.Context, cfg config.AppConfig, op func(ctx context.Context, proxyURI string) <-chan StreamChunk, yield func(StreamChunk) bool) {
	cands := nodes.SelectForParallel(cfg.ParallelPoolSize)
	if !cfg.ParallelPoolEnabled || len(cands) == 0 {
		proxy := cfg.ActiveNodeURI
		if proxy == "" {
			proxy = cfg.ProxyURL
		}
		log.Printf("[Vertex] [StreamParallel] 降级为单节点运行: %s", nodes.GetNodeName(proxy))
		for chunk := range op(ctx, proxy) {
			if !yield(chunk) {
				return
			}
		}
		return
	}

	log.Printf("[Vertex] [StreamParallel] 开启对冲延迟流式竞速, %d 个节点参与", len(cands))
	for _, c := range cands {
		log.Printf("[Vertex] [StreamParallel] 参与节点: %s", c.Name)
	}

	ctxRace, cancel := context.WithCancel(ctx)
	defer cancel()

	type res struct {
		uri   string
		ch    <-chan StreamChunk
		first StreamChunk
		err   error
	}

	resCh := make(chan res, len(cands)+20)
	var active int32
	activeKeys := make(map[string]bool)
	var mu sync.Mutex

	launchNode := func(uri string) {
		mu.Lock()
		if activeKeys[uri] {
			mu.Unlock()
			return
		}
		activeKeys[uri] = true
		mu.Unlock()

		atomic.AddInt32(&active, 1)
		go func(u string) {
			ch := op(ctxRace, u)
			select {
			case first, ok := <-ch:
				if !ok {
					select {
					case resCh <- res{u, nil, StreamChunk{}, fmt.Errorf("stream closed")}:
					case <-ctxRace.Done():
					}
				} else if first.Err != nil {
					select {
					case resCh <- res{u, nil, StreamChunk{}, first.Err}:
					case <-ctxRace.Done():
					}
				} else {
					select {
					case resCh <- res{u, ch, first, nil}:
					case <-ctxRace.Done():
					}
				}
			case <-ctxRace.Done():
			}
		}(uri)
	}

	// 启动首个节点
	launchNode(cands[0].RawURI)

	// 计算延迟
	delay := time.Duration(cfg.ParallelPoolDelayMs) * time.Millisecond
	if cfg.ParallelPoolDelayDynamic {
		delay = time.Duration(nodes.GetAverageLatency()) * time.Millisecond
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	candIdx := 1
	var winner *res

loop:
	for {
		select {
		case r := <-resCh:
			atomic.AddInt32(&active, -1)
			name := nodes.GetNodeName(r.uri)

			if r.err == nil {
				winner = &r
				log.Printf("[Vertex] [StreamParallel] 节点胜出: %s", name)
				nodes.RecordTest(r.uri, true, 50, "")
				break loop
			} else if ctx.Err() == nil && r.err != context.Canceled && !errors.Is(r.err, context.Canceled) {
				log.Printf("[Racing] 节点 %s 失败: %s", name, r.err.Error())

				ve := asVertexError(r.err)
				if ve != nil && ve.Kind == "ratelimit" {
					log.Printf("[Racing] 节点 %s 触发 429 API 限制，进入 30 秒短时歇息", name)
					nodes.RecordRateLimit(r.uri, 30)
				} else {
					nodes.RecordTest(r.uri, false, 0, r.err.Error())
				}

				if ve != nil && !ve.IsRetryable() {
					log.Printf("[Racing] 节点 %s 触发不可重试的硬性错误，终止竞速", name)
					cancel()
					yield(StreamChunk{Err: ve})
					return
				}

				// Fast-path hedging: 如果失败，立刻替补后续节点
				if candIdx < len(cands) {
					log.Printf("[Racing] 竞速失败触发极速对冲接力...")
					launchNode(cands[candIdx].RawURI)
					candIdx++
					safeResetTimer(timer, delay)
				}
			}

			if atomic.LoadInt32(&active) == 0 && candIdx >= len(cands) {
				break loop
			}

		case <-timer.C:
			// 没有及时响应，启动备份节点
			if candIdx < len(cands) {
				log.Printf("[Racing] 对冲延迟唤醒，启动备份节点: %s", cands[candIdx].Name)
				launchNode(cands[candIdx].RawURI)
				candIdx++
				timer.Reset(delay)
			}

		case <-ctx.Done():
			log.Printf("[Racing] 客户端断开，停止并行竞争")
			return
		}
	}