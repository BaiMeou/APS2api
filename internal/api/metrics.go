// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package api

import (
	"runtime"

	"github.com/bsfdsagfadg/vertex/internal/spool"
)

func metricsBody() map[string]any {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return map[string]any{
		"status": "ok",
		"memory": map[string]any{
			"alloc_mb":   bToMb(m.Alloc),
			"heap_inuse": bToMb(m.HeapInuse),
			"num_gc":     m.NumGC,
			"goroutines": runtime.NumGoroutine(),
			"spilled_mb": bToMb(uint64(spool.SpilledBytes())),
		},
	}
}

func bToMb(b uint64) float64 {
	return float64(b) / 1024 / 1024
}
