// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package transform

import "strings"

// FinishReasonMap 把 Gemini finishReason 映射到 OpenAI finish_reason。
var FinishReasonMap = map[string]string{ //nolint:gochecknoglobals
	"STOP":                    "stop",
	"MAX_TOKENS":              "length",
	"SAFETY":                  "content_filter",
	"RECITATION":              "content_filter",
	"PROHIBITED_CONTENT":      "content_filter",
	"TOOL_CALLS":              "tool_calls",
	"MALFORMED_FUNCTION_CALL": "tool_calls",
	"BLOCKLIST":               "content_filter",
	"SPII":                    "content_filter",
	"OTHER":                   "stop",
}

// MapFinishReason 把 Gemini finishReason 转 OpenAI finish_reason。
func MapFinishReason(finish string, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_calls"
	}
	if finish == "" {
		return "stop"
	}
	if v, ok := FinishReasonMap[strings.ToUpper(finish)]; ok {
		return v
	}
	return "stop"
}
