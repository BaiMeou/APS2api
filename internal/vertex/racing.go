package vertex

import (
	"context"
	"fmt"

	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/nodes"
)

func StreamParallel(ctx context.Context, cfg config.ConfigProvider,
	op func(context.Context, string) <-chan StreamChunk,
	yield func(StreamChunk) bool,
) {
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	wrappedOp := func(ctx context.Context, uri string) (<-chan StreamChunk, error) {
		ch := op(ctx, uri)
		var first StreamChunk
		var ok bool
		select {
		case first, ok = <-ch:
			if !ok {
				return nil, fmt.Errorf("stream: %s closed immediately", nodes.GetNodeName(uri))
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		if first.Err != nil {
			return nil, first.Err
		}
		rest := make(chan StreamChunk, 64)
		rest <- first
		go func() {
			defer close(rest)
			for chunk := range ch {
				select {
				case rest <- chunk:
				case <-ctx.Done():
					return
				}
			}
		}()
		return rest, nil
	}

	winnerCh, err := RunRace(streamCtx, cfg, wrappedOp, WithNoCancelOnSuccess[<-chan StreamChunk]())
	if err != nil {
		vertexErr, ok := err.(*VertexError)
		if ok {
			yield(StreamChunk{Err: vertexErr})
		} else {
			yield(StreamChunk{Err: NewInternalError(err.Error())})
		}
		return
	}
	for chunk := range winnerCh {
		if !yield(chunk) {
			return
		}
	}
}
