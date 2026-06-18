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
