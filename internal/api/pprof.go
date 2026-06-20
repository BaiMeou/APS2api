// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package api

import "net/http/pprof"

var (
	pprofIndex        = pprof.Index
	pprintCmdline     = pprof.Cmdline
	pprofProfile      = pprof.Profile
	pprofSymbol       = pprof.Symbol
	pprofTrace        = pprof.Trace
	pprofGoroutine    = pprof.Handler("goroutine").ServeHTTP
	pprofHeap         = pprof.Handler("heap").ServeHTTP
	pprofThreadcreate = pprof.Handler("threadcreate").ServeHTTP
	pprofBlock        = pprof.Handler("block").ServeHTTP
	pprofMutex        = pprof.Handler("mutex").ServeHTTP
)
