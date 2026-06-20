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

func RunParallel[T any](ctx context.Context, cfg config.AppConfig, op func(ctx context.Context, proxyURI string) (T, error)) (T, error) {
	cands := nodes.SelectForParallel(cfg.ParallelPoolSize)
	if !cfg.ParallelPoolEnabled || len(cands) == 0 {
		proxy := cfg.ActiveNodeURI
		if proxy == "" {
			proxy = cfg.ProxyURL
		}
		log.Printf("[Vertex] [RunParallel] 降级为单节点运行: %s", nodes.GetNodeName(proxy))
		return op(ctx, proxy)
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
			v, err := op(ctxRace, u)
			select {
			case resCh <- result{u, v, err}:
			case <-ctxRace.Done():
			}
		}(uri)
	}

	// Start the first candidate immediately
	launchNode(cands[0].RawURI)

	// Determine Hedging Delay
	hedgingDelay := 500 * time.Millisecond
	if cfg.ParallelPoolDelayDynamic {
		avgLat := nodes.GetAverageLatency()
		hedgingDelay = time.Duration(avgLat) * time.Millisecond
		if hedgingDelay < 100*time.Millisecond {
			hedgingDelay = 100 * time.Millisecond
		}
		if hedgingDelay > 2000*time.Millisecond {
			hedgingDelay = 2000 * time.Millisecond
		}
	} else if cfg.ParallelPoolDelayMs > 0 {
		hedgingDelay = time.Duration(cfg.ParallelPoolDelayMs) * time.Millisecond
	}

	timer := time.NewTimer(hedgingDelay)
	defer timer.Stop()

	candIdx := 1
	var lastErr error
	var zero T

	for {
		select {
		case res := <-resCh:
			atomic.AddInt32(&active, -1)
			name := nodes.GetNodeName(res.uri)

			if res.err == nil {
				log.Printf("[Racing] 节点 %s 成功", name)
				nodes.RecordTest(res.uri, true, 50, "")
				if ctx.Err() == nil {
					return res.val, nil
				}
			} else if res.err != context.Canceled && !errors.Is(res.err, context.Canceled) {
				log.Printf("[Racing] 节点 %s 失败: %s", name, res.err.Error())
				nodes.RecordTest(res.uri, false, 0, res.err.Error())
				if ve := asVertexError(res.err); ve != nil && !ve.IsRetryable() {
					log.Printf("[Racing] 节点 %s 触发不可重试的硬性错误，终止竞速", name)
					cancel()
					return zero, res.err
				}

				// Fast-path hedging: launch next candidate immediately on failure without waiting for timer
				if candIdx < len(cands) {
					launchNode(cands[candIdx].RawURI)
					candIdx++
					safeResetTimer(timer, hedgingDelay)
				}
			} else {
				log.Printf("[Racing] 节点 %s 被取消", name)
			}
			lastErr = res.err

			if atomic.LoadInt32(&active) == 0 && candIdx >= len(cands) {
				if lastErr != nil {
					return zero, lastErr
				}
				return zero, fmt.Errorf("all nodes failed")
			}

		case <-timer.C:
			// Hedged timeout reached, spin up next backup candidate
			if candIdx < len(cands) {
				launchNode(cands[candIdx].RawURI)
				candIdx++
				timer.Reset(hedgingDelay)
			}

		case <-ctx.Done():
			log.Printf("[Racing] 客户端断开，停止并行竞争")
			return zero, ctx.Err()
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

	// Launch first candidate immediately
	launchNode(cands[0].RawURI)

	// Determine Hedging Delay
	hedgingDelay := 500 * time.Millisecond
	if cfg.ParallelPoolDelayDynamic {
		avgLat := nodes.GetAverageLatency()
		hedgingDelay = time.Duration(avgLat) * time.Millisecond
		if hedgingDelay < 100*time.Millisecond {
			hedgingDelay = 100 * time.Millisecond
		}
		if hedgingDelay > 2000*time.Millisecond {
			hedgingDelay = 2000 * time.Millisecond
		}
	} else if cfg.ParallelPoolDelayMs > 0 {
		hedgingDelay = time.Duration(cfg.ParallelPoolDelayMs) * time.Millisecond
	}

	timer := time.NewTimer(hedgingDelay)
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
				nodes.RecordTest(r.uri, false, 0, r.err.Error())
				if ve := asVertexError(r.err); ve != nil && !ve.IsRetryable() {
					log.Printf("[Racing] 节点 %s 触发不可重试的硬性错误，终止竞速", name)
					cancel()
					yield(StreamChunk{Err: ve})
					return
				}

				// Fast-path hedging: launch immediately on error
				if candIdx < len(cands) {
					launchNode(cands[candIdx].RawURI)
					candIdx++
					safeResetTimer(timer, hedgingDelay)
				}
			}

			if atomic.LoadInt32(&active) == 0 && candIdx >= len(cands) {
				break loop
			}

		case <-timer.C:
			// No first packet arrived within threshold, invoke fallback hedging candidate
			if candIdx < len(cands) {
				launchNode(cands[candIdx].RawURI)
				candIdx++
				timer.Reset(hedgingDelay)
			}

		case <-ctx.Done():
			log.Printf("[Racing] 客户端断开，停止并行竞争")
			return
		}
	}

	if winner != nil {
		if !yield(winner.first) {
			return
		}
		for chunk := range winner.ch {
			if !yield(chunk) {
				return
			}
		}
	} else {
		yield(StreamChunk{Err: NewInternalError("all nodes failed to stream")})
	}
}
