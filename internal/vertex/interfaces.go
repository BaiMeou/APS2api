// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package vertex

import (
	"context"

	"github.com/bsfdsagfadg/vertex/internal/transform"
)

type ChatCompleter interface {
	CompleteChat(ctx context.Context, model string, geminiPayload map[string]any) (map[string]any, error)
	CompleteChatN(ctx context.Context, model string, geminiPayload map[string]any, n int) ([]map[string]any, error)
	StreamChat(ctx context.Context, model string, geminiPayload map[string]any, yield func(StreamChunk) bool)
}

type ImageGenerator interface {
	CompleteChatImage(ctx context.Context, model string, geminiPayload map[string]any) ([]transform.InlineImage, error)
}

type AudioGenerator interface {
	CompleteChatAudio(ctx context.Context, model string, geminiPayload map[string]any) (AudioData, error)
}
